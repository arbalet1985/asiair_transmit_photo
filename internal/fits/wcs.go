package fits

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// WCS maps the pixels of a frame to the sky: the TAN projection with the
// SIP distortion ASIAIR writes after plate solving, or (Approx) one made
// from where the mount pointed, the focal length and the camera angle.
// Pixels are 0-based: the centre of the first pixel is (0, 0).
type WCS struct {
	CRVAL  [2]float64 // degrees
	CRPIX  [2]float64 // 1-based, as in the header
	CD     [2][2]float64
	inv    [2][2]float64
	a, b   sip // pixel → intermediate
	ap, bp sip // intermediate → pixel
	// Approx: no solution in the header; the centre may be off by a
	// degree (ASIAIR writes the target, not where it really pointed)
	Approx bool
}

// sip: a polynomial Σ c[p][q] u^p v^q.
type sip struct {
	order int
	c     [10][10]float64
}

func (s *sip) at(u, v float64) float64 {
	if s.order == 0 {
		return 0
	}
	var sum float64
	up := 1.0
	for p := 0; p <= s.order; p++ {
		vq := 1.0
		for q := 0; q <= s.order-p; q++ {
			sum += s.c[p][q] * up * vq
			vq *= v
		}
		up *= u
	}
	return sum
}

func readSIP(h *Header, name string) sip {
	var s sip
	n := h.int(name+"_ORDER", 0)
	if n <= 0 || n > 9 {
		return s
	}
	s.order = n
	for p := 0; p <= n; p++ {
		for q := 0; q <= n-p; q++ {
			s.c[p][q] = h.float(fmt.Sprintf("%s_%d_%d", name, p, q), 0)
		}
	}
	return s
}

func has(h *Header, key string) bool { return h.Get(key) != "" }

// ReadWCS returns the WCS of a frame: its plate solution, or one from its
// pointing (fileName may carry the camera angle as ASIAIR names its files,
// "…_246deg_…").
func ReadWCS(h *Header, fileName string) (*WCS, bool) {
	if w, ok := solvedWCS(h); ok {
		return w, true
	}
	return pointingWCS(h, fileName)
}

func solvedWCS(h *Header) (*WCS, bool) {
	if !has(h, "CRVAL1") || !has(h, "CRVAL2") || !has(h, "CRPIX1") || !has(h, "CRPIX2") {
		return nil, false
	}
	w := &WCS{
		CRVAL: [2]float64{h.float("CRVAL1", 0), h.float("CRVAL2", 0)},
		CRPIX: [2]float64{h.float("CRPIX1", 0), h.float("CRPIX2", 0)},
	}
	switch {
	case has(h, "CD1_1"):
		w.CD = [2][2]float64{{h.float("CD1_1", 0), h.float("CD1_2", 0)}, {h.float("CD2_1", 0), h.float("CD2_2", 0)}}
	case has(h, "CDELT1") && has(h, "CDELT2"):
		c1, c2 := h.float("CDELT1", 0), h.float("CDELT2", 0)
		if has(h, "PC1_1") {
			w.CD = [2][2]float64{{c1 * h.float("PC1_1", 1), c1 * h.float("PC1_2", 0)}, {c2 * h.float("PC2_1", 0), c2 * h.float("PC2_2", 1)}}
		} else {
			r := h.float("CROTA2", 0) * math.Pi / 180
			w.CD = [2][2]float64{{c1 * math.Cos(r), -c2 * math.Sin(r)}, {c1 * math.Sin(r), c2 * math.Cos(r)}}
		}
	default:
		return nil, false
	}
	if !w.invert() {
		return nil, false
	}
	w.a, w.b = readSIP(h, "A"), readSIP(h, "B")
	w.ap, w.bp = readSIP(h, "AP"), readSIP(h, "BP")
	return w, true
}

var angleInName = regexp.MustCompile(`_(\d{1,3})deg_`)

// pointingWCS: the centre from RA/DEC (or OBJCTRA/OBJCTDEC), the scale from
// FOCALLEN and XPIXSZ, the angle from ROTATOR or the file name. ASIAIR's
// camera angle θ turns the frame as the solved CD matrix of its frames
// shows: CD = scale·[[cos(θ−180°), −sin], [sin, cos]].
func pointingWCS(h *Header, fileName string) (*WCS, bool) {
	ra, dec, ok := pointing(h)
	if !ok {
		return nil, false
	}
	fl, px := h.float("FOCALLEN", 0), h.float("XPIXSZ", 0)
	if fl <= 0 || px <= 0 {
		return nil, false
	}
	var rot float64
	switch {
	case has(h, "ROTATOR"):
		rot = h.float("ROTATOR", 0)
	default:
		m := angleInName.FindStringSubmatch(fileName)
		if m == nil {
			return nil, false
		}
		rot, _ = strconv.ParseFloat(m[1], 64)
	}
	scale := px / 1000 / fl * 180 / math.Pi // degrees per pixel
	t := (rot - 180) * math.Pi / 180
	nx, ny := h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	w := &WCS{
		CRVAL:  [2]float64{ra, dec},
		CRPIX:  [2]float64{float64(nx)/2 + 0.5, float64(ny)/2 + 0.5},
		CD:     [2][2]float64{{scale * math.Cos(t), -scale * math.Sin(t)}, {scale * math.Sin(t), scale * math.Cos(t)}},
		Approx: true,
	}
	return w, w.invert()
}

