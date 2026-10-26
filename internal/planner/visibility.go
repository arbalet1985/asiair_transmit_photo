package planner

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

// How objects are seen in a night, as the lists of ASIAIR show them: the
// altitude through the night, the rise, the culmination and the set; and
// the filter of ASIAIR: the objects that at some moment of a time window
// are in an altitude range and in one of the chosen sides of the sky.

// Favorite is an object kept in the favorites of the planner.
type Favorite struct {
	Name  string  `json:"name"`
	Label string  `json:"label,omitempty"`
	RA    float64 `json:"ra"`  // J2000, degrees
	Dec   float64 `json:"dec"` // J2000, degrees
	Kind  string  `json:"kind,omitempty"`
	Size  float64 `json:"size,omitempty"` // arcminutes across (0: unknown)
	Note  string  `json:"note,omitempty"` // the user's own few words
}

// MaxNote: the longest note of a favorite, characters.
const MaxNote = 300

// CleanNote: the note trimmed and cut to MaxNote.
func CleanNote(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxNote {
		s = strings.TrimSpace(string(r[:MaxNote]))
	}
	return s
}

// VisEvery: the samples of the altitude curve.
const VisEvery = 10 * time.Minute

// Vis is how an object is seen in the night; times are unix milliseconds.
type Vis struct {
	Alt  []float64 `json:"alt"`  // every VisEvery from the sunset
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

// Visibility is how objects are seen in a night.
type Visibility struct {
	From, To   time.Time // the sunset and the sunrise (noon to noon when the Sun does not set)
	Dusk, Dawn time.Time // the dark night; zero when there is none
	// the time window of the filter
	FilterFrom, FilterTo time.Time
	Objects              []Vis
}

// Filter is the filter of ASIAIR.
type Filter struct {
	From   int64   `json:"from,omitempty"` // unix milliseconds; 0: the dark night
	To     int64   `json:"to,omitempty"`
	AltMin float64 `json:"alt_min"`
	AltMax float64 `json:"alt_max"`
	// Dirs: the sides of the sky (Sides), a bit each from the north
	// clockwise, 45° each; 0 = all
	Dirs uint8  `json:"dirs,omitempty"`
	Open bool   `json:"open,omitempty"` // also open to the telescope (the horizon and the zones of the planner)
	Kind string `json:"kind,omitempty"` // galaxy, nebula, cluster; "" = all
}

// Sides are the sides of the sky of Filter.Dirs.
var Sides = []string{"С", "СВ", "В", "ЮВ", "Ю", "ЮЗ", "З", "СЗ"}

// Side is the bit of the side of the sky at azimuth az.
func Side(az float64) uint8 {
	return 1 << (int(math.Floor(math.Mod(az+22.5+720, 360)/45)) % 8)
}

// window is the time the filter looks at.
func (f *Filter) window(from, to, dusk, dawn time.Time) (time.Time, time.Time) {
	a, b := from, to
	if !dusk.IsZero() {
		a, b = dusk, dawn
	}
	if f.From != 0 && f.To > f.From {
		a, b = time.UnixMilli(f.From), time.UnixMilli(f.To)
	}
	return a, b
}

// match: how long in [a, b] the point is in the filter, by step.
func (f *Filter) match(sky Sky, lat, lon, ra, dec float64, a, b time.Time, step time.Duration) float64 {
	n := 0
	for t := a; !t.After(b); t = t.Add(step) {
		p := moon.Object(t, lat, lon, ra, dec)
		if p.Alt < f.AltMin || p.Alt > f.AltMax || f.Dirs != 0 && f.Dirs&Side(p.Az) == 0 || f.Open && !sky.Visible(p.Az, p.Alt) {
			continue
		}
		n++
	}
	return float64(n) * step.Seconds()
}

// nights: the sunset and the sunrise of the night (noon to noon when the
// Sun does not set) and its dark part of sunAlt (zero when there is none).
func nights(night string, lat, lon, sunAlt float64) (from, to, dusk, dawn time.Time, err error) {
	d, err := time.Parse("2006-01-02", night)
	if err != nil {
		return from, to, dusk, dawn, errors.New("дата ночи: нужна вида 2026-10-01")
	}
	noon := d.Add(12*time.Hour - time.Duration(lon/15*float64(time.Hour)))
	if from, to, err = Night(night, lat, lon, -0.833); err != nil {
		from, to = noon, noon.Add(24*time.Hour)
	}
	if dusk, dawn, err = Night(night, lat, lon, sunAlt); err != nil {
		dusk, dawn = time.Time{}, time.Time{}
	}
	return from, to, dusk, dawn, nil
}

// See computes how objects are seen in the night at lat, lon over sky; the
// dark night is of sunAlt; f, when not nil, is the filter they are tried.
func See(night string, lat, lon, sunAlt float64, sky Sky, objs []Favorite, f *Filter) (*Visibility, error) {
	from, to, dusk, dawn, err := nights(night, lat, lon, sunAlt)
	if err != nil {
		return nil, err
	}
	d, _ := time.Parse("2006-01-02", night)
	noon := d.Add(12*time.Hour - time.Duration(lon/15*float64(time.Hour)))
	v := &Visibility{From: from, To: to, Dusk: dusk, Dawn: dawn, Objects: make([]Vis, 0, len(objs))}
	if f != nil {
		v.FilterFrom, v.FilterTo = f.window(from, to, dusk, dawn)
	}
	for _, o := range objs {
		s := Vis{Spans: [][2]int64{}, Match: true}
		if f != nil {
			s.MatchSec = f.match(sky, lat, lon, o.RA, o.Dec, v.FilterFrom, v.FilterTo, 5*time.Minute)
			s.Match = s.MatchSec > 0
		}
		// rise, set and culmination by the minute over the day
		prev := false
		for i := 0; i <= 24*60; i++ {
			t := noon.Add(time.Duration(i) * time.Minute)
			p := moon.Object(t, lat, lon, o.RA, o.Dec)
			up := p.Alt > 0
			if i > 0 && up && !prev && s.Rise == 0 {
				s.Rise = t.UnixMilli()
			}
			if i > 0 && !up && prev && s.Set == 0 {
				s.Set = t.UnixMilli()
			}
			if i == 0 || p.Alt > s.TransitAlt {
				s.Transit, s.TransitAlt = t.UnixMilli(), p.Alt
			}
			if i == 0 {
				s.Up = up
			}
			prev = up
		}
		s.TransitAlt = math.Round(s.TransitAlt*10) / 10
		// open to the telescope in the chart
		var open []byte
		var start time.Time
		for t := from; !t.After(to); t = t.Add(time.Minute) {
			p := moon.Object(t, lat, lon, o.RA, o.Dec)
			ok := sky.Visible(p.Az, p.Alt)
			if t.Sub(from)%VisEvery == 0 {
				s.Alt = append(s.Alt, math.Round(p.Alt*10)/10)
				open = append(open, map[bool]byte{true: '1', false: '0'}[ok])
			}
			if ok && start.IsZero() {
				start = t
			}
			if (!ok || !t.Before(to)) && !start.IsZero() {
				s.Spans = append(s.Spans, [2]int64{start.UnixMilli(), t.UnixMilli()})
				start = time.Time{}
			}
			if ok && !dusk.IsZero() && !t.Before(dusk) && t.Before(dawn) {
				s.Dark += 60
			}
		}
		s.Open = string(open)
		v.Objects = append(v.Objects, s)
	}
	return v, nil
}

// MaxBest: how many objects Best gives.
const MaxBest = 60

// Best: the objects of the catalog in the filter, the best known and the
// longest in it first, as "Tonight's Best" of ASIAIR.
func Best(night string, lat, lon, sunAlt float64, sky Sky, f *Filter) ([]Favorite, error) {
	from, to, dusk, dawn, err := nights(night, lat, lon, sunAlt)
	if err != nil {
		return nil, err
	}
	a, b := f.window(from, to, dusk, dawn)
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
		if len(out) == MaxBest {
			break
		}
		out = append(out, Favorite{Name: h.o.Name, Label: h.o.Label(), RA: h.o.RA, Dec: h.o.Dec, Kind: h.o.Kind, Size: h.o.Size})
	}
	return out, nil
}

// KindName: the type of an object in Russian.
var KindName = map[string]string{"G": "галактика", "GPair": "пара галактик", "GTrpl": "три галактики", "GGroup": "группа галактик",
	"HII": "область HII", "Neb": "туманность", "EmN": "эмиссионная туманность", "RfN": "отражательная туманность", "SNR": "остаток сверхновой",
	"PN": "планетарная туманность", "Cl+N": "скопление с туманностью", "DrkN": "тёмная туманность", "OCl": "рассеянное скопление",
	"GCl": "шаровое скопление", "*Ass": "звёздная ассоциация", "кадры": "из кадров"}
