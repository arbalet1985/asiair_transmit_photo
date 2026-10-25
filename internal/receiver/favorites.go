package receiver

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// The favorite objects of the planner and how each is seen in a night, as
// the list of ASIAIR shows them: the altitude through the night, the rise,
// the culmination and the set; and the filter of ASIAIR: the objects that
// at some moment of a time window are in an altitude range and in one of
// the chosen sides of the sky.

// Favorite is an object kept in the favorites of the planner.
type Favorite struct {
	Name  string  `json:"name"`
	Label string  `json:"label,omitempty"`
	RA    float64 `json:"ra"`  // J2000, degrees
	Dec   float64 `json:"dec"` // J2000, degrees
	Kind  string  `json:"kind,omitempty"`
}

// visEvery: the samples of the altitude curve.
const visEvery = 10 * time.Minute

const maxVisObjects = 200

// visObject is how an object is seen in the night.
type visObject struct {
	Alt  []float64 `json:"alt"`  // every visEvery from the sunset
	Open string    `json:"open"` // a "1" or "0" per sample: open to the telescope (the sky of the planner)
	// over the true horizon, in the day from noon to noon: 0 when it does
	// not happen (Up tells whether it is then always up or always down)
	Rise int64 `json:"rise,omitempty"`
	Set  int64 `json:"set,omitempty"`
	Up   bool  `json:"up,omitempty"`
	// the upper culmination
	Transit    int64   `json:"transit"`
	TransitAlt float64 `json:"transit_alt"`
	// open to the telescope from the sunset to the sunrise, and in the dark
	Spans [][2]int64 `json:"spans"`
	Dark  float64    `json:"dark"` // seconds
	// the filter: whether it passes and for how long (seconds)
	Match    bool    `json:"match"`
	MatchSec float64 `json:"match_sec,omitempty"`
}

// skyFilter is the filter of ASIAIR.
type skyFilter struct {
	From   int64   `json:"from,omitempty"` // unix milliseconds; 0: the dark night
	To     int64   `json:"to,omitempty"`
	AltMin float64 `json:"alt_min"`
	AltMax float64 `json:"alt_max"`
	// Dirs: the sides of the sky, a bit each from the north clockwise
	// (С, СВ, В, ЮВ, Ю, ЮЗ, З, СЗ — 45° each); 0 = all
	Dirs uint8  `json:"dirs,omitempty"`
	Open bool   `json:"open,omitempty"` // also open to the telescope (the horizon and the zones of the planner)
	Kind string `json:"kind,omitempty"` // galaxy, nebula, cluster; "" = all
}

// side is the bit of the side of the sky at azimuth az.
func side(az float64) uint8 {
	return 1 << (int(math.Floor(math.Mod(az+22.5+720, 360)/45)) % 8)
}

// window is the time the filter looks at.
func (f *skyFilter) window(from, to time.Time, dusk, dawn time.Time, dark bool) (time.Time, time.Time) {
	a, b := from, to
	if dark {
		a, b = dusk, dawn
	}
	if f.From != 0 && f.To > f.From {
		a, b = time.UnixMilli(f.From), time.UnixMilli(f.To)
	}
	return a, b
}

// match: how long in [a, b] the point is in the filter, by step.
func (f *skyFilter) match(sky planner.Sky, lat, lon, ra, dec float64, a, b time.Time, step time.Duration) float64 {
	n := 0
	for t := a; !t.After(b); t = t.Add(step) {
		p := moon.Object(t, lat, lon, ra, dec)
		if p.Alt < f.AltMin || p.Alt > f.AltMax || f.Dirs != 0 && f.Dirs&side(p.Az) == 0 || f.Open && !sky.Visible(p.Az, p.Alt) {
			continue
		}
		n++
	}
	return float64(n) * step.Seconds()
}

