package satellites

import (
	"math"
	"sort"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// PlanePoint is where a plane was (ADS-B, OpenSky): degrees, metres above
// the ellipsoid, m/s, degrees from north.
type PlanePoint struct {
	T                  time.Time
	Lat, Lon, AltM     float64
	Speed, Track, Vert float64
}

// Plane is the points of one plane, by time.
type Plane struct {
	ICAO, Callsign string
	Points         []PlanePoint
}

// PlaneMatch: the plane that left a track.
type PlaneMatch struct {
	Streak  int
	Plane   *Plane
	Cross   float64 // degrees beside its path
	Angle   float64 // degrees between the track and the path
	RangeKm float64
	Alt     float64 // degrees above the horizon
	AltM    float64 // its height, m
	Others  int
	score   float64
}

const (
	planeExtrap = 45.0 // seconds a position is carried on beyond the points
	planeGap    = 150.0
	planeSlack  = 20.0 // seconds of the times of the points
)

// at: where the plane was at t; false when no point is near enough.
func (p *Plane) at(t time.Time) (PlanePoint, bool) {
	pts := p.Points
	i := sort.Search(len(pts), func(i int) bool { return !pts[i].T.Before(t) })
	switch {
	case i < len(pts) && i > 0:
		a, b := pts[i-1], pts[i]
		span := b.T.Sub(a.T).Seconds()
		if span > planeGap {
			if t.Sub(a.T).Seconds() <= planeExtrap {
				return carry(a, t), true
			}
			if b.T.Sub(t).Seconds() <= planeExtrap {
				return carry(b, t), true
			}
			return PlanePoint{}, false
		}
		u := 0.0
		if span > 0 {
			u = t.Sub(a.T).Seconds() / span
		}
		dl := b.Lon - a.Lon
		if dl > 180 {
			dl -= 360
		} else if dl < -180 {
			dl += 360
		}
		return PlanePoint{T: t, Lat: a.Lat + u*(b.Lat-a.Lat), Lon: a.Lon + u*dl, AltM: a.AltM + u*(b.AltM-a.AltM),
			Speed: b.Speed, Track: b.Track}, true
	case i == 0 && len(pts) > 0 && pts[0].T.Sub(t).Seconds() <= planeExtrap:
		return carry(pts[0], t), true
	case i == len(pts) && len(pts) > 0 && t.Sub(pts[len(pts)-1].T).Seconds() <= planeExtrap:
		return carry(pts[len(pts)-1], t), true
	}
	return PlanePoint{}, false
}

// carry moves a point on with its speed and course to t.
func carry(p PlanePoint, t time.Time) PlanePoint {
	dt := t.Sub(p.T).Seconds()
	const r = 6371000.0
	tr := p.Track * math.Pi / 180
	q := p
	q.T = t
	q.Lat += p.Speed * math.Cos(tr) * dt / r * 180 / math.Pi
	q.Lon += p.Speed * math.Sin(tr) * dt / (r * math.Cos(p.Lat*math.Pi/180)) * 180 / math.Pi
	q.AltM += p.Vert * dt
	return q
}

// look: the direction (J2000) and distance of a point seen from the site
// at t, and its altitude above the horizon.
func (ob *Observer) lookPoint(p PlanePoint, t time.Time, prec *[3][3]float64) (Vec, float64) {
	e := Site{Lat: p.Lat, Lon: p.Lon, Height: p.AltM}.ecef()
	d := [3]float64{e[0] - ob.pos[0], e[1] - ob.pos[1], e[2] - ob.pos[2]}
	km := math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
	lat, lon := ob.site.Lat*math.Pi/180, ob.site.Lon*math.Pi/180
	up := [3]float64{math.Cos(lat) * math.Cos(lon), math.Cos(lat) * math.Sin(lon), math.Sin(lat)}
	alt := math.Asin((d[0]*up[0]+d[1]*up[1]+d[2]*up[2])/km) * 180 / math.Pi
	// the Earth fixed frame turned to the equator and equinox of date
	g := gstime(julian(t))
	cg, sg := math.Cos(g), math.Sin(g)
	q := [3]float64{d[0]*cg - d[1]*sg, d[0]*sg + d[1]*cg, d[2]}
	m := prec
	return Vec{
		X:  (m[0][0]*q[0] + m[0][1]*q[1] + m[0][2]*q[2]) / km,
		Y:  (m[1][0]*q[0] + m[1][1]*q[1] + m[1][2]*q[2]) / km,
		Z:  (m[2][0]*q[0] + m[2][1]*q[1] + m[2][2]*q[2]) / km,
		Km: km,
	}, alt
}

// IdentifyPlanes tells which planes left the tracks of a frame. A plane
// crosses a field of a degree in a second or two and its positions are
// known to some hundred metres and seconds: it is told by where and which
// way its path crosses the frame, not by when.
func IdentifyPlanes(f Frame, streaks []Streak, planes []*Plane) []PlaneMatch {
	if len(streaks) == 0 || len(planes) == 0 {
		return nil
	}
	w := f.WCS
	scale := w.Scale()
	cra, cdec := w.PixelToWorld(float64(f.Width)/2-0.5, float64(f.Height)/2-0.5)
	cx, cy, cz := fits.Direction(cra, cdec)
	radius := math.Hypot(float64(f.Width), float64(f.Height)) / 2 * scale
	near := math.Cos(math.Min(radius+3, 60) * math.Pi / 180)
	crossTol, angTol := 0.5, 8.0
	if w.Approx {
		crossTol, angTol = 1.7, 10
	}
	ob := NewObserver(f.Site)
	prec := precession(f.Start)
	exp := f.Exposure.Seconds()
	all := make([][]PlaneMatch, len(streaks))
	for _, p := range planes {
		// the path in pixels while it is near the frame; brk: the path
		// left the frame before this point
		type ps struct {
			x, y, alt, km float64
			brk           bool
		}
		var path []ps
		gap := true
		for t := -planeSlack; t <= exp+planeSlack; t += 0.05 {
			at := f.Start.Add(time.Duration(t * 1e9))
			pt, ok := p.at(at)
			if !ok {
				gap = true
				continue
			}
			v, alt := ob.lookPoint(pt, at, &prec)
			if alt < 0 || v.X*cx+v.Y*cy+v.Z*cz < near {
				gap = true
				continue
			}
			x, y, ok := w.WorldToPixel(v.X, v.Y, v.Z)
			if !ok {
				gap = true
				continue
			}
			path = append(path, ps{x, y, alt, v.Km, gap})
			gap = false
		}
		if len(path) < 2 {
			continue
		}
		for i, s := range streaks {
			mx, my := (s.X0+s.X1)/2, (s.Y0+s.Y1)/2
			sl := math.Hypot(s.X1-s.X0, s.Y1-s.Y0)
			best, bk := math.Inf(1), -1
			var angle float64
			for j := 1; j < len(path); j++ {
				a, b := path[j-1], path[j]
				if b.brk {
					continue
				}
				dx, dy := b.x-a.x, b.y-a.y
				l2 := dx*dx + dy*dy
				if l2 == 0 {
					continue
				}
				u := math.Max(0, math.Min(1, ((mx-a.x)*dx+(my-a.y)*dy)/l2))
				if d := math.Hypot(a.x+u*dx-mx, a.y+u*dy-my); d < best {
					best, bk = d, j
					c := math.Abs(((s.X1-s.X0)*dx + (s.Y1-s.Y0)*dy) / (sl * math.Sqrt(l2)))
					angle = math.Acos(math.Min(1, c)) * 180 / math.Pi
				}
			}
			cross := best * scale
			if bk < 0 || cross > crossTol || angle > angTol {
				continue
			}
			m := PlaneMatch{Streak: i, Plane: p, Cross: cross, Angle: angle, Alt: path[bk].alt, RangeKm: path[bk].km,
				score: (cross/crossTol)*(cross/crossTol) + (angle/angTol)*(angle/angTol)}
			if pt, ok := p.at(f.Start.Add(time.Duration(exp / 2 * 1e9))); ok {
				m.AltM = pt.AltM
			}
			all[i] = append(all[i], m)
		}
	}
	var out []PlaneMatch
	for _, ms := range all {
		if len(ms) == 0 {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].score < ms[b].score })
		m := ms[0]
		for _, o := range ms[1:] {
			if o.score < m.score*2+0.2 {
				m.Others++
			}
		}
		out = append(out, m)
	}
	return out
}
