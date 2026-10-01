package planner

import (
	"math"
	"sort"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

// Obs is a light frame taken: when, of what, where the telescope pointed.
type Obs struct {
	Object   string
	Night    string    // the evening the night began, YYYY-MM-DD
	UTC      time.Time // start of the exposure
	Exposure float64   // seconds
	RA, Dec  float64   // J2000, degrees
	Lat, Lon float64   // the observatory
	Point              // computed by Infer
}

// Guess is what the frames tell about the sky.
type Guess struct {
	Points  []Obs   // the frames with where they were in the sky
	Horizon Horizon // guessed horizon, nil without frames
	// Zones: gaps in a series while nothing at all was shot (the object
	// was hidden: wires, a pole… or clouds); Maybe: gaps while another
	// object was shot — perhaps hidden, perhaps a choice
	Zones, Maybe []Zone
}

const binDeg = 5

// Infer guesses the horizon and the closed zones from the frames taken:
//
//   - a frame shows the sky was open where it was: the horizon is lower;
//   - the first frame of a rising object (the night had begun before) and
//     the last of a setting one (the night went on) show about where the
//     horizon is;
//   - a gap in the series of one object, longer than three frames and ten
//     minutes and not a meridian flip, shows a closed part of the sky
//     along the path of the object during the gap.
//
// minAlt is the horizon where the frames say nothing.
func Infer(obs []Obs, minAlt float64) Guess {
	var g Guess
	for _, o := range obs {
		if o.UTC.IsZero() {
			continue
		}
		o.Point = pos(o.UTC, o)
		g.Points = append(g.Points, o)
	}
	if len(g.Points) == 0 {
		return g
	}
	sort.SliceStable(g.Points, func(i, j int) bool { return g.Points[i].UTC.Before(g.Points[j].UTC) })

	const bins = 360 / binDeg
	upper := make([]float64, bins) // the horizon is below every frame
	sample := make([]float64, bins)
	for i := range upper {
		upper[i], sample[i] = math.Inf(1), math.Inf(1)
	}
	bin := func(az float64) int { return int(normAz(az)/binDeg) % bins }
	for _, o := range g.Points {
		b := bin(o.Az)
		upper[b] = math.Min(upper[b], o.Alt)
	}
	// the ends of the series: per object and night
	series := map[string][]Obs{}
	var keys []string
	for _, o := range g.Points {
		k := o.Night + "\x00" + o.Object
		if _, ok := series[k]; !ok {
			keys = append(keys, k)
		}
		series[k] = append(series[k], o)
	}
	for _, k := range keys {
		s := series[k]
		first, last := s[0], s[len(s)-1]
		before, after := first.UTC.Add(-10*time.Minute), last.UTC.Add(time.Duration(last.Exposure*float64(time.Second))+10*time.Minute)
		if p := pos(before, first); p.Alt < first.Alt && dark(before, first) {
			b := bin(first.Az)
			sample[b] = math.Min(sample[b], first.Alt)
		}
		if p := pos(after, last); p.Alt < last.Alt && dark(after, last) {
			b := bin(last.Az)
			sample[b] = math.Min(sample[b], last.Alt)
		}
		g.gaps(s)
	}
	g.Zones, g.Maybe = merge(g.Zones), merge(g.Maybe)

	// the horizon: the samples, between them interpolated, never above a
	// frame
	h := make([]float64, bins)
	var have []int
	for i, v := range sample {
		if !math.IsInf(v, 1) {
			have = append(have, i)
		}
	}
	for i := range h {
		switch {
		case len(have) == 0:
			h[i] = minAlt
		case !math.IsInf(sample[i], 1):
			h[i] = sample[i] - 1
		default:
			// the nearest samples on both sides, through the north
			a, b := have[len(have)-1]-bins, have[0]+bins
			for _, j := range have {
				if j < i {
					a = j
				} else if j > i {
					b = j
					break
				}
			}
			va, vb := sample[(a+bins)%bins]-1, sample[b%bins]-1
			h[i] = va + (vb-va)*float64(i-a)/float64(b-a)
		}
		if !math.IsInf(upper[i], 1) {
			h[i] = math.Min(h[i], upper[i]-1)
		}
		h[i] = math.Max(h[i], minAlt)
	}
	// the nodes at the edges of the bins, as low as the lower of the two
	// bins: the line between them stays under the frames of either bin
	v := make([]float64, bins)
	for i := range v {
		v[i] = math.Floor(math.Min(h[(i-1+bins)%bins], h[i])*2) / 2
	}
	for i := range v {
		prev, next := v[(i-1+bins)%bins], v[(i+1)%bins]
		if i > 0 && prev == v[i] && next == v[i] {
			continue // inside a flat run
		}
		g.Horizon = append(g.Horizon, Point{float64(i * binDeg), v[i]})
	}
	return g
}

// gaps finds the gaps in the series s of one object in one night.
func (g *Guess) gaps(s []Obs) {
	for i := 1; i < len(s); i++ {
		a, b := s[i-1], s[i]
		end := a.UTC.Add(time.Duration(a.Exposure * float64(time.Second)))
		gap := b.UTC.Sub(end)
		if gap < 10*time.Minute || gap.Seconds() < 3*(a.Exposure+10) {
			continue
		}
		// a meridian flip: the hour angle went through 0
		ha1, ha2 := moon.HourAngle(end, a.Lon, a.RA), moon.HourAngle(b.UTC, a.Lon, a.RA)
		if math.Abs(ha1) < 0.5 && ha2 > ha1 && ha2 < 1 && gap < 40*time.Minute {
			continue
		}
		// the path of the object while it was not shot
		z := Zone{Az1: math.Inf(1), Az2: math.Inf(-1), Alt1: math.Inf(1), Alt2: math.Inf(-1), Auto: true}
		ref := pos(end, a).Az
		for t := end; !t.After(b.UTC); t = t.Add(time.Minute) {
			p := pos(t, a)
			az := ref + wrap180(p.Az-ref)
			z.Az1, z.Az2 = math.Min(z.Az1, az), math.Max(z.Az2, az)
			z.Alt1, z.Alt2 = math.Min(z.Alt1, p.Alt), math.Max(z.Alt2, p.Alt)
		}
		z.Az1, z.Az2 = normAz(z.Az1-0.5), normAz(z.Az2+0.5)
		z.Alt1, z.Alt2 = z.Alt1-0.5, z.Alt2+0.5
		other := false
		for _, o := range g.Points {
			if o.UTC.After(end) && o.UTC.Before(b.UTC) && o.Object != a.Object {
				other = true
				break
			}
		}
		if other {
			g.Maybe = append(g.Maybe, z)
		} else {
			g.Zones = append(g.Zones, z)
		}
	}
}

// merge joins the zones that overlap (the same wires seen on two nights).
func merge(zs []Zone) []Zone {
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(zs) && !changed; i++ {
			for j := i + 1; j < len(zs); j++ {
				if overlap(zs[i], zs[j]) {
					zs[i] = union(zs[i], zs[j])
					zs = append(zs[:j], zs[j+1:]...)
					changed = true
					break
				}
			}
		}
	}
	return zs
}

