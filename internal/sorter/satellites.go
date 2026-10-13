package sorter

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// Track is a straight track on a light frame (a satellite, a plane, a
// meteor), as the receiver tells it: its ends in pixels of the frame
// (0-based, the row order of the file), E0, E1: the end is at the edge;
// where its ends are in the sky (with the frame's WCS); Static: another
// frame has it in the same place of the sky (a nebula, not a satellite).
type Track struct {
	X0     float64    `json:"x0"`
	Y0     float64    `json:"y0"`
	X1     float64    `json:"x1"`
	Y1     float64    `json:"y1"`
	E0     bool       `json:"e0,omitempty"`
	E1     bool       `json:"e1,omitempty"`
	RA0    float64    `json:"ra0,omitempty"`
	Dec0   float64    `json:"dec0,omitempty"`
	RA1    float64    `json:"ra1,omitempty"`
	Dec1   float64    `json:"dec1,omitempty"`
	Sky    bool       `json:"sky,omitempty"`
	Static bool       `json:"static,omitempty"`
	Sat    *Satellite `json:"sat,omitempty"`
}

// Satellite is the object that left a track.
type Satellite struct {
	Name    string  `json:"name"`
	NORAD   int     `json:"norad"`
	Intl    string  `json:"intl,omitempty"`
	Offset  float64 `json:"off"`   // seconds later than predicted
	Cross   float64 `json:"cross"` // degrees beside the predicted path
	Km      float64 `json:"km"`    // distance from the observatory
	Alt     float64 `json:"alt"`   // degrees above the horizon
	Perigee float64 `json:"per"`   // km
	Apogee  float64 `json:"apo"`   // km
	Others  int     `json:"others,omitempty"`
}

// trackVersion: the version of the track search (as the receiver's); frames
// searched by an older one are searched again.
const trackVersion = 1

func (t Track) sky() satellites.SkyLine {
	return satellites.SkyLine{RA0: t.RA0, Dec0: t.Dec0, RA1: t.RA1, Dec1: t.Dec1}
}

// tracksOf converts the tracks found; w (may be nil) places them in the sky.
func tracksOf(st []satellites.Streak, w *fits.WCS) []Track {
	if len(st) == 0 {
		return nil
	}
	out := make([]Track, len(st))
	r := func(v float64) float64 { return math.Round(v*10) / 10 }
	for i, s := range st {
		t := Track{X0: r(s.X0), Y0: r(s.Y0), X1: r(s.X1), Y1: r(s.Y1), E0: s.Edge0, E1: s.Edge1}
		if w != nil && !w.Approx {
			t.RA0, t.Dec0 = w.PixelToWorld(s.X0, s.Y0)
			t.RA1, t.Dec1 = w.PixelToWorld(s.X1, s.Y1)
			t.Sky = true
		}
		out[i] = t
	}
	return out
}

// frameStart: the start of the exposure, unix milliseconds; 0 when the
// header does not tell.
func frameStart(h *fits.Header) int64 {
	v := strings.TrimSuffix(strings.TrimSpace(h.Get("DATE-OBS")), "Z")
	for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, v); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// SatCatalog keeps the catalogues of orbits downloaded from CelesTrak, one
// a day at most, in a folder (as the receiver does).
type SatCatalog struct {
	store satellites.Store
}

const (
	satRefresh = 20 * time.Hour // a new catalogue when the newest is this old
	satRetry   = 3 * time.Hour  // after a download tried (CelesTrak allows one in 2 hours)
	satKeep    = 60             // catalogues kept (about 2 MB each)
	// a frame is told again when a catalogue this much nearer its time appears
	satBetter = 6 * time.Hour
	satWeek   = 7 * 24 * time.Hour // tracks on frames farther apart are not compared
)

// NewSatCatalog keeps the catalogues in dir.
func NewSatCatalog(dir string) *SatCatalog {
	return &SatCatalog{store: satellites.Store{Dir: dir, Keep: satKeep}}
}

