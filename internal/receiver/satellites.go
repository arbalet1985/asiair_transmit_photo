package receiver

import (
	"context"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// trackEntry is a straight track found on a frame, in its pixels (0-based,
// the row order of the file); E0, E1: the end is at the edge of the frame.
type trackEntry struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	E0 bool    `json:"e0,omitempty"`
	E1 bool    `json:"e1,omitempty"`
	// where its ends are in the sky (degrees; with the frame's WCS)
	RA0  float64 `json:"ra0,omitempty"`
	Dec0 float64 `json:"dec0,omitempty"`
	RA1  float64 `json:"ra1,omitempty"`
	Dec1 float64 `json:"dec1,omitempty"`
	Sky  bool    `json:"sky,omitempty"`
	// Static: another frame has it in the same place of the sky: a
	// filament of a nebula or an edge-on galaxy, not a satellite
	Static bool `json:"static,omitempty"`
}

// satEntry: the object that left track T.
type satEntry struct {
	T       int     `json:"t"`
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

// trackVersion: the version of the track search; frames searched by an
// older one are searched again.
const trackVersion = 3

// tracksOf converts the tracks found; w (may be nil) places them in the sky.
func tracksOf(st []satellites.Streak, w *fits.WCS) []trackEntry {
	out := make([]trackEntry, len(st))
	r := func(v float64) float64 { return math.Round(v*10) / 10 }
	for i, s := range st {
		e := trackEntry{X0: r(s.X0), Y0: r(s.Y0), X1: r(s.X1), Y1: r(s.Y1), E0: s.Edge0, E1: s.Edge1}
		if w != nil && !w.Approx {
			e.RA0, e.Dec0 = w.PixelToWorld(s.X0, s.Y0)
			e.RA1, e.Dec1 = w.PixelToWorld(s.X1, s.Y1)
			e.Sky = true
		}
		out[i] = e
	}
	return out
}

// streaksOf: the tracks to tell, with their indexes (static ones left out).
func streaksOf(t []trackEntry) ([]satellites.Streak, []int) {
	var out []satellites.Streak
	var idx []int
	for i, e := range t {
		if e.Static {
			continue
		}
		out = append(out, satellites.Streak{X0: e.X0, Y0: e.Y0, X1: e.X1, Y1: e.Y1, Edge0: e.E0, Edge1: e.E1})
		idx = append(idx, i)
	}
	return out, idx
}

// sky: the track placed in the sky.
func (t trackEntry) sky() satellites.SkyLine {
	return satellites.SkyLine{RA0: t.RA0, Dec0: t.Dec0, RA1: t.RA1, Dec1: t.Dec1}
}

// markStatic marks the tracks found in the same place of the sky on
// another frame (taken within a week); it returns how many it marked.
func (q *quality) markStatic() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	type ref struct {
		p string
		i int
	}
	var all []ref
	for p, e := range q.m {
		for i, t := range e.Tracks {
			if t.Sky {
				all = append(all, ref{p, i})
			}
		}
	}
	n := 0
	for x := 0; x < len(all); x++ {
		for y := x + 1; y < len(all); y++ {
			a, b := all[x], all[y]
			if a.p == b.p {
				continue
			}
			ea, eb := q.m[a.p], q.m[b.p]
			if d := ea.Start - eb.Start; d > 7*864e5 || d < -7*864e5 {
				continue
			}
			ta, tb := ea.Tracks[a.i], eb.Tracks[b.i]
			if ta.Static && tb.Static || !satellites.SameSky(ta.sky(), tb.sky()) {
				continue
			}
			for _, r := range []ref{a, b} {
				e := q.m[r.p]
				if !e.Tracks[r.i].Static {
					e.Tracks[r.i].Static = true
					// a satellite told for it is dropped
					var keep []satEntry
					for _, s := range e.Sats {
						if s.T != r.i {
							keep = append(keep, s)
						}
					}
					e.Sats = keep
					q.m[r.p] = e
					n++
				}
			}
			q.dirty = true
		}
	}
	return n
}

// satState: the catalogues of orbits and the one loaded.
type satState struct {
	mu      sync.Mutex
	store   satellites.Store
	loaded  string
	orbits  []*satellites.Orbit
	lastTry time.Time
	err     string // of the last download
	wake    chan struct{}
	// the login of Space-Track: the orbits of the past
	stUser, stPass string
	histErr        string
	histTried      map[string]time.Time // nights asked, by their first hour
}

const (
	satRefresh = 20 * time.Hour // a new catalogue when the newest is this old
	satRetry   = 3 * time.Hour  // after a failed download (CelesTrak allows one in 2 hours)
	satKeep    = 60             // catalogues kept (about 2 MB each)
	// a frame is told again when a catalogue this much nearer its time appears
	satBetter = 6 * time.Hour
)

func newSatState(dir, stUser, stPass string) *satState {
	return &satState{store: satellites.Store{Dir: dir, Keep: satKeep}, wake: make(chan struct{}, 1),
		stUser: stUser, stPass: stPass, histTried: map[string]time.Time{}}
}

