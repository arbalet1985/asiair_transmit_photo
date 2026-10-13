package receiver

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
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
	// Bg: the median of the frame (the sky background, ADU); Round, Angle,
	// Aligned: the shape of the stars (stars.Result); Sat: the pixels at
	// the level of saturation, SatPct: their share of the frame, percent
	Bg      float64 `json:"bg,omitempty"`
	Round   float64 `json:"round,omitempty"`
	Angle   float64 `json:"angle,omitempty"`
	Aligned float64 `json:"aligned,omitempty"`
	Sat     int64   `json:"sat,omitempty"`
	SatPct  float64 `json:"satpct,omitempty"`
	// Tracks: the straight tracks on the frame (satellites, planes,
	// meteors) found by version TV of the search; Start: the start of the
	// exposure (unix ms); Flip: the rows of the file go bottom up
	Tracks []trackEntry `json:"tracks,omitempty"`
	TV     int          `json:"tv,omitempty"`
	Start  int64        `json:"start,omitempty"`
	Flip   bool         `json:"flip,omitempty"`
	// Sats: the objects that left the tracks, told with the catalogue of
	// orbits SatCat downloaded at SatCatT (unix ms); "": not told yet
	Sats    []satEntry `json:"sats,omitempty"`
	SatCat  string     `json:"satcat,omitempty"`
	SatCatT int64      `json:"satcatt,omitempty"`
	SatErr  string     `json:"saterr,omitempty"`
}

// qualityVersion: 2 takes the background of each star around it, 3 tells
// saturated stars as Siril does (counted, but not for the HFR), 4 adds the
// background, the shape of the stars, the saturated pixels and the time.
const qualityVersion = 4

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

// measureFrame finds the stars and the tracks of a frame file, reading it
// once.
func measureFrame(local string) qualityEntry {
	e := qualityEntry{V: qualityVersion, TV: trackVersion}
	f, err := os.Open(local)
	if err != nil {
		e.Err = err.Error()
		return e
	}
	defer f.Close()
	img, err := fits.Preview(f, stars.MaxWidth)
	if err != nil {
		e.Err = err.Error()
		return e
	}
	r := stars.Measure(img)
	e.HFR, e.Stars = r.HFR, r.Stars
	e.Round, e.Angle, e.Aligned = round3(r.Round), math.Round(r.Angle), round3(r.Aligned)
	e.Bg, e.Sat, e.SatPct = frameLevels(img.Stats)
	w, _ := fits.ReadWCS(img.Header, filepath.Base(local))
	e.Tracks = tracksOf(satellites.FindStreaks(img), w)
	e.Start = frameStart(img.Header)
	e.Flip = strings.EqualFold(img.Header.Get("ROWORDER"), "BOTTOM-UP")
	return e
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// frameLevels: the background of a frame (the mean of the medians of its
// channels), its saturated pixels and their share, percent.
func frameLevels(st []fits.ChannelStats) (bg float64, sat int64, pct float64) {
	var n int64
	for _, c := range st {
		if c.NoMedian {
			bg += c.Mean
		} else {
			bg += c.Median
		}
		sat += c.Saturated
		n += c.Count
	}
	if len(st) > 0 {
		bg = math.Round(bg / float64(len(st)))
	}
	if n > 0 {
		pct = math.Round(1e6*float64(sat)/float64(n)) / 1e4
	}
	return bg, sat, pct
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
		if e, ok := s.quality.get(f.Path, f.Size, f.ModTime); !ok || e.V < qualityVersion || e.TV < trackVersion {
			todo = append(todo, f) // new, or measured the old way (shown until then)
		} else if e.Sky == nil && !e.NoSky {
			hdrTodo = append(hdrTodo, f) // measured by an older version
		}
	}
	s.quality.forget(keep)
	// newest first
	sort.Slice(todo, func(i, j int) bool { return todo[i].ModTime.After(todo[j].ModTime) })
	n, tracks := 0, 0
	for _, f := range todo {
		if ctx.Err() != nil {
			break
		}
		e := measureFrame(store.Local(f.Path))
		e.Size, e.MTime = f.Size, f.ModTime.UnixNano()
		if old, ok := s.quality.get(f.Path, f.Size, f.ModTime); ok && old.TV == trackVersion && e.TV == trackVersion {
			// measured again for the rest: the tracks are the same, so
			// are the satellites told for them
			e.Tracks, e.Sats, e.SatCat, e.SatCatT, e.SatErr = old.Tracks, old.Sats, old.SatCat, old.SatCatT, old.SatErr
		}
		e.Sky, e.NoSky = readSky(store.Local(f.Path), f.Path)
		s.quality.set(f.Path, e)
		if len(e.Tracks) > 0 {
			tracks++
		}
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
		log.Printf("Приёмник: оценено кадров (звёзды, HFR, треки спутников): %d, с треками: %d", n, tracks)
	}
	if tracks > 0 && s.sats != nil {
		s.quality.save()
		s.sats.poke()
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
