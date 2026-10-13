package sorter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// QualityCache keeps the stars and the tracks found on frames on disk (and
// the satellites told for them), so a frame is measured once. Entries are
// keyed by path, size and time of the file.
type QualityCache struct {
	mu   sync.Mutex
	file string
	m    map[string]qualityEntry
}

type qualityEntry struct {
	HFR   float64 `json:"hfr,omitempty"`
	Stars int     `json:"stars,omitempty"`
	// V: the version of the measurement; an older one is measured again
	V int `json:"v,omitempty"`
	// Tracks: the straight tracks on the frame found by version TV of the
	// search, Start: the start of the exposure (unix ms), Flip: the rows of
	// the file go bottom up; the objects that left them were told with the
	// catalogue of orbits SatCat downloaded at SatCatT (unix ms)
	Tracks  []Track `json:"tracks,omitempty"`
	TV      int     `json:"tv,omitempty"`
	Start   int64   `json:"start,omitempty"`
	Flip    bool    `json:"flip,omitempty"`
	SatCat  string  `json:"satcat,omitempty"`
	SatCatT int64   `json:"satcatt,omitempty"`
	SatErr  string  `json:"saterr,omitempty"`
}

// qualityVersion: 1 tells saturated stars as Siril does (counted, but not
// for the HFR).
const qualityVersion = 1

// LoadQualityCache reads the cache file ("" = keep in memory only).
func LoadQualityCache(file string) *QualityCache {
	c := &QualityCache{file: file, m: map[string]qualityEntry{}}
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			json.Unmarshal(b, &c.m)
		}
	}
	return c
}

func qualityKey(p string, st os.FileInfo) string {
	return fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().Unix())
}

// Save writes the cache.
func (c *QualityCache) Save() error {
	if c.file == "" {
		return nil
	}
	c.mu.Lock()
	b, err := json.Marshal(c.m)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.file), 0o755); err != nil {
		return err
	}
	tmp := c.file + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.file)
}

// Measurement is the stars and the tracks of one frame.
type Measurement struct {
	Frame  *Frame
	HFR    float64
	Stars  int
	Tracks []Track // with the static ones
	Flip   bool

	path, key      string // the file read and its key in the cache
	start          int64
	satCat, satErr string
	satCatT        int64
}

func (m Measurement) entry() qualityEntry {
	return qualityEntry{HFR: m.HFR, Stars: m.Stars, V: qualityVersion, Tracks: m.Tracks, TV: trackVersion,
		Start: m.start, Flip: m.Flip, SatCat: m.satCat, SatCatT: m.satCatT, SatErr: m.satErr}
}

func (m *Measurement) fromEntry(e qualityEntry) {
	m.HFR, m.Stars, m.Tracks, m.Flip = e.HFR, e.Stars, e.Tracks, e.Flip
	m.start, m.satCat, m.satCatT, m.satErr = e.Start, e.SatCat, e.SatCatT, e.SatErr
}

// moving: some track is not fixed in the sky.
func (m Measurement) moving() bool {
	for _, t := range m.Tracks {
		if !t.Static {
			return true
		}
	}
	return false
}

// Apply gives the frame its measurement (on the window's thread).
func (m Measurement) Apply() {
	f := m.Frame
	f.Measured, f.HFR, f.Stars = true, m.HFR, m.Stars
	f.Tracks = nil
	for _, t := range m.Tracks {
		if !t.Static {
			f.Tracks = append(f.Tracks, t)
		}
	}
	f.SatTold, f.SatErr, f.FlipRows, f.SatAge = m.satCat != "", m.satErr, m.Flip, 0
	if m.satCat != "" && m.start != 0 {
		f.SatAge = math.Round(math.Abs(float64(m.satCatT-m.start))/864e5*10) / 10
	}
}

// put keeps a measurement in the cache.
func (c *QualityCache) put(m Measurement) {
	if c == nil || m.key == "" {
		return
	}
	c.mu.Lock()
	c.m[m.key] = m.entry()
	c.mu.Unlock()
}

// ToMeasure lists the light frames of the layout without stars measured
// that can be read on disk: a file of the input, or a frame of a receiver
// already in the output (out). The path to read goes with each.
func (l *Layout) ToMeasure(out string) (frames []*Frame, paths []string) {
	for _, it := range l.Items {
		f := it.Frame
		if it.Dup || f.Kind != Light || f.Measured {
			continue
		}
		p := f.Path
		if f.Src != nil {
			p = ""
			cands := []string{it.Existing}
			if out != "" {
				cands = append(cands, filepath.Join(out, filepath.FromSlash(it.Dest)))
			}
			for _, c := range cands {
				if st, err := os.Stat(c); c != "" && err == nil && st.Size() == f.Size {
					p = c
					break
				}
			}
			if p == "" {
				continue
			}
		}
		frames = append(frames, f)
		paths = append(paths, p)
	}
	return frames, paths
}

// MeasureFrames measures the stars of the frames read from paths, several
// at a time, the ones in the cache without reading. got is called for each
// frame measured (from the measuring goroutines). The frames themselves are
// not changed: the caller applies the measurements.
func MeasureFrames(ctx context.Context, frames []*Frame, paths []string, cache *QualityCache, got func(Measurement)) {
	work := make(chan int)
	var wg sync.WaitGroup
	n := max(1, runtime.NumCPU()/2)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				m, ok := measureOne(frames[i], paths[i], cache)
				if ok {
					got(m)
				}
			}
		}()
	}
	for i := range frames {
		select {
		case work <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
}

func measureOne(f *Frame, p string, cache *QualityCache) (Measurement, bool) {
	st, err := os.Stat(p)
	if err != nil {
		return Measurement{}, false
	}
	m := Measurement{Frame: f, path: p, key: qualityKey(p, st)}
	if cache != nil {
		cache.mu.Lock()
		e, ok := cache.m[m.key]
		cache.mu.Unlock()
		if ok && e.V == qualityVersion && e.TV == trackVersion {
			m.fromEntry(e)
			return m, true
		}
	}
	e, err := measureFile(p)
	if err != nil {
		return Measurement{}, false
	}
	m.fromEntry(e)
	cache.put(m)
	return m, true
}

// measureFile finds the stars and the tracks of a frame, reading it once.
func measureFile(p string) (qualityEntry, error) {
	fh, err := os.Open(p)
	if err != nil {
		return qualityEntry{}, err
	}
	defer fh.Close()
	img, err := fits.Preview(fh, stars.MaxWidth)
	if err != nil {
		return qualityEntry{}, err
	}
	r := stars.Measure(img)
	e := qualityEntry{HFR: r.HFR, Stars: r.Stars, V: qualityVersion, TV: trackVersion}
	w, _ := fits.ReadWCS(img.Header, filepath.Base(p))
	e.Tracks = tracksOf(satellites.FindStreaks(img), w)
	if len(e.Tracks) > 0 {
		e.Start = frameStart(img.Header)
		e.Flip = strings.EqualFold(img.Header.Get("ROWORDER"), "BOTTOM-UP")
	}
	return e, nil
}
