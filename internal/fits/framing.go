package fits

import (
	"math"
	"strings"
)

// Framing is the sky of a frame as it is shown (rows from the top): its
// centre, the angle of its top edge from north through east (as the atlas
// of the planner turns its frames), its size, and a linear TAN WCS for a
// picture of it (Preview binned by Binning) to lay it over the sky.
type Framing struct {
	RA, Dec       float64        // the centre, degrees
	Angle         float64        // degrees, 0–360
	Width, Height float64        // degrees
	Approx        bool           // from the pointing, not a plate solution
	WCS           map[string]any // FITS keywords of the picture
}

// FramingOf works out the framing of a frame from its header (the plate
// solution, else the pointing with ROTATOR or the angle in fileName) for a
// picture pw × ph pixels, each bin × bin pixels of the frame. Aladin counts
// the rows of a picture from the bottom, as FITS does.
func FramingOf(h *Header, fileName string, pw, ph, bin int) (*Framing, bool) {
	w, ok := ReadWCS(h, fileName)
	if !ok || pw <= 0 || ph <= 0 || bin <= 0 {
		return nil, false
	}
	bottomUp := strings.EqualFold(h.Get("ROWORDER"), "BOTTOM-UP")
	rows := ph * bin // the rows the picture covers (the rest is cut off)
	// sky of a point of the picture (pixel centres at x+0.5)
	sky := func(px, py float64) (float64, float64) {
		sx, sy := px*float64(bin)-0.5, py*float64(bin)-0.5
		if bottomUp {
			sy = float64(rows) - 1 - sy
		}
		return w.PixelToWorld(sx, sy)
	}
	cx, cy := float64(pw)/2, float64(ph)/2
	ra0, dec0 := sky(cx, cy)
	plane := func(px, py float64) (float64, float64) {
		ra, dec := sky(px, py)
		return tangent(ra0, dec0, ra, dec)
	}
	f := &Framing{RA: ra0, Dec: dec0, Approx: w.Approx}
	// the top: the direction to the middle of the upper edge
	ux, uy := plane(cx, cy-float64(ph)/4)
	f.Angle = math.Mod(math.Atan2(ux, uy)*180/math.Pi+360, 360)
	lx, ly := plane(0, cy)
	rx, ry := plane(float64(pw), cy)
	f.Width = math.Hypot(rx-lx, ry-ly)
	tx, ty := plane(cx, 0)
	bx, by := plane(cx, float64(ph))
	f.Height = math.Hypot(bx-tx, by-ty)
	// the linear WCS of the picture around its centre: the change of the
	// tangent plane per pixel to the right and per pixel up
	k := math.Max(1, math.Min(cx, cy)/2)
	x1, y1 := plane(cx+k, cy)
	x0, y0 := plane(cx-k, cy)
	u1, v1 := plane(cx, cy-k) // up: rows towards the top
	u0, v0 := plane(cx, cy+k)
	f.WCS = map[string]any{
		"NAXIS": 2, "NAXIS1": pw, "NAXIS2": ph, "CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CUNIT1": "deg", "CUNIT2": "deg",
		"CRVAL1": ra0, "CRVAL2": dec0, "CRPIX1": cx + 0.5, "CRPIX2": cy + 0.5,
		"CD1_1": (x1 - x0) / (2 * k), "CD2_1": (y1 - y0) / (2 * k),
		"CD1_2": (u1 - u0) / (2 * k), "CD2_2": (v1 - v0) / (2 * k),
	}
	return f, true
}

// tangent: the offset of a sky point from the centre on the tangent plane,
// degrees (east, north).
func tangent(ra0, dec0, ra, dec float64) (float64, float64) {
	r := math.Pi / 180
	d0, d, da := dec0*r, dec*r, (ra-ra0)*r
	c := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(da)
	x := math.Cos(d) * math.Sin(da) / c
	y := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(da)) / c
	return x / r, y / r
}
