package satellites

import (
	"math"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// geodetic: the latitude, longitude and height (m) of an ECEF point, km.
func geodetic(p [3]float64) (lat, lon, h float64) {
	const a, f = 6378.137, 1 / 298.257223563
	e2 := f * (2 - f)
	lon = math.Atan2(p[1], p[0])
	r := math.Hypot(p[0], p[1])
	lat = math.Atan2(p[2], r*(1-e2))
	for i := 0; i < 10; i++ {
		n := a / math.Sqrt(1-e2*math.Sin(lat)*math.Sin(lat))
		h = r/math.Cos(lat) - n
		lat = math.Atan2(p[2], r*(1-e2*n/(n+h)))
	}
	return lat * 180 / math.Pi, lon * 180 / math.Pi, h * 1000
}

// planeAt: a point 10 km high on the line of sight to ra, dec at t.
func planeAt(t *testing.T, site Site, at time.Time, ra, dec float64) PlanePoint {
	x, y, z := fits.Direction(ra, dec)
	m := precession(at) // date to J2000: its transpose back
	d := [3]float64{m[0][0]*x + m[1][0]*y + m[2][0]*z, m[0][1]*x + m[1][1]*y + m[2][1]*z, m[0][2]*x + m[1][2]*y + m[2][2]*z}
	g := gstime(julian(at))
	cg, sg := math.Cos(g), math.Sin(g)
	e := [3]float64{d[0]*cg + d[1]*sg, -d[0]*sg + d[1]*cg, d[2]}
	o := site.ecef()
	lo, hi := 0.0, 200.0
	for i := 0; i < 60; i++ {
		r := (lo + hi) / 2
		if _, _, h := geodetic([3]float64{o[0] + r*e[0], o[1] + r*e[1], o[2] + r*e[2]}); h < 10000 {
			lo = r
		} else {
			hi = r
		}
	}
	lat, lon, h := geodetic([3]float64{o[0] + lo*e[0], o[1] + lo*e[1], o[2] + lo*e[2]})
	if math.Abs(h-10000) > 1 {
		t.Fatalf("height %v", h)
	}
	return PlanePoint{T: at, Lat: lat, Lon: lon, AltM: h, Speed: 230, Track: 70}
}

func TestIdentifyPlanes(t *testing.T) {
	f, err := FrameFromHeader(header(t, m31), "x.fit", nil)
	if err != nil {
		t.Fatal(err)
	}
	mid := f.Start.Add(12 * time.Second)
	cra, cdec := f.WCS.PixelToWorld(float64(f.Width)/2, float64(f.Height)/2)
	p0 := planeAt(t, f.Site, mid, cra, cdec)
	// recorded every 25 seconds around the frame
	plane := &Plane{ICAO: "4ca7b3", Callsign: "AFL1234"}
	for dt := -50.0; dt <= 50; dt += 25 {
		plane.Points = append(plane.Points, carry(p0, mid.Add(time.Duration(dt*1e9))))
	}
	other := &Plane{ICAO: "aaaaaa"}
	for _, q := range plane.Points {
		q.Lat += 0.05 // 5 km north
		other.Points = append(other.Points, q)
	}
	// the track it leaves: its path a fraction of a second around mid
	ob := NewObserver(f.Site)
	prec := precession(mid)
	end := func(dt float64) (float64, float64) {
		at := mid.Add(time.Duration(dt * 1e9))
		pt, _ := plane.at(at)
		v, _ := ob.lookPoint(pt, at, &prec)
		x, y, ok := f.WCS.WorldToPixel(v.X, v.Y, v.Z)
		if !ok {
			t.Fatal("off the frame")
		}
		return x, y
	}
	x0, y0 := end(-0.1)
	x1, y1 := end(0.1)
	track := Streak{X0: x0, Y0: y0, X1: x1, Y1: y1, Edge0: true, Edge1: true}
	ms := IdentifyPlanes(f, []Streak{track}, []*Plane{other, plane})
	if len(ms) != 1 || ms[0].Plane != plane || ms[0].Cross > 0.01 || math.Abs(ms[0].AltM-10000) > 50 || ms[0].Alt < 5 {
		t.Fatalf("%+v", ms)
	}
	// turned across its path: nothing
	mx, my, dx, dy := (x0+x1)/2, (y0+y1)/2, (x1-x0)/2, (y1-y0)/2
	turned := Streak{X0: mx - dy, Y0: my + dx, X1: mx + dy, Y1: my - dx}
	if ms := IdentifyPlanes(f, []Streak{turned}, []*Plane{plane}); len(ms) != 0 {
		t.Fatalf("a track across the path told: %+v", ms[0])
	}
	// no recording near the frame: nothing
	late := &Plane{ICAO: plane.ICAO}
	for _, q := range plane.Points {
		q.T = q.T.Add(10 * time.Minute)
		late.Points = append(late.Points, q)
	}
	if ms := IdentifyPlanes(f, []Streak{track}, []*Plane{late}); len(ms) != 0 {
		t.Fatalf("told 10 minutes off: %+v", ms[0])
	}
}
