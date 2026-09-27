package receiver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// CleanupSettings: files already received and checked here are deleted on
// the ASIAIR once they are older than Days.
type CleanupSettings struct {
	Enabled bool      `json:"enabled"`
	Days    int       `json:"days"`
	LastRun time.Time `json:"last_run,omitempty"`
	LastN   int       `json:"last_n,omitempty"` // files queued by the last run
}

// ScheduleSettings: between From and To (receiver local time, "22:00",
// "06:00"; the window may cross midnight) the transfer is limited to Rate
// bytes/s, or paused when Rate is 0.
type ScheduleSettings struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
	Rate    int64  `json:"rate"`
}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("время %q: нужно ЧЧ:ММ, например 22:00", s)
	}
	return hh*60 + mm, nil
}

// Active reports whether the window includes t.
func (s ScheduleSettings) Active(t time.Time) bool {
	if !s.Enabled {
		return false
	}
	from, err1 := parseClock(s.From)
	to, err2 := parseClock(s.To)
	if err1 != nil || err2 != nil || from == to {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to // across midnight
}

// Text describes the window for people.
func (s ScheduleSettings) Text() string {
	what := "пауза"
	if s.Rate > 0 {
		what = "не быстрее " + config.FormatRate(s.Rate)
	}
	return fmt.Sprintf("с %s до %s — %s", s.From, s.To, what)
}

// Transfer orders.
const (
	OrderOldest = protocol.OrderOldest // oldest files first (as before)
	OrderNewest = protocol.OrderNewest // the last night first
	OrderLights = protocol.OrderLights // Light frames first, then calibration frames
)

// ---------- state ----------

func (s *State) SetCleanup(c CleanupSettings) error {
	if c.Days < 1 || c.Days > 3650 {
		return fmt.Errorf("срок должен быть от 1 до 3650 дней")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.LastRun, c.LastN = s.p.Cleanup.LastRun, s.p.Cleanup.LastN
	s.p.Cleanup = c
	return s.save()
}

func (s *State) SetSchedule(c ScheduleSettings) error {
	if _, err := parseClock(c.From); err != nil {
		return err
	}
	if _, err := parseClock(c.To); err != nil {
		return err
	}
	if c.From == c.To {
		return fmt.Errorf("начало и конец совпадают")
	}
	if c.Rate < 0 {
		c.Rate = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Schedule = c
	s.bump()
	return s.save()
}

func (s *State) SetOrder(o string) error {
	switch o {
	case OrderOldest, OrderNewest, OrderLights:
	default:
		return fmt.Errorf("неизвестный порядок %q", o)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Order = o
	s.bump()
	return s.save()
}

// Bump wakes the held sync requests (the schedule window opened or closed).
func (s *State) Bump() {
	s.mu.Lock()
	s.bump()
	s.mu.Unlock()
}

// ObservatoryZone is the time zone of the observatory computer, as its
// agent reports it; the receiver's own zone until an agent 1.15+ reports.
func ObservatoryZone(st protocol.AgentStatus) (*time.Location, bool) {
	if st.TZOffset == nil {
		return time.Local, false
	}
	return time.FixedZone(st.TZName, *st.TZOffset), true
}

// scheduleActive: the window is in the observatory's time.
func scheduleActive(p *persisted, now time.Time) bool {
	loc, _ := ObservatoryZone(p.Status)
	return p.Schedule.Active(now.In(loc))
}

// applySchedule puts the schedule into a sync response. Caller holds s.mu.
func (s *State) applySchedule(resp *protocol.SyncResponse, now time.Time) {
	sc := s.p.Schedule
	if !scheduleActive(&s.p, now) {
		return
	}
	resp.Schedule = sc.Text()
	if sc.Rate <= 0 {
		resp.Paused = true
		return
	}
	if resp.MaxRate == nil || *resp.MaxRate <= 0 || *resp.MaxRate > sc.Rate {
		r := sc.Rate
		resp.MaxRate = &r
	}
}

// ---------- background work ----------

// automation wakes the agent when the schedule window opens or closes and
// runs the ASIAIR cleanup.
func (s *Server) automation(ctx context.Context) {
	first := s.state.Snapshot()
	active := scheduleActive(&first, time.Now())
	lastCleanup := time.Time{}
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		now := time.Now()
		snap := s.state.Snapshot()
		sc := snap.Schedule
		if a := scheduleActive(&snap, now); a != active {
			active = a
			if a {
				log.Printf("Приёмник: расписание: %s", sc.Text())
			} else {
				log.Printf("Приёмник: расписание: окно закончилось")
			}
			s.state.Bump()
		}
		if now.Sub(lastCleanup) >= 10*time.Minute {
			lastCleanup = now
			s.runCleanup(now)
			if !s.state.Snapshot().Site.Set { // the Moon on the page needs the coordinates
				if site, err := s.detectSite(); err == nil && s.state.SetSite(site) == nil {
					log.Printf("Приёмник: координаты обсерватории из снимков: %s", fmtSite(site))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.cleanupNow:
			lastCleanup = time.Time{}
		}
	}
}

// cleanupBatch limits the files queued at once.
const cleanupBatch = 1000

// cleanupCandidates: files on the ASIAIR that are here with the same size
// (received and checked), older than days and not being deleted already.
func cleanupCandidates(local []LocalFile, inv []protocol.RemoteFile, cmds []*Command, days int, now time.Time) []string {
	have := make(map[string]int64, len(local))
	for _, l := range local {
		have[l.Path] = l.Size
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var out []string
	for _, rf := range inv {
		if size, ok := have[rf.Path]; !ok || size != rf.Size || !rf.ModTime.Before(cutoff) {
			continue
		}
		pending := false
		for _, c := range cmds {
			if c.Status == StatusPending && c.Covers(rf.Path) {
				pending = true
				break
			}
		}
		if !pending {
			out = append(out, rf.Path)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) runCleanup(now time.Time) {
	snap := s.state.Snapshot()
	c := snap.Cleanup
	if !c.Enabled || c.Days < 1 || len(snap.Inventory) == 0 {
		return
	}
	shares := map[string]bool{}
	for _, f := range snap.Inventory {
		if sh, _, ok := strings.Cut(f.Path, "/"); ok {
			shares[sh] = true
		}
	}
	var list []string
	for sh := range shares {
		list = append(list, sh)
	}
	local, err := s.getStore().List(list)
	if err != nil {
		log.Printf("Приёмник: автоочистка ASIAIR: %v", err)
		return
	}
	paths := cleanupCandidates(local, snap.Inventory, snap.Commands, c.Days, now)
	if len(paths) > cleanupBatch {
		paths = paths[:cleanupBatch]
	}
	n := 0
	if len(paths) > 0 {
		if n, err = s.state.QueueDelete(paths, protocol.CmdDelete, false); err != nil {
			log.Printf("Приёмник: автоочистка ASIAIR: %v", err)
			return
		}
		log.Printf("Приёмник: автоочистка ASIAIR: на удаление %d файл(ов) старше %d дн., уже полученных сюда", n, c.Days)
	}
	s.state.mu.Lock()
	s.state.p.Cleanup.LastRun, s.state.p.Cleanup.LastN = now, n
	s.state.save()
	s.state.mu.Unlock()
}

// ---------- speed log ----------

// speedSample is one minute of the transfer.
type speedSample struct {
	T          time.Time `json:"t"`
	Speed      float64   `json:"speed"` // bytes/s, average over the minute
	Max        float64   `json:"max"`
	Rate       int64     `json:"rate"`                 // limit in effect at the end of the minute
	Active     bool      `json:"active"`               // files were being sent
	Bottleneck string    `json:"bottleneck,omitempty"` // from the agent diagnostics
	Streams    int       `json:"streams,omitempty"`
	n          int
}

const speedLogLen = 24 * 60

// speedLog keeps the last 24 hours of the agent speed, per minute.
type speedLog struct {
	mu      sync.Mutex
	samples []speedSample
}

func (l *speedLog) add(st protocol.AgentStatus, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := now.Truncate(time.Minute)
	if n := len(l.samples); n == 0 || !l.samples[n-1].T.Equal(m) {
		l.samples = append(l.samples, speedSample{T: m})
		if len(l.samples) > speedLogLen {
			l.samples = append(l.samples[:0:0], l.samples[len(l.samples)-speedLogLen:]...)
		}
	}
	s := &l.samples[len(l.samples)-1]
	active := st.Uploading != "" || len(st.Uploads) > 0
	s.n++
	s.Speed += (st.Speed - s.Speed) / float64(s.n)
	s.Max = max(s.Max, st.Speed)
	s.Rate = st.Rate
	s.Active = s.Active || active
	s.Streams = st.Streams
	if st.Diag != nil && st.Diag.Bottleneck != "" {
		s.Bottleneck = st.Diag.Bottleneck
	}
}

func (l *speedLog) list(since time.Time) []speedSample {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []speedSample
	for _, s := range l.samples {
		if !s.T.Before(since) {
			out = append(out, s)
		}
	}
	return out
}

// ---------- web page ----------

func (s *Server) handleUISpeed(w http.ResponseWriter, r *http.Request) {
	hours := 6
	if h, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && h >= 1 && h <= 24 {
		hours = h
	}
	writeJSON(w, s.speed.list(time.Now().Add(-time.Duration(hours)*time.Hour)))
}

func (s *Server) handleUICleanup(w http.ResponseWriter, r *http.Request) {
	var req CleanupSettings
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.state.SetCleanup(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Enabled {
		log.Printf("Приёмник: автоочистка ASIAIR включена: файлы старше %d дн., уже полученные сюда", req.Days)
		select {
		case s.cleanupNow <- struct{}{}:
		default:
		}
	} else {
		log.Printf("Приёмник: автоочистка ASIAIR выключена")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUISchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		From    string `json:"from"`
		To      string `json:"to"`
		Rate    string `json:"rate"` // "2MB/s"; "0" = pause
	}
	if !readJSON(w, r, &req) {
		return
	}
	bps, err := config.ParseRate(req.Rate)
	if err == nil {
		err = s.state.SetSchedule(ScheduleSettings{Enabled: req.Enabled, From: req.From, To: req.To, Rate: bps})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sc := s.state.Snapshot().Schedule
	if sc.Enabled {
		log.Printf("Приёмник: расписание передачи: %s", sc.Text())
	} else {
		log.Printf("Приёмник: расписание передачи выключено")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUIOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Order string `json:"order"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.state.SetOrder(req.Order); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// obsTZ tells the page the time zone of the observatory.
type obsTZ struct {
	Known  bool   `json:"known"`  // reported by the agent
	Offset int    `json:"offset"` // seconds east of UTC
	Name   string `json:"name,omitempty"`
	Text   string `json:"text"` // "UTC+3 (MSK)"
}

func newObsTZ(st protocol.AgentStatus) obsTZ {
	loc, known := ObservatoryZone(st)
	name, off := time.Now().In(loc).Zone()
	t := "UTC"
	if off != 0 {
		sign := "+"
		if off < 0 {
			sign, off = "-", -off
		}
		t += sign + strconv.Itoa(off/3600)
		if m := off % 3600 / 60; m != 0 {
			t += fmt.Sprintf(":%02d", m)
		}
		if sign == "-" {
			off = -off
		}
	}
	if name != "" && !strings.HasPrefix(name, "+") && !strings.HasPrefix(name, "-") && name != "UTC" {
		t += " (" + name + ")"
	}
	return obsTZ{Known: known, Offset: off, Name: name, Text: t}
}