// pointing: where the mount pointed, degrees.
func pointing(h *Header) (ra, dec float64, ok bool) {
	if has(h, "RA") && has(h, "DEC") {
		ra, e1 := strconv.ParseFloat(h.Get("RA"), 64)
		dec, e2 := strconv.ParseFloat(h.Get("DEC"), 64)
		if e1 == nil && e2 == nil {
			return ra, dec, true
		}
	}
	r, ok1 := sexagesimal(h.Get("OBJCTRA"))
	d, ok2 := sexagesimal(h.Get("OBJCTDEC"))
	return r * 15, d, ok1 && ok2
}

func sexagesimal(s string) (float64, bool) {
	var a, b, c float64
	if n, _ := fmt.Sscanf(s, "%f %f %f", &a, &b, &c); n < 2 {
		return 0, false
	}
	sign := 1.0
	if a < 0 || len(s) > 0 && s[0] == '-' {
		sign, a = -1, math.Abs(a)
	}
	return sign * (a + b/60 + c/3600), true
}

func (w *WCS) invert() bool {
	d := w.CD[0][0]*w.CD[1][1] - w.CD[0][1]*w.CD[1][0]
	if d == 0 || math.IsNaN(d) {
		return false
	}
	w.inv = [2][2]float64{{w.CD[1][1] / d, -w.CD[0][1] / d}, {-w.CD[1][0] / d, w.CD[0][0] / d}}
	return true
}

// Scale: degrees per pixel.
func (w *WCS) Scale() float64 {
	return math.Sqrt(math.Abs(w.CD[0][0]*w.CD[1][1] - w.CD[0][1]*w.CD[1][0]))
}

// PixelToWorld: RA and Dec in degrees of a pixel.
func (w *WCS) PixelToWorld(x, y float64) (ra, dec float64) {
	u, v := x+1-w.CRPIX[0], y+1-w.CRPIX[1]
	U, V := u+w.a.at(u, v), v+w.b.at(u, v)
	xi := (w.CD[0][0]*U + w.CD[0][1]*V) * math.Pi / 180
	eta := (w.CD[1][0]*U + w.CD[1][1]*V) * math.Pi / 180
	a0, d0 := w.CRVAL[0]*math.Pi/180, w.CRVAL[1]*math.Pi/180
	rho := math.Hypot(xi, eta)
	if rho == 0 {
		return w.CRVAL[0], w.CRVAL[1]
	}
	c := math.Atan(rho)
	sc, cc := math.Sin(c), math.Cos(c)
	dec = math.Asin(cc*math.Sin(d0) + eta*sc*math.Cos(d0)/rho)
	ra = a0 + math.Atan2(xi*sc, rho*math.Cos(d0)*cc-eta*math.Sin(d0)*sc)
	ra = math.Mod(ra*180/math.Pi+360, 360)
	return ra, dec * 180 / math.Pi
}

// WorldToPixel: the pixel of a direction (unit vector, equatorial); false
// when it is 80° or more off the reference point.
func (w *WCS) WorldToPixel(vx, vy, vz float64) (x, y float64, ok bool) {
	a0, d0 := w.CRVAL[0]*math.Pi/180, w.CRVAL[1]*math.Pi/180
	sa, ca := math.Sin(a0), math.Cos(a0)
	sd, cd := math.Sin(d0), math.Cos(d0)
	// the reference direction and the east and north axes there
	cz := vx*cd*ca + vy*cd*sa + vz*sd
	if cz < 0.17 {
		return 0, 0, false
	}
	xi := (-vx*sa + vy*ca) / cz
	eta := (-vx*sd*ca - vy*sd*sa + vz*cd) / cz
	xi, eta = xi*180/math.Pi, eta*180/math.Pi
	U := w.inv[0][0]*xi + w.inv[0][1]*eta
	V := w.inv[1][0]*xi + w.inv[1][1]*eta
	var u, v float64
	if w.ap.order > 0 || w.bp.order > 0 {
		u, v = U+w.ap.at(U, V), V+w.bp.at(U, V)
	} else {
		u, v = U, V
		for i := 0; i < 8 && (w.a.order > 0 || w.b.order > 0); i++ {
			u, v = U-w.a.at(u, v), V-w.b.at(u, v)
		}
	}
	return u + w.CRPIX[0] - 1, v + w.CRPIX[1] - 1, true
}

// Direction: the unit vector of RA and Dec in degrees.
func Direction(ra, dec float64) (x, y, z float64) {
	r, d := ra*math.Pi/180, dec*math.Pi/180
	return math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)
}
