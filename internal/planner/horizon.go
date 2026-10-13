// Package planner plans a night of imaging: what sky the observatory sees
// (a horizon line and closed zones such as wires, guessed from the frames
// already taken and corrected by hand), and in what order and when to
// shoot several objects by priority, with the time lost on slews, focusing,
// dithering and meridian flips. The package is portable; the window lives
// in cmd/asiair-sorter.
package planner

import (
	"math"
	"sort"
)

// Point is a point of the sky in degrees: azimuth from the north through
// the east, altitude above the mathematical horizon.
type Point struct {
	Az  float64 `json:"az"`
	Alt float64 `json:"alt"`
	// Manual: a point of the horizon put or moved by hand; the horizon
	// guessed again keeps it
	Manual bool `json:"manual,omitempty"`
}

// Horizon is the line under which nothing is seen (trees, houses), a
// broken line over the azimuth, closed through the north.
type Horizon []Point

// Sorted is the horizon ordered by azimuth, azimuths in 0..360.
func (h Horizon) Sorted() Horizon {
	out := make(Horizon, len(h))
	for i, p := range h {
		out[i] = p
		out[i].Az = normAz(p.Az)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Az < out[j].Az })
	return out
}

// At is the height of the horizon at the azimuth az; 0 without points.
func (h Horizon) At(az float64) float64 {
	if len(h) == 0 {
		return 0
	}
	if len(h) == 1 {
		return h[0].Alt
	}
	s := h.Sorted()
	az = normAz(az)
	i := sort.Search(len(s), func(i int) bool { return s[i].Az > az })
	a, b := s[(i-1+len(s))%len(s)], s[i%len(s)] // through the north at the ends
	span := normAz(b.Az - a.Az)
	if span == 0 {
		return math.Max(a.Alt, b.Alt)
	}
	f := normAz(az-a.Az) / span
	return a.Alt + (b.Alt-a.Alt)*f
}

// Zone is a closed part of the sky above the horizon (wires, a pole, a
// roof): azimuths from Az1 eastwards to Az2 (through the north when
// Az1 > Az2), altitudes from Alt1 to Alt2; or, drawn by hand, a polygon
// (Poly, three vertices or more, edges straight on the strip of azimuth and
// altitude), whose bounding box is then kept in Az1…Alt2 for the programs
// that know only boxes.
type Zone struct {
	Az1  float64 `json:"az1"`
	Az2  float64 `json:"az2"`
	Alt1 float64 `json:"alt1"`
	Alt2 float64 `json:"alt2"`
	// Poly: the vertices of a polygon zone
	Poly []Point `json:"poly,omitempty"`
	// Auto: guessed from the frames (a gap in a series); a zone drawn,
	// changed or accepted by hand is not, and the sky guessed again keeps it
	Auto bool `json:"auto,omitempty"`
}

// Polygon tells a zone drawn as a polygon.
func (z Zone) Polygon() bool { return len(z.Poly) >= 3 }

// Contains tells a point inside the zone.
func (z Zone) Contains(az, alt float64) bool {
	if z.Polygon() {
		u := unwrap(z.Poly)
		az = normAz(az)
		for _, k := range [...]float64{-360, 0, 360} {
			if inPolygon(u, az+k, alt) {
				return true
			}
		}
		return false
	}
	if alt < math.Min(z.Alt1, z.Alt2) || alt > math.Max(z.Alt1, z.Alt2) {
		return false
	}
	return normAz(az-z.Az1) <= normAz(z.Az2-z.Az1)
}

// unwrap: the vertices with the azimuths running on from the first, no
// jump over 180° between neighbours (a polygon across the north).
func unwrap(p []Point) []Point {
	out := make([]Point, len(p))
	for i, q := range p {
		out[i] = q
		if i > 0 {
			out[i].Az = out[i-1].Az + wrap180(q.Az-out[i-1].Az)
		}
	}
	return out
}

// inPolygon: the even-odd rule.
func inPolygon(p []Point, az, alt float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		a, b := p[i], p[j]
		if (a.Alt > alt) != (b.Alt > alt) && az < a.Az+(alt-a.Alt)*(b.Az-a.Az)/(b.Alt-a.Alt) {
			in = !in
		}
	}
	return in
}

// Fit puts the bounding box of a polygon zone in Az1…Alt2 (azimuths in
// 0..360, through the north when the polygon crosses it).
func (z *Zone) Fit() {
	if !z.Polygon() {
		return
	}
	u := unwrap(z.Poly)
	lo, hi := u[0].Az, u[0].Az
	z.Alt1, z.Alt2 = u[0].Alt, u[0].Alt
	for _, q := range u {
		lo, hi = math.Min(lo, q.Az), math.Max(hi, q.Az)
		z.Alt1, z.Alt2 = math.Min(z.Alt1, q.Alt), math.Max(z.Alt2, q.Alt)
	}
	if hi-lo >= 360 {
		z.Az1, z.Az2 = 0, 360
		return
	}
	z.Az1, z.Az2 = normAz(lo), normAz(hi)
}

