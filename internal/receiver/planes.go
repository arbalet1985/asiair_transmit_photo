package receiver

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/aircraft"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// planeEntry: the plane that left track T.
type planeEntry struct {
	T        int     `json:"t"`
	ICAO     string  `json:"icao"`
	Callsign string  `json:"call,omitempty"`
	Km       float64 `json:"km"`   // from the observatory
	Alt      float64 `json:"alt"`  // degrees above the horizon
	AltM     float64 `json:"altm"` // its height, m
	Cross    float64 `json:"cross"`
	Others   int     `json:"others,omitempty"`
}

const (
	planePoll    = 25 * time.Second // ~2500 credits of the 4000 a day in a long winter night
	planeSunAlt  = -6.0             // recorded while the Sun is lower at the observatory
	planeBox     = 1.5              // degrees of latitude each way (a plane at 10 km is 3° up there)
	planeKeep    = 60               // days of recording kept
	planeCovered = 90 * time.Second // the queries at most this apart around a frame
	planeWait    = 2 * time.Hour    // a frame not covered by the recording by then is not told
)

// planeState: the API client of OpenSky and the recording.
type planeState struct {
	mu       sync.Mutex
	id       string
	secret   string
	client   *aircraft.Client
	log      *aircraft.Log
	err      string
	last     time.Time // the last query answered
	lastN    int       // planes in it
	retry    time.Time // the credits are spent until then
	wake     chan struct{}
	notified bool
}

func newPlaneState(dir, id, secret string) *planeState {
	return &planeState{id: id, secret: secret, log: &aircraft.Log{Dir: dir, Keep: planeKeep}, wake: make(chan struct{}, 1)}
}

func (p *planeState) set() bool { return p.id != "" && p.secret != "" }

// planeWorker records the planes near the observatory while it is dark there.
func (s *Server) planeWorker(ctx context.Context) {
	p := s.planes
	lastPrune := time.Time{}
	for {
		s.pollPlanes(ctx)
		if time.Since(lastPrune) > 24*time.Hour {
			p.log.Prune(time.Now())
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-time.After(planePoll):
		}
	}
}

// recording: why the planes are not recorded now ("" when they are).
func (s *Server) planesIdle(now time.Time) string {
	p := s.planes
	if !p.set() {
		return "не задан API-клиент"
	}
	site := s.state.Snapshot().Site
	if !site.Set {
		return "не заданы координаты обсерватории"
	}
	if moon.Sun(now, site.Lat, site.Lon).Alt > planeSunAlt {
		return "днём не записываются"
	}
	if now.Before(p.retry) {
		return "кредиты OpenSky на сегодня израсходованы"
	}
	return ""
}

func (s *Server) pollPlanes(ctx context.Context) {
	p := s.planes
	now := time.Now()
	p.mu.Lock()
	idle := s.planesIdle(now)
	if idle == "" && (p.client == nil || p.client.ID != p.id || p.client.Secret != p.secret) {
		p.client = &aircraft.Client{ID: p.id, Secret: p.secret, UserAgent: "asiair-receiver/" + protocol.Version}
	}
	c := p.client
	p.mu.Unlock()
	if idle != "" {
		return
	}
	site := s.state.Snapshot().Site
	qctx, cancel := context.WithTimeout(ctx, time.Minute)
	states, at, err := c.States(qctx, aircraft.Around(site.Lat, site.Lon, planeBox))
	cancel()
	if err == nil {
		if at.IsZero() || at.Unix() <= 0 {
			at = now
		}
		err = p.log.Add(at, states)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		var rl aircraft.RateLimited
		if errors.As(err, &rl) {
			p.retry = now.Add(rl.Retry)
		}
		if p.err != err.Error() {
			log.Printf("Приёмник: самолёты не записаны: %v", err)
		}
		p.err = err.Error()
		if errors.Is(err, aircraft.ErrAuth) {
			p.retry = now.Add(time.Hour) // not every 25 seconds
		}
		return
	}
	if p.err != "" || !p.notified {
		log.Printf("Приёмник: идёт запись самолётов возле обсерватории (OpenSky), сейчас рядом: %d", len(states))
		p.notified = true
	}
	p.err, p.last, p.lastN = "", now, len(states)
}