func overlap(a, b Zone) bool {
	if math.Max(a.Alt1, b.Alt1) > math.Min(a.Alt2, b.Alt2) {
		return false
	}
	return a.Contains(b.Az1, a.Alt1) || a.Contains(b.Az2, a.Alt1) || b.Contains(a.Az1, b.Alt1)
}

func union(a, b Zone) Zone {
	// the azimuths: from a's start, the furthest end of both
	w1, w2 := normAz(a.Az2-a.Az1), normAz(b.Az2-a.Az1)
	s := normAz(b.Az1 - a.Az1)
	start := a.Az1
	if s > 180 { // b starts before a
		start = b.Az1
		w1, w2 = normAz(a.Az2-start), normAz(b.Az2-start)
	}
	return Zone{Az1: start, Az2: normAz(start + math.Max(w1, w2)), Alt1: math.Min(a.Alt1, b.Alt1),
		Alt2: math.Max(a.Alt2, b.Alt2), Auto: a.Auto && b.Auto}
}

func pos(t time.Time, o Obs) Point {
	p := moon.Object(t, o.Lat, o.Lon, o.RA, o.Dec)
	return Point{p.Az, p.Alt}
}

// dark: the Sun was below -12° (the session was not begun or ended by the
// twilight).
func dark(t time.Time, o Obs) bool { return moon.Sun(t, o.Lat, o.Lon).Alt < -12 }

func wrap180(a float64) float64 {
	a = normAz(a)
	if a > 180 {
		a -= 360
	}
	return a
}