func (st *satState) poke() {
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// satelliteWorker keeps a fresh catalogue of orbits and tells the objects
// of the tracks found on the frames.
func (s *Server) satelliteWorker(ctx context.Context) {
	st := s.sats
	for {
		s.refreshCatalog(ctx)
		if n := s.quality.markStatic(); n > 0 {
			log.Printf("Приёмник: неподвижных треков (туманность, галактика — не спутник): %d", n)
		}
		s.fetchHistory(ctx)
		if n := s.identifyPending(ctx); n > 0 {
			s.quality.save()
		}
		if n := s.tellPlanes(ctx); n > 0 {
			s.quality.save()
		}
		st.mu.Lock()
		st.orbits, st.loaded = nil, "" // the memory back while idle
		st.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-st.wake:
		case <-time.After(time.Hour):
		}
	}
}

// refreshCatalog downloads the catalogue once a day.
func (s *Server) refreshCatalog(ctx context.Context) {
	st := s.sats
	c := st.store.Downloads()
	if len(c) > 0 && time.Since(c[len(c)-1].Time) < satRefresh || time.Since(st.lastTry) < satRetry {
		return
	}
	st.lastTry = time.Now()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	text, n, err := satellites.Download(dctx, &http.Client{Timeout: 3 * time.Minute}, "asiair-receiver/"+protocol.Version)
	if err == nil {
		_, err = st.store.Save(text, time.Now())
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err != nil {
		st.err = err.Error()
		log.Printf("Приёмник: каталог орбит спутников не скачан: %v", err)
		return
	}
	st.err = ""
	log.Printf("Приёмник: скачан каталог орбит спутников с CelesTrak: %d объектов", n)
}

// orbitsOf loads a catalogue (one is kept in memory).
func (st *satState) orbitsOf(c satellites.CatalogFile) ([]*satellites.Orbit, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loaded == c.Path {
		return st.orbits, nil
	}
	o, err := satellites.Load(c.Path)
	if err != nil {
		return nil, err
	}
	st.orbits, st.loaded = o, c.Path
	return o, nil
}

// identifyPending tells the objects of the frames with tracks not told yet,
// or told with a catalogue much farther from their time than one there is
// now. It returns how many frames it told.
func (s *Server) identifyPending(ctx context.Context) int {
	st := s.sats
	if len(st.store.Catalogs()) == 0 {
		return 0
	}
	type job struct {
		path string
		e    qualityEntry
		cat  satellites.CatalogFile
	}
	var jobs []job
	s.quality.mu.Lock()
	for p, e := range s.quality.m {
		if len(e.Tracks) == 0 || e.Start == 0 {
			continue
		}
		start := time.UnixMilli(e.Start)
		c, ok := st.store.Nearest(start)
		if !ok {
			continue
		}
		if e.SatCat != "" {
			if filepath.Base(c.Path) == e.SatCat ||
				absDur(c.Time.Sub(start)) > absDur(time.UnixMilli(e.SatCatT).Sub(start))-satBetter {
				continue
			}
		}
		jobs = append(jobs, job{p, e, c})
	}
	s.quality.mu.Unlock()
	if len(jobs) == 0 {
		return 0
	}
	// by catalogue, then newest first
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].cat.Path != jobs[j].cat.Path {
			return jobs[i].cat.Time.After(jobs[j].cat.Time)
		}
		return jobs[i].e.Start > jobs[j].e.Start
	})
	store := s.getStore()
	snap := s.state.Snapshot()
	var site *satellites.Site
	if snap.Site.Set {
		site = &satellites.Site{Lat: snap.Site.Lat, Lon: snap.Site.Lon}
	}
	n, found := 0, 0
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		orbits, err := st.orbitsOf(j.cat)
		if err != nil {
			log.Printf("Приёмник: каталог орбит %s не прочитан: %v", j.cat.Path, err)
			os.Remove(j.cat.Path) // a broken file: downloaded again
			return n
		}
		e := j.e
		e.Sats, e.SatErr = nil, ""
		e.Planes, e.PlanesDone = nil, false // told again for the tracks left
		ms, err := identifyFrame(store.Local(j.path), j.path, e.Tracks, orbits, site)
		if err != nil {
			e.SatErr = err.Error()
		}
		for _, m := range ms {
			e.Sats = append(e.Sats, satEntry{T: m.Streak, Name: m.Orbit.Name, NORAD: m.Orbit.NORAD, Intl: m.Orbit.IntlID,
				Offset: math.Round(m.Offset*10) / 10, Cross: math.Round(m.Cross*1000) / 1000, Km: math.Round(m.RangeKm),
				Alt: math.Round(m.Alt*10) / 10, Perigee: math.Round(m.Orbit.PerigeeKm()), Apogee: math.Round(m.Orbit.ApogeeKm()),
				Others: m.Others})
			if j.e.SatCat == "" {
				log.Printf("Приёмник: на кадре %s спутник %s (NORAD %d)", path.Base(j.path), m.Orbit.Name, m.Orbit.NORAD)
			}
			found++
		}
		e.SatCat, e.SatCatT = filepath.Base(j.cat.Path), j.cat.Time.UnixMilli()
		// the file may have been received again meanwhile
		if cur, ok := s.quality.get(j.path, e.Size, time.Unix(0, e.MTime)); ok && cur.TV == e.TV {
			s.quality.set(j.path, e)
		}
		n++
		if n%20 == 0 {
			s.quality.save()
		}
	}
	if n > 0 {
		log.Printf("Приёмник: опознаны треки на кадрах: %d, спутников найдено: %d", n, found)
	}
	return n
}

