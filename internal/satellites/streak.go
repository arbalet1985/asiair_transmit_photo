package satellites

import (
	"math"
	"sort"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// Streak is a straight track on a frame: its ends in pixels of the frame
// (0-based, the row order of the file) and whether an end is at the edge of
// the frame (the object went on beyond it).
type Streak struct {
	X0, Y0, X1, Y1 float64
	Edge0, Edge1   bool
	SNR            float64 // mean signal along it over the noise
	Soft           float64 // its width: the signal beside it over that on it
}

// Length in pixels.
func (s Streak) Length() float64 { return math.Hypot(s.X1-s.X0, s.Y1-s.Y0) }

const (
	streakWidth  = 800 // the image is binned to about this width to look for tracks
	streakSigma  = 3   // pixels of a track are this many noise sigmas above the background
	minSegment   = 12  // binned pixels: a shorter piece is not taken
	minStreak    = 30  // binned pixels: the shortest track reported
	maxSoftness  = 0.6 // signal 3 binned pixels beside a track over that on it: wider is a nebula
	maxWidthPx   = 6   // binned pixels: wider pieces are nebulae, galaxies, halos
	minElongated = 4   // length over width of a piece of a track
	maxStreaks   = 20
	edgeReach    = 8 // binned pixels beyond an end still taken for the edge
)

// FindStreaks finds the straight tracks of satellites (planes and meteors
// too) on a preview of a frame.
func FindStreaks(img *fits.Image) []Streak {
	lum, w, h, k := binLum(img, streakWidth)
	if w < 64 || h < 64 {
		return nil
	}
	res := subtractBackground(lum, w, h)
	sigma := noise(res)
	segs := segments(res, w, h, streakSigma*sigma)
	lines := mergeSegments(segs)
	// the pieces followed to their ends, then those of one track joined
	var pieces []line
	for _, l := range lines {
		t0, t1, _ := refineEnds(res, w, h, l)
		if softness(res, w, h, l, t0, t1) <= maxSoftness {
			l.t0, l.t1 = t0, t1
			pieces = append(pieces, l)
		}
	}
	lines = joinPieces(pieces)
	var out []Streak
	bin := float64(max(img.Binning, 1) * k)
	srcH := float64(img.Height * max(img.Binning, 1))
	flip := img.Header != nil && strings.EqualFold(img.Header.Get("ROWORDER"), "BOTTOM-UP")
	var kept []line
	for _, l := range lines {
		t0, t1, snr := refineEnds(res, w, h, l)
		soft := softness(res, w, h, l, t0, t1)
		if t1-t0 < minStreak || soft > maxSoftness {
			continue
		}
		l.t0, l.t1 = t0, t1
		if within(l, kept) {
			continue // a piece of a track already taken
		}
		kept = append(kept, l)
		at := func(t float64) (float64, float64) { return l.cx + l.dx*t, l.cy + l.dy*t }
		ax, ay := at(t0)
		bx, by := at(t1)
		// an end at the edge: the track goes on out of the frame. The ends
		// are measured with the sky 6 pixels beside the track, so a slanted
		// one stops a few pixels short of the edge: look a bit further on.
		outside := func(x, y float64) bool { return x < 3 || y < 3 || x > float64(w)-4 || y > float64(h)-4 }
		s := Streak{Edge0: outside(at(t0 - edgeReach)), Edge1: outside(at(t1 + edgeReach)), SNR: snr, Soft: soft}
		// the centre of a binned pixel in pixels of the frame
		s.X0, s.Y0 = (ax+0.5)*bin-0.5, (ay+0.5)*bin-0.5
		s.X1, s.Y1 = (bx+0.5)*bin-0.5, (by+0.5)*bin-0.5
		if flip {
			s.Y0, s.Y1 = srcH-1-s.Y0, srcH-1-s.Y1
		}
		out = append(out, s)
		if len(out) >= maxStreaks {
			break
		}
	}
	return out
}

// binLum averages the colour channels and bins the image by k to at most
// maxW pixels wide.
func binLum(img *fits.Image, maxW int) ([]float32, int, int, int) {
	k := (img.Width + maxW - 1) / maxW
	k = max(k, 1)
	w, h := img.Width/k, img.Height/k
	out := make([]float32, w*h)
	ch := img.Channels
	n := float32(k * k * ch)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			for yy := y * k; yy < y*k+k; yy++ {
				row := yy * img.Width
				for xx := x * k; xx < x*k+k; xx++ {
					for c := 0; c < ch; c++ {
						s += img.Data[(row+xx)*ch+c]
					}
				}
			}
			out[y*w+x] = s / n
		}
	}
	return out, w, h, k
}

