package satellites

import (
	"errors"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

// Frame is what is known of a frame for telling its satellites.
type Frame struct {
	WCS           *fits.WCS
	Width, Height int
	Start         time.Time // of the exposure, UTC
	Exposure      time.Duration
	Site          Site
}

// FrameFromHeader reads a frame's WCS, time and site from its header;
// site is used when the header has no coordinates (nil: required).
func FrameFromHeader(h *fits.Header, name string, site *Site) (Frame, error) {
	var f Frame
	w, ok := fits.ReadWCS(h, path.Base(strings.ReplaceAll(name, "\\", "/")))
	if !ok {
		return f, errors.New("в заголовке нет ни решения кадра, ни наведения с фокусом и углом камеры")
	}
	f.WCS = w
	f.Width, f.Height = atoi(h.Get("NAXIS1")), atoi(h.Get("NAXIS2"))
	if f.Width <= 0 || f.Height <= 0 {
		return f, errors.New("нет размеров кадра")
	}
	start, ok := dateObs(h.Get("DATE-OBS"))
	if !ok {
		return f, errors.New("нет времени начала выдержки (DATE-OBS)")
	}
	f.Start = start
	exp := atof(h.Get("EXPTIME"))
	if exp <= 0 {
		exp = atof(h.Get("EXPOSURE"))
	}
	if exp <= 0 {
		return f, errors.New("нет выдержки (EXPTIME)")
	}
	f.Exposure = time.Duration(exp * float64(time.Second))
	if lat, lon, ok := moon.Site(h.Get); ok {
		f.Site = Site{Lat: lat, Lon: lon}
	} else if site != nil {
		f.Site = *site
	} else {
		return f, errors.New("нет координат обсерватории")
	}
	return f, nil
}

func dateObs(v string) (time.Time, bool) {
	for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(l, strings.TrimSuffix(strings.TrimSpace(v), "Z")); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Match: the object that left a track.
type Match struct {
	Streak  int // index of the track
	Orbit   *Orbit
	Offset  float64 // seconds: the object passed this much later than the orbit predicts
	Cross   float64 // degrees: how far beside the predicted path the track lies
	Angle   float64 // degrees between the track and the predicted path
	RangeKm float64
	Alt     float64 // degrees above the horizon
	Others  int     // other objects that fit nearly as well
	score   float64
}

// samples of a path: time (seconds from the start of the exposure) and pixel
type sample struct{ t, x, y float64 }

// Identify tells which objects of orbits left the tracks of a frame.
func Identify(f Frame, streaks []Streak, orbits []*Orbit) []Match {
	if len(streaks) == 0 || len(orbits) == 0 {
		return nil
	}
	w := f.WCS
	scale := w.Scale()
	cra, cdec := w.PixelToWorld(float64(f.Width)/2-0.5, float64(f.Height)/2-0.5)
	cx, cy, cz := fits.Direction(cra, cdec)
	radius := math.Hypot(float64(f.Width), float64(f.Height)) / 2 * scale // degrees
	margin := 1.5
	if w.Approx {
		margin = 3
	}
	ob := NewObserver(f.Site)
	prec := precession(f.Start)
	exp := f.Exposure.Seconds()
	// the gnomonic projection about the frame centre: great circles are
	// straight there, so a short piece of a path is a segment
	ra0, de0 := cra*math.Pi/180, cdec*math.Pi/180
	ex := [3]float64{-math.Sin(ra0), math.Cos(ra0), 0}
	ny := [3]float64{-math.Sin(de0) * math.Cos(ra0), -math.Sin(de0) * math.Sin(ra0), math.Cos(de0)}
	project := func(v Vec) (float64, float64, bool) {
		c := v.X*cx + v.Y*cy + v.Z*cz
		if c < 0.3 {
			return 0, 0, false
		}
		return (v.X*ex[0] + v.Y*ex[1] + v.Z*ex[2]) / c * 180 / math.Pi,
			(v.X*ny[0] + v.Y*ny[1] + v.Z*ny[2]) / c * 180 / math.Pi, true
	}
	const coarse = 8.0 // seconds
	maxSlack := 120.0
	var best []Match
	all := make([][]Match, len(streaks))
	for _, o := range orbits {
		age := math.Abs(f.Start.Sub(o.Epoch).Hours() / 24)
		slack := math.Min(maxSlack, 30+10*age)
		t0, t1 := -slack, exp+slack
		// coarse: does the path come near the frame at all?
		var near []float64
		var px, py float64
		have := false
		for t := t0; t <= t1+coarse; t += coarse {
			v, err := ob.Look(o, f.Start.Add(time.Duration(t*1e9)), &prec)
			if err != nil {
				have = false
				break
			}
			x, y, ok := project(v)
			if ok && have && segDist(px, py, x, y) < radius+margin {
				near = append(near, t-coarse)
			}
			px, py, have = x, y, ok
		}
		if len(near) == 0 {
			continue
		}
		// fine: the path in pixels around where it was near
		var path []sample
		lo, hi := near[0]-coarse, near[len(near)-1]+2*coarse
		step := 0.5
		for t := lo; t <= hi; t += step {
			v, err := ob.Look(o, f.Start.Add(time.Duration(t*1e9)), &prec)
			if err != nil {
				break
			}
			x, y, ok := w.WorldToPixel(v.X, v.Y, v.Z)
			if !ok {
				continue
			}
			if n := len(path); n > 0 {
				// a step of at most about 20 pixels
				if d := math.Hypot(x-path[n-1].x, y-path[n-1].y); d > 20 && step > 0.02 {
					t -= step
					step = math.Max(0.02, step*20/d)
					continue
				}
			}
			path = append(path, sample{t, x, y})
		}
		if len(path) < 2 {
			continue
		}
		for i, s := range streaks {
			if m, ok := fitPath(f, s, path, scale, slack, age); ok {
				m.Streak = i
				m.Orbit = o
				all[i] = append(all[i], m)
			}
		}
	}
	for i, ms := range all {
		if len(ms) == 0 {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].score < ms[b].score })
		m := ms[0]
		for _, other := range ms[1:] {
			if other.score < m.score*2+0.2 {
				m.Others++
			}
		}
		at := f.Start.Add(time.Duration((exp/2 + m.Offset) * 1e9))
		if v, err := ob.Look(m.Orbit, at, &prec); err == nil {
			m.RangeKm = v.Km
		}
		m.Alt, _ = ob.Altitude(m.Orbit, at)
		m.Streak = i
		best = append(best, m)
	}
	return best
}