// tellPlanes tells the planes of the tracks no satellite was found for,
// on the frames the recording covers. It returns how many frames it told.
func (s *Server) tellPlanes(ctx context.Context) int {
	p := s.planes
	p.mu.Lock()
	set := p.set()
	p.mu.Unlock()
	if !set {
		return 0
	}
	type job struct {
		path string
		e    qualityEntry
	}
	var jobs []job
	s.quality.mu.Lock()
	for k, e := range s.quality.m {
		if e.PlanesDone || e.Start == 0 || e.SatCat == "" {
			continue
		}
		if len(untold(e)) > 0 {
			jobs = append(jobs, job{k, e})
		}
	}
	s.quality.mu.Unlock()
	store := s.getStore()
	snap := s.state.Snapshot()
	var site *satellites.Site
	if snap.Site.Set {
		site = &satellites.Site{Lat: snap.Site.Lat, Lon: snap.Site.Lon}
	}
	n := 0
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		fr, err := frameOf(store.Local(j.path), j.path, site)
		e := j.e
		if err == nil {
			idx := untold(e)
			var st []satellites.Streak
			for _, i := range idx {
				t := e.Tracks[i]
				st = append(st, satellites.Streak{X0: t.X0, Y0: t.Y0, X1: t.X1, Y1: t.Y1, Edge0: t.E0, Edge1: t.E1})
			}
			slack := 20 * time.Second
			planes, covered, lerr := p.log.Window(fr.Start.Add(-slack), fr.Start.Add(fr.Exposure+slack), planeCovered)
			if lerr != nil || !covered && time.Since(fr.Start) < planeWait {
				continue // later
			}
			e.Planes = nil
			for _, m := range satellites.IdentifyPlanes(fr, st, planes) {
				pe := planeEntry{T: idx[m.Streak], ICAO: m.Plane.ICAO, Callsign: m.Plane.Callsign, Km: math.Round(m.RangeKm*10) / 10,
					Alt: math.Round(m.Alt*10) / 10, AltM: math.Round(m.AltM), Cross: math.Round(m.Cross*1000) / 1000, Others: m.Others}
				e.Planes = append(e.Planes, pe)
				log.Printf("Приёмник: на кадре %s самолёт %s (%s)", path.Base(j.path), strings.TrimSpace(pe.Callsign+" "), pe.ICAO)
			}
		}
		e.PlanesDone = true
		if cur, ok := s.quality.get(j.path, e.Size, time.Unix(0, e.MTime)); ok && cur.TV == e.TV {
			s.quality.set(j.path, e)
		}
		n++
	}
	return n
}

// untold: the moving tracks of a frame no satellite was found for.
func untold(e qualityEntry) []int {
	var out []int
	for i, t := range e.Tracks {
		if t.Static {
			continue
		}
		told := false
		for _, s := range e.Sats {
			told = told || s.T == i
		}
		if !told {
			out = append(out, i)
		}
	}
	return out
}

// frameOf reads what is needed of a frame file to tell its tracks.
func frameOf(local, rel string, site *satellites.Site) (satellites.Frame, error) {
	f, err := os.Open(local)
	if err != nil {
		return satellites.Frame{}, err
	}
	h, err := fits.ReadHeader(f)
	f.Close()
	if err != nil {
		return satellites.Frame{}, err
	}
	return satellites.FrameFromHeader(h, rel, site)
}

// planeInfo is shown on the page.
type planeInfo struct {
	ID    string    `json:"id,omitempty"`
	Set   bool      `json:"set,omitempty"` // the secret too
	Idle  string    `json:"idle,omitempty"`
	Last  time.Time `json:"last,omitempty"`
	LastN int       `json:"last_n,omitempty"`
	Error string    `json:"error,omitempty"`
}

func (s *Server) planeInfo() planeInfo {
	p := s.planes
	p.mu.Lock()
	defer p.mu.Unlock()
	return planeInfo{ID: p.id, Set: p.set(), Idle: s.planesIdle(time.Now()), Last: p.last, LastN: p.lastN, Error: p.err}
}

// handleUIOpenSky saves the API client of OpenSky (only on the computer of
// the receiver).
func (s *Server) handleUIOpenSky(w http.ResponseWriter, r *http.Request) {
	if isRemote(r) {
		http.Error(w, "доступно только на компьютере с приёмником", http.StatusForbidden)
		return
	}
	var req struct {
		ID     string  `json:"id"`
		Secret *string `json:"secret"` // nil: kept
	}
	if !readJSON(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	values := map[string]string{"opensky_id": id}
	if req.Secret != nil {
		values["opensky_secret"] = strings.TrimSpace(*req.Secret)
	}
	if id == "" {
		values["opensky_secret"] = ""
	}
	if s.cfg.Path != "" {
		if err := config.UpdateReceiverFile(s.cfg.Path, values); err != nil {
			http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	p := s.planes
	p.mu.Lock()
	p.id = id
	if v, ok := values["opensky_secret"]; ok {
		p.secret = v
	}
	p.err, p.retry, p.notified = "", time.Time{}, false
	set := p.set()
	p.mu.Unlock()
	if set {
		log.Printf("Приёмник: задан API-клиент OpenSky (%s) — ночью записываются самолёты возле обсерватории", id)
		select {
		case p.wake <- struct{}{}:
		default:
		}
	} else {
		log.Printf("Приёмник: API-клиент OpenSky удалён")
	}
	writeJSON(w, s.planeInfo())
}