// tryFile: its time is when a download was last tried (the sorter is
// started and closed often).
func (c *SatCatalog) tryFile() string { return filepath.Join(c.store.Dir, "last-try") }

// due: a new catalogue is to be downloaded.
func (c *SatCatalog) due(now time.Time) bool {
	if cs := c.store.Catalogs(); len(cs) > 0 && now.Sub(cs[len(cs)-1].Time) < satRefresh {
		return false
	}
	if st, err := os.Stat(c.tryFile()); err == nil && now.Sub(st.ModTime()) < satRetry {
		return false
	}
	return true
}

// refresh downloads a catalogue when one is due; n: the objects in it (0:
// none downloaded).
func (c *SatCatalog) refresh(ctx context.Context) (int, error) {
	if !c.due(time.Now()) {
		return 0, nil
	}
	os.MkdirAll(c.store.Dir, 0o755)
	os.WriteFile(c.tryFile(), nil, 0o644)
	os.Chtimes(c.tryFile(), time.Now(), time.Now())
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	text, n, err := satellites.Download(dctx, &http.Client{Timeout: 3 * time.Minute}, "asiair-sorter/"+protocol.Version)
	if err == nil {
		_, err = c.store.Save(text, time.Now())
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

// SatReport tells how the identification went.
type SatReport struct {
	Frames     int    // frames with tracks looked at
	Told       int    // frames told now
	Found      int    // satellites found on them
	Static     int    // tracks found fixed in the sky (not satellites)
	Downloaded int    // objects in the catalogue downloaded now, 0: none
	CatalogErr string // the download failed
	Catalogs   int    // catalogues kept
}

// Text describes the report for the status line.
func (r SatReport) Text() string {
	if r.Frames == 0 {
		return ""
	}
	t := fmt.Sprintf("Треки спутников: кадров с треками %d", r.Frames)
	if r.Told > 0 {
		t += fmt.Sprintf(", опознано сейчас %d, спутников найдено %d", r.Told, r.Found)
	}
	if r.Static > 0 {
		t += fmt.Sprintf(", неподвижных (туманность, галактика) %d", r.Static)
	}
	if r.Downloaded > 0 {
		t += fmt.Sprintf("; скачан каталог орбит CelesTrak: %d объектов", r.Downloaded)
	}
	switch {
	case r.CatalogErr != "" && r.Catalogs == 0:
		t += "; каталог орбит не скачан (" + r.CatalogErr + ") — спутники не опознаны"
	case r.CatalogErr != "":
		t += "; новый каталог орбит не скачан (" + r.CatalogErr + "), взят прежний"
	}
	return t + "."
}

// markStatic marks the tracks found in the same place of the sky on
// another frame (taken within a week); it returns how many it marked.
func markStatic(ms []*Measurement) int {
	type ref struct{ m, i int }
	var all []ref
	for m, x := range ms {
		for i, t := range x.Tracks {
			if t.Sky {
				all = append(all, ref{m, i})
			}
		}
	}
	n := 0
	for x := 0; x < len(all); x++ {
		for y := x + 1; y < len(all); y++ {
			a, b := all[x], all[y]
			if a.m == b.m {
				continue
			}
			ma, mb := ms[a.m], ms[b.m]
			if d := time.Duration(ma.start-mb.start) * time.Millisecond; d > satWeek || d < -satWeek {
				continue
			}
			ta, tb := &ma.Tracks[a.i], &mb.Tracks[b.i]
			if ta.Static && tb.Static || !satellites.SameSky(ta.sky(), tb.sky()) {
				continue
			}
			for _, t := range []*Track{ta, tb} {
				if !t.Static {
					t.Static, t.Sat = true, nil
					n++
				}
			}
		}
	}
	return n
}

// IdentifySatellites tells the objects that left the tracks of the frames
// measured (MeasureFrames): tracks fixed in the sky on another frame are
// left out, the others are compared with the orbits of the catalogue
// downloaded nearest the frame's time (one is downloaded when the newest
// is a day old). The results are kept in the cache; got is called for each
// measurement changed. site is taken when a header has no coordinates
// (nil: none).
func IdentifySatellites(ctx context.Context, ms []Measurement, cache *QualityCache, cat *SatCatalog, site *satellites.Site, got func(Measurement)) SatReport {
	var rep SatReport
	var with []*Measurement
	for i := range ms {
		if len(ms[i].Tracks) > 0 {
			ms[i].Tracks = append([]Track(nil), ms[i].Tracks...) // not shared with the cache
			with = append(with, &ms[i])
		}
	}
	rep.Frames = len(with)
	if len(with) == 0 {
		return rep
	}
	if rep.Static = markStatic(with); rep.Static > 0 {
		for _, m := range with {
			cache.put(*m)
			got(*m)
		}
	}
	// what is still to be told
	pending := func() []*Measurement {
		var out []*Measurement
		for _, m := range with {
			if m.start == 0 || !m.moving() {
				continue
			}
			start := time.UnixMilli(m.start)
			c, ok := cat.store.Nearest(start)
			if ok && m.satCat != "" && (filepath.Base(c.Path) == m.satCat ||
				absDur(c.Time.Sub(start)) > absDur(time.UnixMilli(m.satCatT).Sub(start))-satBetter) {
				continue
			}
			out = append(out, m)
		}
		return out
	}
	if len(pending()) > 0 {
		n, err := cat.refresh(ctx)
		rep.Downloaded = n
		if err != nil {
			rep.CatalogErr = err.Error()
		}
	}
	rep.Catalogs = len(cat.store.Catalogs())
	jobs := pending()
	if rep.Catalogs == 0 || len(jobs) == 0 {
		return rep
	}
	type job struct {
		m   *Measurement
		cat satellites.CatalogFile
	}
	var js []job
	for _, m := range jobs {
		c, _ := cat.store.Nearest(time.UnixMilli(m.start))
		js = append(js, job{m, c})
	}
	// by catalogue: each is loaded once
	sort.SliceStable(js, func(i, j int) bool { return js[i].cat.Time.After(js[j].cat.Time) })
	var loaded string
	var orbits []*satellites.Orbit
	for _, j := range js {
		if ctx.Err() != nil {
			break
		}
		if loaded != j.cat.Path {
			o, err := satellites.Load(j.cat.Path)
			if err != nil {
				os.Remove(j.cat.Path) // a broken file: downloaded again
				rep.CatalogErr = err.Error()
				break
			}
			orbits, loaded = o, j.cat.Path
		}
		m := j.m
		found, err := identifyFrame(m, orbits, site)
		m.satErr = ""
		if err != nil {
			m.satErr = err.Error()
		}
		m.satCat, m.satCatT = filepath.Base(j.cat.Path), j.cat.Time.UnixMilli()
		cache.put(*m)
		got(*m)
		rep.Told++
		rep.Found += found
	}
	return rep
}

// identifyFrame tells the objects of the tracks of a measured frame; it
// returns how many it found.
func identifyFrame(m *Measurement, orbits []*satellites.Orbit, site *satellites.Site) (int, error) {
	for i := range m.Tracks {
		m.Tracks[i].Sat = nil
	}
	f, err := os.Open(m.path)
	if err != nil {
		return 0, err
	}
	h, err := fits.ReadHeader(f)
	f.Close()
	if err != nil {
		return 0, err
	}
	fr, err := satellites.FrameFromHeader(h, m.path, site)
	if err != nil {
		return 0, err
	}
	var st []satellites.Streak
	var idx []int
	for i, t := range m.Tracks {
		if !t.Static {
			st = append(st, satellites.Streak{X0: t.X0, Y0: t.Y0, X1: t.X1, Y1: t.Y1, Edge0: t.E0, Edge1: t.E1})
			idx = append(idx, i)
		}
	}
	n := 0
	for _, x := range satellites.Identify(fr, st, orbits) {
		o := x.Orbit
		m.Tracks[idx[x.Streak]].Sat = &Satellite{Name: o.Name, NORAD: o.NORAD, Intl: o.IntlID,
			Offset: math.Round(x.Offset*10) / 10, Cross: math.Round(x.Cross*1000) / 1000, Km: math.Round(x.RangeKm),
			Alt: math.Round(x.Alt*10) / 10, Perigee: math.Round(o.PerigeeKm()), Apogee: math.Round(o.ApogeeKm()),
			Others: x.Others}
		n++
	}
	return n, nil
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TracksText is the column of the table: the objects of the tracks ("?"
// not told), or how many tracks there are while not looked for.
func (f *Frame) TracksText() string {
	if len(f.Tracks) == 0 {
		return ""
	}
	if !f.SatTold {
		return fmt.Sprintf("треков: %d", len(f.Tracks))
	}
	names := make([]string, len(f.Tracks))
	for i, t := range f.Tracks {
		names[i] = "?"
		if t.Sat != nil {
			names[i] = t.Sat.Name
		}
	}
	return strings.Join(names, ", ")
}

// TrackLabel: what is written beside a track on the preview.
func (f *Frame) TrackLabel(t Track) string {
	switch {
	case t.Sat != nil:
		return fmt.Sprintf("%s (%d)", t.Sat.Name, t.Sat.NORAD)
	case f.SatTold:
		return "не опознан"
	}
	return "трек"
}

// TrackText tells what left a track, in words.
func (f *Frame) TrackText(t Track) string {
	if s := t.Sat; s != nil {
		orbit := fmt.Sprintf("орбита %.0f–%.0f км", s.Perigee, s.Apogee)
		switch {
		case s.Apogee < 2000:
			orbit = fmt.Sprintf("низкая орбита %.0f–%.0f км", s.Perigee, s.Apogee)
		case s.Perigee > 35000 && s.Apogee < 36600:
			orbit = "геостационарная орбита"
		}
		off := "по прогнозу"
		if math.Abs(s.Offset) >= 1 {
			dir := "раньше"
			if s.Offset > 0 {
				dir = "позже"
			}
			off = fmt.Sprintf("на %s с %s прогноза", ruFloat(math.Abs(s.Offset), 1), dir)
		}
		id := fmt.Sprintf("NORAD %d", s.NORAD)
		if s.Intl != "" {
			id += ", " + s.Intl
		}
		txt := fmt.Sprintf("%s (%s): %s, %.0f км от обсерватории, %s° над горизонтом; прошёл %s, в %s′ от расчётного пути",
			s.Name, id, orbit, s.Km, ruFloat(s.Alt, 1), off, ruFloat(s.Cross*60, 1))
		if f.SatAge > 2 {
			txt += fmt.Sprintf("; орбиты взяты в %s дн. от кадра — точность ниже", ruFloat(f.SatAge, 1))
		}
		if s.Others > 0 {
			txt += fmt.Sprintf("; почти так же подходят ещё %d", s.Others)
		}
		return txt
	}
	switch {
	case f.SatErr != "":
		return "не опознан: " + f.SatErr
	case f.SatTold:
		return "не опознан: самолёт, метеор или объекта нет в открытом каталоге"
	}
	return "опознание — после загрузки каталога орбит"
}

// TracksInfo: every track of the frame in words, a line each.
func (f *Frame) TracksInfo() string {
	var b strings.Builder
	for i, t := range f.Tracks {
		if i > 0 {
			b.WriteString("\r\n")
		}
		if len(f.Tracks) > 1 {
			fmt.Fprintf(&b, "Трек %d: ", i+1)
		} else {
			b.WriteString("Трек: ")
		}
		b.WriteString(f.TrackText(t))
	}
	return b.String()
}

func ruFloat(v float64, dec int) string {
	return strings.Replace(fmt.Sprintf("%.*f", dec, v), ".", ",", 1)
}
