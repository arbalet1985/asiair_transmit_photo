package receiver

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// qualityEntry is the measurement of one received Light frame.
type qualityEntry struct {
	Size  int64   `json:"size"`
	MTime int64   `json:"mtime"` // unix nanoseconds of the local file
	HFR   float64 `json:"hfr,omitempty"`
	Stars int     `json:"stars"`
	Err   string  `json:"err,omitempty"`
	// Sky: where the frame was taken, from its header (for the planner);
	// NoSky: the header was read and tells nothing of it
	Sky   *skyEntry `json:"sky,omitempty"`
	NoSky bool      `json:"nosky,omitempty"`
	// V: the version of the measurement; an older one is measured again
	V int `json:"v,omitempty"`
}

// qualityVersion: 2 takes the background of each star around it, 3 tells
// saturated stars as Siril does (counted, but not for the HFR).
const qualityVersion = 3

// quality keeps the star measurements of the received Light frames in
// quality.json in the data folder. A background worker measures new frames
// one at a time.
type quality struct {
	mu    sync.Mutex
	file  string
	m     map[string]qualityEntry
	dirty bool
	wake  chan struct{}
}

func newQuality(file string) *quality {
	q := &quality{file: file, m: map[string]qualityEntry{}, wake: make(chan struct{}, 1)}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &q.m); err != nil {
			log.Printf("Приёмник: оценки кадров %s не прочитаны: %v", file, err)
			q.m = map[string]qualityEntry{}
		}
	}
	return q
}

// get returns the measurement of the file if it is up to date.
func (q *quality) get(p string, size int64, mtime time.Time) (qualityEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.m[p]
	return e, ok && e.Size == size && e.MTime == mtime.UnixNano()
}

func (q *quality) set(p string, e qualityEntry) {
	q.mu.Lock()
	q.m[p] = e
	q.dirty = true
	q.mu.Unlock()
}

// skies: the frames with where they were taken.
func (q *quality) skies() []skyEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]skyEntry, 0, len(q.m))
	for _, e := range q.m {
		if e.Sky != nil {
			out = append(out, *e.Sky)
		}
	}
	return out
}

func (q *quality) save() {
	q.mu.Lock()
	if !q.dirty {
		q.mu.Unlock()
		return
	}
	b, err := json.Marshal(q.m)
	q.dirty = false
	q.mu.Unlock()
	if err == nil {
		tmp := q.file + ".tmp"
		if err = os.WriteFile(tmp, b, 0o644); err == nil {
			err = os.Rename(tmp, q.file)
		}
	}
	if err != nil {
		log.Printf("Приёмник: оценки кадров не сохранены: %v", err)
	}
}

// forget drops the files that are no longer here.
func (q *quality) forget(keep map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for p := range q.m {
		if !keep[p] {
			delete(q.m, p)
			q.dirty = true
		}
	}
}

// poke wakes the worker (a file was received).
func (q *quality) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// measurable: Light frames in FITS.
func measurable(p string) bool {
	n := strings.ToLower(p)
	return (strings.HasSuffix(n, ".fit") || strings.HasSuffix(n, ".fits")) && protocol.FrameKind(p) == "light"
}

// qualityWorker measures the received Light frames not measured yet, newest
// first, so the last night is ready soonest.
func (s *Server) qualityWorker(ctx context.Context) {
	q := s.quality
	defer q.save()
	for {
		n := s.measurePending(ctx)
		if n > 0 {
			q.save()
		}
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-time.After(5 * time.Minute):
		}
	}
}

// measurePending measures what is missing; it returns how many it measured.
func (s *Server) measurePending(ctx context.Context) int {
	store := s.getStore()
	shares := map[string]bool{"EMMC Images": true, "Udisk": true}
	for _, f := range s.state.Snapshot().Inventory {
		if sh, _, ok := strings.Cut(f.Path, "/"); ok {
			shares[sh] = true
		}
	}
	var list []string
	for sh := range shares {
		list = append(list, sh)
	}
	local, err := store.List(list)
	if err != nil {
		return 0
	}
	keep := map[string]bool{}
	var todo, hdrTodo []LocalFile
	for _, f := range local {
		if !measurable(f.Path) {
			continue
		}
		keep[f.Path] = true
		if e, ok := s.quality.get(f.Path, f.Size, f.ModTime); !ok || e.V < qualityVersion {
			todo = append(todo, f) // new, or measured the old way (shown until then)
		} else if e.Sky == nil && !e.NoSky {
			hdrTodo = append(hdrTodo, f) // measured by an older version
		}
	}
	s.quality.forget(keep)
	// newest first
	sort.Slice(todo, func(i, j int) bool { return todo[i].ModTime.After(todo[j].ModTime) })
	n := 0
	for _, f := range todo {
		if ctx.Err() != nil {
			break
		}
		r, err := stars.MeasureFile(store.Local(f.Path))
		e := qualityEntry{Size: f.Size, MTime: f.ModTime.UnixNano(), HFR: r.HFR, Stars: r.Stars, V: qualityVersion}
		if err != nil {
			e.Err = err.Error()
		}
		e.Sky, e.NoSky = readSky(store.Local(f.Path), f.Path)
		s.quality.set(f.Path, e)
		n++
		if n%20 == 0 {
			s.quality.save()
		}
		select { // let the transfer and the page have the disk and CPU too
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	if n > 0 {
		log.Printf("Приёмник: оценено кадров (звёзды, HFR): %d", n)
	}
	// the headers only, of the frames measured before the planner
	for i, f := range hdrTodo {
		if ctx.Err() != nil {
			break
		}
		if e, ok := s.quality.get(f.Path, f.Size, f.ModTime); ok {
			e.Sky, e.NoSky = readSky(store.Local(f.Path), f.Path)
			s.quality.set(f.Path, e)
		}
		if i%200 == 199 {
			s.quality.save()
		}
	}
	return n + len(hdrTodo)
}