const bgBlock = 32

// subtractBackground removes the sky background, the median of blocks
// interpolated between their centres.
func subtractBackground(d []float32, w, h int) []float32 {
	bw, bh := (w+bgBlock-1)/bgBlock, (h+bgBlock-1)/bgBlock
	med := make([]float32, bw*bh)
	buf := make([]float32, 0, bgBlock*bgBlock)
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			buf = buf[:0]
			for y := by * bgBlock; y < min(h, (by+1)*bgBlock); y++ {
				for x := bx * bgBlock; x < min(w, (bx+1)*bgBlock); x++ {
					buf = append(buf, d[y*w+x])
				}
			}
			sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
			med[by*bw+bx] = buf[len(buf)/2]
		}
	}
	out := make([]float32, len(d))
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/bgBlock - 0.5
		y0 := int(math.Floor(fy))
		ty := float32(fy - float64(y0))
		y0c, y1c := clamp(y0, bh), clamp(y0+1, bh)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/bgBlock - 0.5
			x0 := int(math.Floor(fx))
			tx := float32(fx - float64(x0))
			x0c, x1c := clamp(x0, bw), clamp(x0+1, bw)
			top := med[y0c*bw+x0c]*(1-tx) + med[y0c*bw+x1c]*tx
			bot := med[y1c*bw+x0c]*(1-tx) + med[y1c*bw+x1c]*tx
			out[y*w+x] = d[y*w+x] - (top*(1-ty) + bot*ty)
		}
	}
	return out
}

func clamp(i, n int) int { return min(max(i, 0), n-1) }

// noise: the sigma of the background from the median absolute deviation.
func noise(d []float32) float32 {
	step := max(1, len(d)/200000)
	s := make([]float32, 0, len(d)/step+1)
	for i := 0; i < len(d); i += step {
		s = append(s, d[i])
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	m := s[len(s)/2]
	for i, v := range s {
		s[i] = float32(math.Abs(float64(v - m)))
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return max(1.4826*s[len(s)/2], 1e-7)
}

// line is a piece of a track: through (cx, cy) along (dx, dy), from t0 to t1.
type line struct {
	cx, cy, dx, dy float64
	t0, t1         float64
	px             [][2]float64 // its pixels
}

// segments returns the elongated groups of pixels above thr.
func segments(d []float32, w, h int, thr float32) []line {
	seen := make([]bool, len(d))
	var out []line
	stack := make([]int, 0, 256)
	var pix [][2]float64
	for start, v := range d {
		if v <= thr || seen[start] {
			continue
		}
		stack = append(stack[:0], start)
		seen[start] = true
		pix = pix[:0]
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			pix = append(pix, [2]float64{float64(x), float64(y)})
			if len(pix) > 20000 {
				break // a nebula: no track
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					q := yy*w + xx
					if !seen[q] && d[q] > thr {
						seen[q] = true
						stack = append(stack, q)
					}
				}
			}
		}
		if len(pix) < 6 || len(pix) > 20000 {
			continue
		}
		l, width := fit(pix)
		if width > maxWidthPx && l.t1-l.t0 >= minElongated*width {
			// a track through a galaxy or touching stars is one group with
			// them: its line is the pixels near the axis, most of the group
			if core, cl, cw := coreOf(pix, l); 2*len(core) >= len(pix) {
				pix, l, width = core, cl, cw
			}
		}
		if length := l.t1 - l.t0; length >= minSegment && width <= maxWidthPx && length >= minElongated*width {
			l.px = append([][2]float64(nil), pix...)
			out = append(out, l)
		}
	}
	return out
}

