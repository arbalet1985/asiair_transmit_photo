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
// Az1 > Az2), altitudes from Alt1 to Alt2.
type Zone struct {
	Az1  float64 `json:"az1"`
	Az2  float64 `json:"az2"`
	Alt1 float64 `json:"alt1"`
	Alt2 float64 `json:"alt2"`
	// Auto: guessed from the frames (a gap in a series); a zone drawn,
	// changed or accepted by hand is not, and the sky guessed again keeps it
	Auto bool `json:"auto,omitempty"`
}

// Contains tells a point inside the zone.
func (z Zone) Contains(az, alt float64) bool {
	if alt < math.Min(z.Alt1, z.Alt2) || alt > math.Max(z.Alt1, z.Alt2) {
		return false
	}
	return normAz(az-z.Az1) <= normAz(z.Az2-z.Az1)
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
