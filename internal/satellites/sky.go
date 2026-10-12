package satellites

import (
	"math"
	"time"
)

// Site is the observer: geodetic latitude and longitude (east positive)
// in degrees, height in metres.
type Site struct {
	Lat, Lon, Height float64
}

// ecef: the observer on the WGS-84 ellipsoid, km.
func (s Site) ecef() [3]float64 {
	const a, f = 6378.137, 1 / 298.257223563
	lat, lon := s.Lat*math.Pi/180, s.Lon*math.Pi/180
	e2 := f * (2 - f)
	n := a / math.Sqrt(1-e2*math.Sin(lat)*math.Sin(lat))
	h := s.Height / 1000
	return [3]float64{
		(n + h) * math.Cos(lat) * math.Cos(lon),
		(n + h) * math.Cos(lat) * math.Sin(lon),
		(n*(1-e2) + h) * math.Sin(lat),
	}
}

// Vec is a unit direction in the sky (equatorial, J2000) with the distance
// to the object in km.
type Vec struct {
	X, Y, Z float64
	Km      float64
}

// RADec of the direction, degrees.
func (v Vec) RADec() (ra, dec float64) {
	ra = math.Atan2(v.Y, v.X) * 180 / math.Pi
	if ra < 0 {
		ra += 360
	}
	return ra, math.Asin(math.Max(-1, math.Min(1, v.Z))) * 180 / math.Pi
}

// precession: the matrix from the mean equator of date to J2000 (IAU 1976);
// nutation (under 20″) and the equation of the equinoxes are left out.
func precession(t time.Time) [3][3]float64 {
	T := (julian(t) - 2451545.0) / 36525
	as := math.Pi / 180 / 3600
	zeta := (2306.2181*T + 0.30188*T*T + 0.017998*T*T*T) * as
	z := (2306.2181*T + 1.09468*T*T + 0.018203*T*T*T) * as
	th := (2004.3109*T - 0.42665*T*T - 0.041833*T*T*T) * as
	cz, sz := math.Cos(z), math.Sin(z)
	ct, st := math.Cos(th), math.Sin(th)
	cx, sx := math.Cos(zeta), math.Sin(zeta)
	// P maps J2000 to date; its transpose maps back
	p := [3][3]float64{
		{cz*ct*cx - sz*sx, -cz*ct*sx - sz*cx, -cz * st},
		{sz*ct*cx + cz*sx, -sz*ct*sx + cz*cx, -sz * st},
		{st * cx, -st * sx, ct},
	}
	var pt [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			pt[i][j] = p[j][i]
		}
	}
	return pt
}

// Observer turns the positions of objects into directions seen from a site.
type Observer struct {
	site Site
	pos  [3]float64
}

func NewObserver(s Site) *Observer { return &Observer{site: s, pos: s.ecef()} }

// Look returns where the object is seen from the site at t (J2000). The
// precession matrix of t may be passed (nil: computed).
func (ob *Observer) Look(o *Orbit, t time.Time, prec *[3][3]float64) (Vec, error) {
	r, err := o.TEME(t)
	if err != nil {
		return Vec{}, err
	}
	g := gstime(julian(t))
	cg, sg := math.Cos(g), math.Sin(g)
	p := ob.pos
	obs := [3]float64{p[0]*cg - p[1]*sg, p[0]*sg + p[1]*cg, p[2]}
	d := [3]float64{r[0] - obs[0], r[1] - obs[1], r[2] - obs[2]}
	km := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
	if prec == nil {
		m := precession(t)
		prec = &m
	}
	m := prec
	v := Vec{
		X:  (m[0][0]*d[0] + m[0][1]*d[1] + m[0][2]*d[2]) / km,
		Y:  (m[1][0]*d[0] + m[1][1]*d[1] + m[1][2]*d[2]) / km,
		Z:  (m[2][0]*d[0] + m[2][1]*d[1] + m[2][2]*d[2]) / km,
		Km: km,
	}
	return v, nil
}

// Altitude of the object above the horizon at t, degrees.
func (ob *Observer) Altitude(o *Orbit, t time.Time) (float64, error) {
	r, err := o.TEME(t)
	if err != nil {
		return 0, err
	}
	g := gstime(julian(t))
	cg, sg := math.Cos(g), math.Sin(g)
	// the object in the Earth fixed frame
	e := [3]float64{r[0]*cg + r[1]*sg, -r[0]*sg + r[1]*cg, r[2]}
	d := [3]float64{e[0] - ob.pos[0], e[1] - ob.pos[1], e[2] - ob.pos[2]}
	lat, lon := ob.site.Lat*math.Pi/180, ob.site.Lon*math.Pi/180
	up := [3]float64{math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), math.Sin(lat)}
	km := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
	return math.Asin((d[0]*up[0]+d[1]*up[1]+d[2]*up[2])/km) * 180 / math.Pi, nil
}
