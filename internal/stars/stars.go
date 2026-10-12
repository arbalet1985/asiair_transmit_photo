// Package stars measures the stars of a frame: how many there are and how
// big they are (HFR, the half flux radius). It is meant to compare frames
// of one night: clouds lower the star count, lost focus, wind or bad
// seeing raise the HFR.
package stars

import (
	"errors"
	"math"
	"os"
	"sort"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// Result of a measurement.
type Result struct {
	Stars int     // stars found, saturated ones too (hot pixels and nebulae left out)
	HFR   float64 // median half flux radius in pixels of the original frame; 0 without stars
}

// MaxWidth is the width frames are binned to before measuring: fast enough
// for tens of megapixels, while a 2 px HFR star still spans several pixels.
const MaxWidth = 3200

// MeasureFile reads a FITS file and measures it.
func MeasureFile(name string) (Result, error) {
	f, err := os.Open(name)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	img, err := fits.Preview(f, MaxWidth)
	if err != nil {
		return Result{}, err
	}
	return Measure(img), nil
}

// Measure finds the stars of an image (colour channels are averaged).
// Saturated stars count as stars but, as in Siril, not for the size: their
// clipped top makes them look bigger than they are.
func Measure(img *fits.Image) Result {
	found := find(img)
	if len(found) == 0 {
		return Result{}
	}
	sized := unsaturated(found)
	if len(sized) == 0 {
		sized = found
	}
	// the size is taken from the brighter half: the faintest stars are
	// mostly noise
	sort.Slice(sized, func(i, j int) bool { return sized[i].Flux > sized[j].Flux })
	bright := sized[:max(1, (len(sized)+1)/2)]
	hfrs := make([]float64, len(bright))
	for i, s := range bright {
		hfrs[i] = s.HFR
	}
	sort.Float64s(hfrs)
	return Result{Stars: len(found), HFR: hfrs[len(hfrs)/2]}
}

// unsaturated returns the stars that are not saturated.
func unsaturated(found []Star) []Star {
	var out []Star
	for _, s := range found {
		if !s.Saturated {
			out = append(out, s)
		}
	}
	return out
}

// Star is a star found: its centre and HFR in pixels of the original frame.
type Star struct {
	X, Y, HFR, Flux float64
	Saturated       bool // its top is clipped: the HFR is too big
}

// find returns the stars of an image (colour channels are averaged).
func find(img *fits.Image) []Star {
	w, h := img.Width, img.Height
	lum := img.Data
	if img.Channels > 1 {
		lum = make([]float32, w*h)
		for i := range lum {
			var s float32
			for c := 0; c < img.Channels; c++ {
				s += img.Data[i*img.Channels+c]
			}
			lum[i] = s / float32(img.Channels)
		}
	}
	bin := float64(max(img.Binning, 1))
	found := measure(lum, w, h, saturation(img))
	for i := range found {
		s := &found[i]
		// the centre of a preview pixel
		s.X, s.Y, s.HFR = (s.X+0.5)*bin, (s.Y+0.5)*bin, s.HFR*bin
	}
	return found
}

// background returns the median and the noise (from the median absolute
// deviation) of a sample of the pixels.
func background(d []float32) (med, sigma float32) {
	step := max(1, len(d)/250000)
	s := make([]float32, 0, len(d)/step+1)
	for i := 0; i < len(d); i += step {
		s = append(s, d[i])
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	med = s[len(s)/2]
	for i, v := range s {
		s[i] = float32(math.Abs(float64(v - med)))
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	sigma = 1.4826 * s[len(s)/2]
	if sigma < 1e-6 {
		sigma = 1e-6
	}
	return med, sigma
}

const (
	detectSigma = 5    // a star pixel is this many noise sigmas above the background
	minPixels   = 3    // smaller spots are hot pixels or noise
	maxPixels   = 2500 // bigger ones are nebulae, galaxies or bloated bright stars
	maxStars    = 30000
)

// Saturation as Siril tells it (star_finder.c), in parts of the range from
// the background to the brightest sample of the frame: a star whose centre
// and its neighbours are above satLevel of it, and flat within satFlat of
// it, is saturated (a plateau). A smaller clipped core is no plateau: Siril
// finds it by the fitted profile rising above the range, here by the
// brightest pixel of the star within satTop of the brightest sample.
const (
	satLevel = 0.7
	satFlat  = 0.1
	satTop   = 0.02
)

// saturation returns the test of a star by the pixels (of the preview) of
// its centre and its brightest one: saturated in some channel. The level
// of saturation is the brightest sample of the whole frame (img.Max), so a
// sensor that clips below the top of its 16 bits is caught too.
func saturation(img *fits.Image) func(centre, brightest int) bool {
	w, h, ch := img.Width, img.Height, img.Channels
	// per channel c: the levels of satLevel, satFlat and satTop
	type limit struct {
		c                 int
		level, flat, clip float32
	}
	var lims []limit
	for c := 0; c < ch; c++ {
		v := make([]float32, 0, len(img.Data)/ch)
		top := float32(0)
		for i := c; i < len(img.Data); i += ch {
			v = append(v, img.Data[i])
			top = max(top, img.Data[i])
		}
		if c < len(img.Max) && img.Max[c] > 0 {
			top = img.Max[c]
		}
		bg, _ := background(v)
		if dyn := top - bg; dyn > 0 {
			lims = append(lims, limit{c: c, level: bg + satLevel*dyn, flat: satFlat * dyn, clip: top - satTop*dyn})
		}
	}
	return func(p, brightest int) bool {
		x, y := p%w, p/w
		for _, l := range lims {
			c := l.c
			if img.Data[brightest*ch+c] >= l.clip {
				return true
			}
			v := img.Data[p*ch+c]
			var sum float32
			low := v
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if dx == 0 && dy == 0 || xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					q := img.Data[(yy*w+xx)*ch+c]
					sum += q
					low = min(low, q)
					n++
				}
			}
			if n > 0 && sum/float32(n) >= l.level && v-low <= l.flat {
				return true
			}
		}
		return false
	}
}

var errTooBig = errors.New("too big")

// measure returns the stars of d, in its pixels; sat tells a saturated
// star by the pixel of its centre and its brightest one (nil: none are).
func measure(d []float32, w, h int, sat func(centre, brightest int) bool) []Star {
	if w < 32 || h < 32 {
		return nil
	}
	med, sigma := background(d)
	thr := med + detectSigma*sigma
	seen := make([]bool, len(d))
	stack := make([]int, 0, 256)
	var found []Star
	comps := 0
	for start, v := range d {
		if v <= thr || seen[start] {
			continue
		}
		comps++
		if comps > maxStars*4 {
			break // a frame full of noise or a bright nebula: enough
		}
		// flood the connected pixels above the threshold
		stack = append(stack[:0], start)
		seen[start] = true
		var n int
		top := start // the brightest pixel
		var peak, sum, sx, sy float64
		minX, maxX, minY, maxY := w, -1, h, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			f := float64(d[p] - med)
			n++
			sum += f
			sx += f * float64(x)
			sy += f * float64(y)
			if float64(d[p]) > peak {
				peak, top = float64(d[p]), p
			}
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
			for _, q := range [4]int{p - 1, p + 1, p - w, p + w} {
				if q < 0 || q >= len(d) || seen[q] || d[q] <= thr {
					continue
				}
				if (q == p-1 || q == p+1) && q/w != y {
					continue // wrapped to the next row
				}
				seen[q] = true
				stack = append(stack, q)
			}
		}
		if n < minPixels || n > maxPixels || sum <= 0 {
			continue
		}
		// elongated streaks (satellites, planes) are not stars
		if bw, bh := maxX-minX+1, maxY-minY+1; bw > 4*bh || bh > 4*bw {
			continue
		}
		cx, cy := sx/sum, sy/sum
		r := math.Min(math.Max(3*math.Sqrt(float64(n)/math.Pi), 4), 25)
		if cx < r+ring || cy < r+ring || cx >= float64(w)-r-ring || cy >= float64(h)-r-ring {
			continue // cut by the edge
		}
		// the background around the star: a gradient or a nebula makes
		// it brighter than that of the frame
		bg := localBackground(d, w, cx, cy, r)
		if peak-float64(bg) < detectSigma*float64(sigma) {
			continue // a brighter patch of the background, not a star
		}
		if v, ok := hfrAt(d, w, bg, cx, cy, r); ok {
			found = append(found, Star{X: cx, Y: cy, HFR: v, Flux: sum, Saturated: sat != nil && sat(int(cy+0.5)*w+int(cx+0.5), top)})
			if len(found) >= maxStars {
				break
			}
		}
	}
	return found
}

// ring: the width of the ring around a star its background is taken from
const ring = 4

// localBackground is the median of the ring from r to r+ring around a star.
func localBackground(d []float32, w int, cx, cy, r float64) float32 {
	var v []float32
	ro := r + ring
	for y := int(cy - ro); y <= int(cy+ro); y++ {
		for x := int(cx - ro); x <= int(cx+ro); x++ {
			if dist := math.Hypot(float64(x)-cx, float64(y)-cy); dist > r && dist <= ro {
				v = append(v, d[y*w+x])
			}
		}
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
}

// hfrAt is the flux weighted mean distance from the centre within radius r.
func hfrAt(d []float32, w int, med float32, cx, cy, r float64) (float64, bool) {
	var sf, sfr float64
	x0, x1 := int(cx-r), int(cx+r)
	y0, y1 := int(cy-r), int(cy+r)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			dist := math.Hypot(dx, dy)
			if dist > r {
				continue
			}
			// noise below the background is kept: it averages out, while
			// clipping it would make every star look bigger
			f := float64(d[y*w+x] - med)
			sf += f
			sfr += f * dist
		}
	}
	if sf <= 0 {
		return 0, false
	}
	v := sfr / sf
	return v, v > 0 && v < 0.8*r
}