// Outline: the edges of a polygon zone as segments on the strip, azimuths
// in 0..360 (an edge across the north is cut in two).
func (z Zone) Outline() [][2]Point {
	if !z.Polygon() {
		return nil
	}
	return Segments(z.Poly, true)
}

// Segments: the edges of a broken line (closed: back to the first point)
// as segments on the strip, azimuths in 0..360; each edge goes the short
// way, across the north when that is shorter.
func Segments(pts []Point, closed bool) [][2]Point {
	u := unwrap(pts)
	n := len(u) - 1
	if closed {
		n = len(u)
	}
	var out [][2]Point
	for i := 0; i < n; i++ {
		a, b := u[i], u[(i+1)%len(u)]
		b.Az = a.Az + wrap180(b.Az-a.Az)
		for _, k := range [...]float64{-360, 0, 360} {
			p, q := Point{Az: a.Az + k, Alt: a.Alt}, Point{Az: b.Az + k, Alt: b.Alt}
			if s, ok := clipAz(p, q); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// clipAz cuts a segment to the azimuths 0..360.
func clipAz(p, q Point) ([2]Point, bool) {
	t0, t1 := 0.0, 1.0
	d := q.Az - p.Az
	for _, lim := range [...]struct{ v, sign float64 }{{0, 1}, {360, -1}} {
		// keep sign*(az - v) >= 0
		f := lim.sign * (p.Az - lim.v)
		g := lim.sign * d
		switch {
		case g == 0 && f < 0:
			return [2]Point{}, false
		case g > 0:
			t0 = math.Max(t0, -f/g)
		case g < 0:
			t1 = math.Min(t1, -f/g)
		}
	}
	if t0 >= t1 {
		return [2]Point{}, false
	}
	at := func(t float64) Point { return Point{Az: p.Az + d*t, Alt: p.Alt + (q.Alt-p.Alt)*t} }
	return [2]Point{at(t0), at(t1)}, true
}

// Sky is what the observatory sees.
type Sky struct {
	Horizon Horizon `json:"horizon,omitempty"`
	Zones   []Zone  `json:"zones,omitempty"`
	MinAlt  float64 `json:"min_alt"` // never lower than this, whatever the horizon
}

// Visible tells whether a point is open to the telescope.
func (s Sky) Visible(az, alt float64) bool {
	if alt < s.MinAlt || alt < s.Horizon.At(az) {
		return false
	}
	for _, z := range s.Zones {
		if z.Contains(az, alt) {
			return false
		}
	}
	return true
}

// Manual tells there is something put by hand in the sky.
func (s Sky) Manual() bool {
	for _, p := range s.Horizon {
		if p.Manual {
			return true
		}
	}
	for _, z := range s.Zones {
		if !z.Auto {
			return true
		}
	}
	return false
}

// Reguess is the sky guessed from the frames again keeping what was put by
// hand: the points of the horizon added or moved (the guessed points near
// them give way) and the zones drawn, changed or accepted (the guessed
// zones over them give way). edited tells the sky was edited; its points
// saved before they were marked count as put by hand when the guess does
// not have them.
func (s Sky) Reguess(g Guess, edited bool) Sky {
	marked := false
	for _, p := range s.Horizon {
		marked = marked || p.Manual
	}
	var hand Horizon
	for _, p := range s.Horizon {
		if p.Manual || edited && !marked && !g.Horizon.has(p) {
			p.Manual = true
			hand = append(hand, p)
		}
	}
	out := Sky{MinAlt: s.MinAlt, Horizon: append(Horizon(nil), hand...)}
	for _, p := range g.Horizon {
		near := false
		for _, q := range hand {
			near = near || math.Abs(wrap180(p.Az-q.Az)) < binDeg
		}
		if !near {
			out.Horizon = append(out.Horizon, p)
		}
	}
	out.Horizon = out.Horizon.Sorted()
	for _, z := range s.Zones {
		if !z.Auto {
			out.Zones = append(out.Zones, z)
		}
	}
	kept := len(out.Zones)
	for _, z := range g.Zones {
		over := false
		for _, m := range out.Zones[:kept] {
			over = over || overlap(z, m) || overlap(m, z)
		}
		if !over {
			out.Zones = append(out.Zones, z)
		}
	}
	return out
}

// has tells the horizon has a point at about p.
func (h Horizon) has(p Point) bool {
	for _, q := range h {
		if math.Abs(wrap180(p.Az-q.Az)) <= 0.5 && math.Abs(p.Alt-q.Alt) <= 0.5 {
			return true
		}
	}
	return false
}

func normAz(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}