// fit: the line through pixels by their principal axis, and the width.
func fit(pix [][2]float64) (line, float64) {
	var mx, my float64
	for _, p := range pix {
		mx += p[0]
		my += p[1]
	}
	n := float64(len(pix))
	mx, my = mx/n, my/n
	var sxx, syy, sxy float64
	for _, p := range pix {
		dx, dy := p[0]-mx, p[1]-my
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	sxx, syy, sxy = sxx/n, syy/n, sxy/n
	tr, det := sxx+syy, sxx*syy-sxy*sxy
	l1 := tr/2 + math.Sqrt(math.Max(0, tr*tr/4-det))
	l2 := math.Max(0, tr-l1)
	ang := 0.5 * math.Atan2(2*sxy, sxx-syy)
	l := line{cx: mx, cy: my, dx: math.Cos(ang), dy: math.Sin(ang), t0: math.Inf(1), t1: math.Inf(-1)}
	for _, p := range pix {
		t := (p[0]-mx)*l.dx + (p[1]-my)*l.dy
		l.t0, l.t1 = math.Min(l.t0, t), math.Max(l.t1, t)
	}
	return l, math.Max(1, math.Sqrt(12*l2))
}

// coreOf: the pixels within 2.5 pixels of the line, its line fitted again
// over them a few times, and its width; nil if too few are left.
func coreOf(pix [][2]float64, l line) ([][2]float64, line, float64) {
	var width float64
	for i := 0; i < 3; i++ {
		var core [][2]float64
		for _, p := range pix {
			if math.Abs(-(p[0]-l.cx)*l.dy+(p[1]-l.cy)*l.dx) <= 2.5 {
				core = append(core, p)
			}
		}
		if len(core) < 6 {
			return nil, l, 0
		}
		pix = core
		l, width = fit(pix)
	}
	return pix, l, width
}

// mergeSegments joins the pieces of one track (broken by noise, or a
// tumbling object flashing) and returns the tracks, longest first.
func mergeSegments(segs []line) []line {
	sort.Slice(segs, func(i, j int) bool { return segs[i].t1-segs[i].t0 > segs[j].t1-segs[j].t0 })
	used := make([]bool, len(segs))
	var out []line
	for i := range segs {
		if used[i] {
			continue
		}
		used[i] = true
		cur := segs[i]
		pix := append([][2]float64(nil), cur.px...)
		for changed := true; changed; {
			changed = false
			for j := range segs {
				if used[j] || !collinear(cur, segs[j]) {
					continue
				}
				used[j] = true
				pix = append(pix, segs[j].px...)
				cur, _ = fit(pix)
				changed = true
			}
		}
		cur.px = pix // kept for joining the pieces
		out = append(out, cur)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].t1-out[i].t0 > out[j].t1-out[j].t0 })
	return out
}

// collinear: piece b lies on the line of a, not too far beyond its ends.
func collinear(a, b line) bool {
	if math.Abs(a.dx*b.dy-a.dy*b.dx) > math.Sin(3*math.Pi/180) {
		return false
	}
	// the direction of a short piece is uncertain: its ends may stray more
	off := func(x, y float64) float64 { return math.Abs(-(x-a.cx)*a.dy + (y-a.cy)*a.dx) }
	if off(b.cx, b.cy) > 2.5 {
		return false
	}
	for _, t := range []float64{b.t0, b.t1} {
		if off(b.cx+b.dx*t, b.cy+b.dy*t) > 2.5+0.05*(b.t1-b.t0) {
			return false
		}
	}
	// the gap along the line
	p := func(t float64) float64 { return (b.cx+b.dx*t-a.cx)*a.dx + (b.cy+b.dy*t-a.cy)*a.dy }
	lo, hi := math.Min(p(b.t0), p(b.t1)), math.Max(p(b.t0), p(b.t1))
	return lo < a.t1+60 && hi > a.t0-60
}

const (
	joinOffset = 3   // binned pixels: the ends of the pieces of a track lie this close to its line
	joinGap    = 100 // binned pixels: the longest gap between them
	// between long pieces the gap may be as long as the shorter piece (a
	// satellite fading, crossing another track): both lying within
	// joinOffset of one line over the whole length is proof enough
	joinGapMax = 250
)

// joinPieces joins the pieces of one track found apart: a faint track breaks
// where the noise hides it for longer than the pieces are followed, and a
// tumbling object flashes. Longest first.
func joinPieces(ls []line) []line {
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(ls) && !changed; i++ {
			for j := i + 1; j < len(ls); j++ {
				if m, ok := joined(ls[i], ls[j]); ok {
					ls[i] = m
					ls = append(ls[:j], ls[j+1:]...)
					changed = true
					break
				}
			}
		}
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].t1-ls[i].t0 > ls[j].t1-ls[j].t0 })
	return ls
}