// identifyFrame tells the objects of the tracks of a frame file.
func identifyFrame(local, rel string, tracks []trackEntry, orbits []*satellites.Orbit, site *satellites.Site) ([]satellites.Match, error) {
	f, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	h, err := fits.ReadHeader(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	fr, err := satellites.FrameFromHeader(h, rel, site)
	if err != nil {
		return nil, err
	}
	st, idx := streaksOf(tracks)
	ms := satellites.Identify(fr, st, orbits)
	for i := range ms {
		ms[i].Streak = idx[ms[i].Streak]
	}
	return ms, nil
}

// frameStart: the start of the exposure of a frame, unix milliseconds; 0
// when the header does not tell.
func frameStart(h *fits.Header) int64 {
	v := strings.TrimSuffix(strings.TrimSpace(h.Get("DATE-OBS")), "Z")
	for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, v); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// satInfo is shown on the page: the catalogues of orbits.
type satInfo struct {
	Catalogs int       `json:"catalogs"`
	Newest   time.Time `json:"newest,omitempty"`
	Oldest   time.Time `json:"oldest,omitempty"`
	Error    string    `json:"error,omitempty"`
	// the orbits of the past from Space-Track
	History int    `json:"history"`
	STUser  string `json:"st_user,omitempty"`
	STSet   bool   `json:"st_set,omitempty"` // the password too
	HistErr string `json:"hist_err,omitempty"`
}

func (s *Server) satInfo() satInfo {
	all := s.sats.store.Catalogs()
	var c []satellites.CatalogFile
	hist := 0
	for _, x := range all {
		if x.History {
			hist++
		} else {
			c = append(c, x)
		}
	}
	s.sats.mu.Lock()
	defer s.sats.mu.Unlock()
	i := satInfo{Catalogs: len(c), Error: s.sats.err, History: hist,
		STUser: s.sats.stUser, STSet: s.sats.stUser != "" && s.sats.stPass != "", HistErr: s.sats.histErr}
	if len(c) > 0 {
		i.Newest, i.Oldest = c[len(c)-1].Time, c[0].Time
	}
	return i
}

// uiTrack: a track of a frame and what left it, for the page.
type uiTrack struct {
	trackEntry
	Sat   *satEntry   `json:"sat,omitempty"`
	Plane *planeEntry `json:"plane,omitempty"`
}

// uiTracks: the tracks of a measured frame for the page; told: the
// objects were looked for, age: days between the catalogue and the frame.
func uiTracks(e qualityEntry) (tr []uiTrack, told bool, age float64, err string) {
	for i, t := range e.Tracks {
		if t.Static {
			continue
		}
		u := uiTrack{trackEntry: t}
		for j := range e.Sats {
			if e.Sats[j].T == i {
				u.Sat = &e.Sats[j]
			}
		}
		for j := range e.Planes {
			if e.Planes[j].T == i && u.Sat == nil {
				u.Plane = &e.Planes[j]
			}
		}
		tr = append(tr, u)
	}
	if len(tr) == 0 {
		return nil, false, 0, ""
	}
	if e.SatCat != "" && e.Start != 0 {
		age = math.Round(math.Abs(float64(e.SatCatT-e.Start))/864e5*10) / 10
	}
	return tr, e.SatCat != "", age, e.SatErr
}

// SatCatalog is a catalogue of orbits kept here, as the sorters see it.
type SatCatalog struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"` // downloaded
}

// handleUISatCatalogs lists the catalogues of orbits kept, for the sorters
// (they tell the satellites of frames on their disk with them).
func (s *Server) handleUISatCatalogs(w http.ResponseWriter, r *http.Request) {
	out := []SatCatalog{}
	for _, c := range s.sats.store.Catalogs() {
		out = append(out, SatCatalog{Name: filepath.Base(c.Path), Time: c.Time})
	}
	writeJSON(w, out)
}

// handleUISatCatalog sends a catalogue of orbits by its name.
func (s *Server) handleUISatCatalog(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	for _, c := range s.sats.store.Catalogs() {
		if filepath.Base(c.Path) != name {
			continue
		}
		f, err := os.Open(c.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		http.ServeContent(w, r, name, st.ModTime(), f)
		return
	}
	http.Error(w, "нет такого каталога орбит", http.StatusNotFound)
}