// segDist: the distance from the origin to the segment from a to b.
func segDist(ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
	}
	return math.Hypot(ax+t*dx, ay+t*dy)
}

// fitPath compares a track with the path of an object.
func fitPath(f Frame, s Streak, path []sample, scale, slack, age float64) (Match, bool) {
	var m Match
	exp := f.Exposure.Seconds()
	mx, my := (s.X0+s.X1)/2, (s.Y0+s.Y1)/2
	// the point of the path nearest the middle of the track
	bestD := math.Inf(1)
	var bt, bdx, bdy, speed float64
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		dx, dy := b.x-a.x, b.y-a.y
		l2 := dx*dx + dy*dy
		if l2 == 0 {
			continue
		}
		u := math.Max(0, math.Min(1, ((mx-a.x)*dx+(my-a.y)*dy)/l2))
		d := math.Hypot(a.x+u*dx-mx, a.y+u*dy-my)
		if d < bestD {
			bestD, bt = d, a.t+u*(b.t-a.t)
			bdx, bdy = dx, dy
			speed = math.Sqrt(l2) / (b.t - a.t) // pixels per second
		}
	}
	if math.IsInf(bestD, 1) || speed <= 0 {
		return m, false
	}
	// across the path, in degrees; the tolerance grows with the age of
	// the orbit, and much for a frame without a plate solution
	cross := bestD * scale
	crossTol := 0.05 + 0.03*age
	angTol := 2.5
	if f.WCS.Approx {
		crossTol += 1.2
		angTol = 3.5
	}
	sl := math.Hypot(s.X1-s.X0, s.Y1-s.Y0)
	cosA := math.Abs(((s.X1-s.X0)*bdx + (s.Y1-s.Y0)*bdy) / (sl * math.Hypot(bdx, bdy)))
	angle := math.Acos(math.Min(1, cosA)) * 180 / math.Pi
	if cross > crossTol || angle > angTol {
		return m, false
	}
	// along the path: when the ends of the track were passed
	half := sl / 2 / speed
	ta, tb := bt-half, bt+half
	predicted := speed * exp
	var off float64
	switch {
	case !s.Edge0 && !s.Edge1:
		// the whole exposure on the frame: the middle of the track is
		// the middle of the exposure, and the track is no longer than the
		// path then (it may be shorter: a flash, the shadow of the Earth)
		// longer by up to half: the end of a faint track is followed on to
		// a star lying on its line
		if sl > 1.5*predicted+20 || sl < 0.25*predicted {
			return m, false
		}
		off = bt - exp/2
	case s.Edge0 != s.Edge1:
		// one end inside: there the exposure started or ended
		if sl > 1.3*predicted+20 {
			return m, false
		}
		if s.Edge0 == (bdx*(s.X1-s.X0)+bdy*(s.Y1-s.Y0) > 0) {
			off = tb - exp // the inner end is the later one: the exposure ended there
		} else {
			off = ta // the earlier one: it started there
		}
	default:
		// across the whole frame: passed during the exposure
		if sl > 1.3*predicted+20 {
			return m, false
		}
		off = math.Max(0, math.Max(-ta, tb-exp))
	}
	if math.Abs(off) > slack {
		return m, false
	}
	m.Offset, m.Cross, m.Angle = off, cross, angle
	m.score = (cross/crossTol)*(cross/crossTol) + (angle/angTol)*(angle/angTol) + (off/slack)*(off/slack)
	return m, true
}

func atoi(s string) int { return int(atof(s)) }

func atof(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