// joined: the line from end to end of two pieces of one track.
func joined(a, b line) (line, bool) {
	var p [4][2]float64
	for i, l := range []line{a, b} {
		p[2*i] = [2]float64{l.cx + l.dx*l.t0, l.cy + l.dy*l.t0}
		p[2*i+1] = [2]float64{l.cx + l.dx*l.t1, l.cy + l.dy*l.t1}
	}
	// the two ends farthest apart
	var u, v [2]float64
	best := -1.0
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if d := math.Hypot(p[j][0]-p[i][0], p[j][1]-p[i][1]); d > best {
				best, u, v = d, p[i], p[j]
			}
		}
	}
	if best <= 0 {
		return line{}, false
	}
	dx, dy := (v[0]-u[0])/best, (v[1]-u[1])/best
	along := func(q [2]float64) float64 { return (q[0]-u[0])*dx + (q[1]-u[1])*dy }
	for _, q := range p {
		if math.Abs(-(q[0]-u[0])*dy+(q[1]-u[1])*dx) > joinOffset {
			return line{}, false
		}
	}
	a0, a1 := math.Min(along(p[0]), along(p[1])), math.Max(along(p[0]), along(p[1]))
	b0, b1 := math.Min(along(p[2]), along(p[3])), math.Max(along(p[2]), along(p[3]))
	if math.Max(b0-a1, a0-b1) > math.Max(joinGap, math.Min(joinGapMax, math.Min(a1-a0, b1-b0))) {
		return line{}, false
	}
	// the line again over the pixels of both: the ends of a short piece are
	// along its own direction, not quite that of the track
	l := line{cx: (u[0] + v[0]) / 2, cy: (u[1] + v[1]) / 2, dx: dx, dy: dy}
	if px := append(append([][2]float64(nil), a.px...), b.px...); len(px) >= 6 {
		l, _ = fit(px)
		l.px = px
	}
	l.t0, l.t1 = math.Inf(1), math.Inf(-1)
	for _, q := range p {
		t := (q[0]-l.cx)*l.dx + (q[1]-l.cy)*l.dy
		l.t0, l.t1 = math.Min(l.t0, t), math.Max(l.t1, t)
	}
	return l, true
}

// softness: how wide the track is, as the signal 3 pixels beside it over
// that on it, averaged along it. A track is as sharp as the stars (near 0
// when binned); the filaments of a nebula, long and thin as they may be,
// are wider and soft.
func softness(d []float32, w, h int, l line, t0, t1 float64) float64 {
	var prof [17]float64
	n := 0
	nx, ny := -l.dy, l.dx
	for t := t0; t <= t1; t++ {
		var p [17]float64
		ok := true
		for i := range p {
			o := float64(i - 8)
			x, y := l.cx+l.dx*t+nx*o, l.cy+l.dy*t+ny*o
			xi, yi := int(x+0.5), int(y+0.5)
			if xi < 0 || yi < 0 || xi >= w || yi >= h {
				ok = false
				break
			}
			p[i] = float64(d[yi*w+xi])
		}
		if ok {
			for i := range p {
				prof[i] += p[i]
			}
			n++
		}
	}
	if n == 0 {
		return 1
	}
	bg := (prof[0] + prof[1] + prof[15] + prof[16]) / 4
	peak := math.Max(prof[8], math.Max(prof[7], prof[9])) - bg
	if peak <= 0 {
		return 1
	}
	return ((prof[5]+prof[11])/2 - bg) / peak
}

// within: the line lies on one of the lines kept, mostly inside its ends.
func within(l line, kept []line) bool {
	for _, k := range kept {
		in := true
		var u [2]float64
		for i, t := range []float64{l.t0, l.t1} {
			x, y := l.cx+l.dx*t, l.cy+l.dy*t
			u[i] = (x-k.cx)*k.dx + (y-k.cy)*k.dy
			if math.Abs(-(x-k.cx)*k.dy+(y-k.cy)*k.dx) > 4 {
				in = false
				break
			}
		}
		u0, u1 := math.Min(u[0], u[1]), math.Max(u[0], u[1])
		if in && (u0 >= k.t0-10 && u1 <= k.t1+10 || math.Min(u1, k.t1)-math.Max(u0, k.t0) >= (u1-u0)/2) {
			return true
		}
	}
	return false
}

