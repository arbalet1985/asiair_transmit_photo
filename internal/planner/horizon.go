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
}

// Horizon is the line under which nothing is seen (trees, houses), a
// broken line over the azimuth, closed through the north.
type Horizon []Point

// Sorted is the horizon ordered by azimuth, azimuths in 0..360.
func (h Horizon) Sorted() Horizon {
	out := make(Horizon, len(h))
	for i, p := range h {
		out[i] = Point{normAz(p.Az), p.Alt}
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
	// Auto: guessed from the frames (a gap in a series)
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

func normAz(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}