// visibility computes how objects are seen in the night at lat, lon over
// sky; dark: the night of sunAlt.
func visibility(night string, lat, lon, sunAlt float64, sky planner.Sky, objs []Favorite, f *skyFilter) (map[string]any, error) {
	d, err := time.Parse("2006-01-02", night)
	if err != nil {
		return nil, errors.New("дата ночи: нужна вида 2026-10-01")
	}
	noon := d.Add(12*time.Hour - time.Duration(lon/15*float64(time.Hour)))
	// the chart: from the sunset to the sunrise, the whole day when the Sun
	// does not set
	from, to, err := planner.Night(night, lat, lon, -0.833)
	if err != nil {
		from, to = noon, noon.Add(24*time.Hour)
	}
	dusk, dawn, derr := planner.Night(night, lat, lon, sunAlt)
	var fa, fb time.Time
	if f != nil {
		fa, fb = f.window(from, to, dusk, dawn, derr == nil)
	}
	out := make([]visObject, 0, len(objs))
	for _, o := range objs {
		v := visObject{Spans: [][2]int64{}, Match: true}
		if f != nil {
			v.MatchSec = f.match(sky, lat, lon, o.RA, o.Dec, fa, fb, 5*time.Minute)
			v.Match = v.MatchSec > 0
		}
		// rise, set and culmination by the minute over the day
		prev := false
		for i := 0; i <= 24*60; i++ {
			t := noon.Add(time.Duration(i) * time.Minute)
			p := moon.Object(t, lat, lon, o.RA, o.Dec)
			up := p.Alt > 0
			if i > 0 && up && !prev && v.Rise == 0 {
				v.Rise = t.UnixMilli()
			}
			if i > 0 && !up && prev && v.Set == 0 {
				v.Set = t.UnixMilli()
			}
			if i == 0 || p.Alt > v.TransitAlt {
				v.Transit, v.TransitAlt = t.UnixMilli(), p.Alt
			}
			if i == 0 {
				v.Up = up
			}
			prev = up
		}
		v.TransitAlt = round1(v.TransitAlt)
		// open to the telescope in the chart
		var open []byte
		var start time.Time
		for t := from; !t.After(to); t = t.Add(time.Minute) {
			p := moon.Object(t, lat, lon, o.RA, o.Dec)
			ok := sky.Visible(p.Az, p.Alt)
			if t.Sub(from)%visEvery == 0 {
				v.Alt = append(v.Alt, round1(p.Alt))
				open = append(open, map[bool]byte{true: '1', false: '0'}[ok])
			}
			if ok && start.IsZero() {
				start = t
			}
			if (!ok || !t.Before(to)) && !start.IsZero() {
				v.Spans = append(v.Spans, [2]int64{start.UnixMilli(), t.UnixMilli()})
				start = time.Time{}
			}
			if ok && derr == nil && !t.Before(dusk) && t.Before(dawn) {
				v.Dark += 60
			}
		}
		v.Open = string(open)
		out = append(out, v)
	}
	res := map[string]any{"from": from.UnixMilli(), "to": to.UnixMilli(), "every": visEvery.Milliseconds(), "objects": out}
	if derr == nil {
		res["dusk"], res["dawn"] = dusk.UnixMilli(), dawn.UnixMilli()
	}
	if f != nil {
		res["filter_from"], res["filter_to"] = fa.UnixMilli(), fb.UnixMilli()
	}
	return res, nil
}

const maxBest = 60

// best: the objects of the catalog in the filter, the best known and the
// longest in it first, as "Tonight's Best" of ASIAIR.
func best(night string, lat, lon, sunAlt float64, sky planner.Sky, f *skyFilter) ([]Favorite, error) {
	from, to, err := planner.Night(night, lat, lon, -0.833)
	if err != nil {
		return nil, errors.New("в эту ночь Солнце не заходит")
	}
	dusk, dawn, derr := planner.Night(night, lat, lon, sunAlt)
	a, b := f.window(from, to, dusk, dawn, derr == nil)
	type hit struct {
		o    catalog.Object
		fame int
		sec  float64
	}
	var hits []hit
	for _, o := range catalog.All() {
		g := catalog.Group(o.Kind)
		if g == "" || f.Kind != "" && g != f.Kind {
			continue
		}
		fame := o.Fame()
		// the faint galaxies of NGC and IC are too many: the small ones only
		// when they are well known
		if fame == 2 && o.Size < 8 {
			continue
		}
		if sec := f.match(sky, lat, lon, o.RA, o.Dec, a, b, 20*time.Minute); sec > 0 {
			hits = append(hits, hit{o, fame, sec})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].fame != hits[j].fame {
			return hits[i].fame < hits[j].fame
		}
		if hits[i].sec != hits[j].sec {
			return hits[i].sec > hits[j].sec
		}
		return hits[i].o.Size > hits[j].o.Size
	})
	out := []Favorite{}
	for _, h := range hits {
		if len(out) == maxBest {
			break
		}
		out = append(out, Favorite{Name: h.o.Name, Label: h.o.Label(), RA: h.o.RA, Dec: h.o.Dec, Kind: h.o.Kind})
	}
	return out, nil
}

func (s *Server) handleUIVisibility(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Night   string      `json:"night"`
		SunAlt  float64     `json:"sun_alt"`
		Sky     planner.Sky `json:"sky"`
		Objects []Favorite  `json:"objects"`
		Filter  *skyFilter  `json:"filter,omitempty"`
		Best    bool        `json:"best,omitempty"` // the objects: the best of the catalog in the filter
	}
	if !readJSON(w, r, &req) {
		return
	}
	site := s.state.Snapshot().Site
	if !site.Set {
		http.Error(w, "задайте координаты обсерватории (блок «Координаты обсерватории» в настройках)", http.StatusBadRequest)
		return
	}
	if len(req.Objects) > maxVisObjects {
		req.Objects = req.Objects[:maxVisObjects]
	}
	if req.SunAlt == 0 {
		req.SunAlt = planner.DefaultParams().SunAlt
	}
	if req.Best {
		if req.Filter == nil {
			req.Filter = &skyFilter{AltMin: 30, AltMax: 90}
		}
		var err error
		if req.Objects, err = best(req.Night, site.Lat, site.Lon, req.SunAlt, req.Sky, req.Filter); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	res, err := visibility(req.Night, site.Lat, site.Lon, req.SunAlt, req.Sky, req.Objects, req.Filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Best {
		res["list"] = req.Objects
	}
	writeJSON(w, res)
}