// refineEnds follows the track beyond the pixels found, where it is too
// faint to pass the threshold pixel by pixel but shows averaged along it.
// It returns the ends along the line and the signal to noise of the track.
func refineEnds(d []float32, w, h int, l line) (float64, float64, float64) {
	at := func(x, y float64) (float32, bool) {
		x0, y0 := int(math.Floor(x)), int(math.Floor(y))
		if x0 < 0 || y0 < 0 || x0+1 >= w || y0+1 >= h {
			return 0, false
		}
		tx, ty := float32(x-float64(x0)), float32(y-float64(y0))
		i := y0*w + x0
		return (d[i]*(1-tx)+d[i+1]*tx)*(1-ty) + (d[i+w]*(1-tx)+d[i+w+1]*tx)*ty, true
	}
	nx, ny := -l.dy, l.dx
	// signal across the line at offset off: on it minus beside it
	sig := func(t, off float64) (float64, bool) {
		cx, cy := l.cx+l.dx*t+nx*off, l.cy+l.dy*t+ny*off
		var on, side float32
		for _, o := range []float64{-1, 0, 1} {
			v, ok := at(cx+nx*o, cy+ny*o)
			if !ok {
				return 0, false
			}
			on += v
		}
		for _, o := range []float64{-6, 6} {
			v, ok := at(cx+nx*o, cy+ny*o)
			if !ok {
				return 0, false
			}
			side += v
		}
		return float64(on/3 - side/2), true
	}
	const ext, win, longWin = 80.0, 3, 10
	lo, hi := l.t0-ext, l.t1+ext
	n := int(hi-lo) + 1
	on := make([]float64, n)
	ctl := make([]float64, 0, n)
	ok := make([]bool, n)
	for i := 0; i < n; i++ {
		t := lo + float64(i)
		on[i], ok[i] = sig(t, 0)
		if v, okc := sig(t, 15); okc {
			ctl = append(ctl, v)
		}
		if v, okc := sig(t, -15); okc {
			ctl = append(ctl, v)
		}
	}
	smooth := func(v []float64, okv []bool, win int) []float64 {
		out := make([]float64, len(v))
		for i := range v {
			var s float64
			c := 0
			for j := max(0, i-win); j <= min(len(v)-1, i+win); j++ {
				if okv == nil || okv[j] {
					s += v[j]
					c++
				}
			}
			if c > 0 {
				out[i] = s / float64(c)
			}
		}
		return out
	}
	// spread: the sigma of values around zero from their median absolute value
	spread := func(v []float64) float64 {
		a := make([]float64, len(v))
		for i, x := range v {
			a[i] = math.Abs(x)
		}
		sort.Float64s(a)
		if len(a) == 0 {
			return 1e-9
		}
		return math.Max(1.4826*a[len(a)/2], 1e-9)
	}
	// extend follows the track outward from i0 and i1 while the signal is
	// above thr, over gaps of a few points
	extend := func(sm []float64, thr float64, i0, i1 int) (int, int) {
		for gap := 0; i0 > 0 && gap <= 4; {
			if ok[i0-1] && sm[i0-1] > thr {
				gap = 0
			} else {
				gap++
			}
			i0--
		}
		for i0 < n-1 && !(ok[i0] && sm[i0] > thr) {
			i0++
		}
		for gap := 0; i1 < n-1 && gap <= 4; {
			if ok[i1+1] && sm[i1+1] > thr {
				gap = 0
			} else {
				gap++
			}
			i1++
		}
		for i1 > 0 && !(ok[i1] && sm[i1] > thr) {
			i1--
		}
		return i0, i1
	}
	sm := smooth(on, ok, win)
	// the noise of the smoothed signal, from lines beside the track
	sn := spread(smooth(ctl, nil, win))
	i0, i1 := extend(sm, 3*sn, int(l.t0-lo), int(l.t1-lo))
	if i1 <= i0 {
		return l.t0, l.t1, 0
	}
	var s float64
	for i := i0; i <= i1; i++ {
		s += sm[i]
	}
	snr := s / float64(i1-i0+1) / sn
	// A faint track sinks below the threshold here and there and would end
	// there: it is followed on averaged over a longer piece, the points
	// clipped so that a star on it or beside it does not stop it. The end is
	// where the signal falls to half that of the track.
	lim := 4 * spread(ctl)
	clip := func(v []float64) []float64 {
		out := make([]float64, len(v))
		for i, x := range v {
			out[i] = math.Max(-lim, math.Min(lim, x))
		}
		return out
	}
	lsm := smooth(clip(on), ok, longWin)
	level := append([]float64(nil), lsm[i0:i1+1]...)
	sort.Float64s(level)
	thr := math.Max(3*spread(smooth(clip(ctl), nil, longWin)), level[len(level)/2]/2)
	if j0, j1 := extend(lsm, thr, i0, i1); j1 > j0 {
		i0, i1 = min(i0, j0), max(i1, j1)
	}
	return lo + float64(i0), lo + float64(i1), snr
}
