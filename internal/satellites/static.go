package satellites

import "math"

// SkyLine is a track placed in the sky: its ends, degrees (J2000).
type SkyLine struct {
	RA0, Dec0, RA1, Dec1 float64
}

// SameSky: two tracks lie on one another in the sky (along one line and
// overlapping by half of the shorter). A moving object on frames taken one
// after another lies on one line too, but end to end. A track found in the
// same place of the sky on another frame is a filament of a nebula or an
// edge-on galaxy, not a satellite.
func SameSky(a, b SkyLine) bool {
	return sameSky(a, b) || sameSky(b, a)
}

func sameSky(a, b SkyLine) bool {
	const tol = 40.0 / 3600 // degrees
	// a plane tangent at the middle of a, in degrees
	mra, mdec := (a.RA0+a.RA1)/2, (a.Dec0+a.Dec1)/2
	if math.Abs(a.RA0-a.RA1) > 180 {
		mra += 180
	}
	c := math.Cos(mdec * math.Pi / 180)
	p := func(ra, dec float64) (float64, float64) {
		d := math.Mod(ra-mra+540, 360) - 180
		return d * c, dec - mdec
	}
	ax0, ay0 := p(a.RA0, a.Dec0)
	ax1, ay1 := p(a.RA1, a.Dec1)
	bx0, by0 := p(b.RA0, b.Dec0)
	bx1, by1 := p(b.RA1, b.Dec1)
	la := math.Hypot(ax1-ax0, ay1-ay0)
	lb := math.Hypot(bx1-bx0, by1-by0)
	if la == 0 || lb == 0 {
		return false
	}
	ux, uy := (ax1-ax0)/la, (ay1-ay0)/la
	across := func(x, y float64) float64 { return math.Abs(-(x-ax0)*uy + (y-ay0)*ux) }
	if across(bx0, by0) > tol || across(bx1, by1) > tol {
		return false
	}
	along := func(x, y float64) float64 { return (x-ax0)*ux + (y-ay0)*uy }
	lo, hi := math.Min(along(bx0, by0), along(bx1, by1)), math.Max(along(bx0, by0), along(bx1, by1))
	overlap := math.Min(hi, la) - math.Max(lo, 0)
	return overlap > 0.5*math.Min(la, lb)
}
