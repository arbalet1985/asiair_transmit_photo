package sorter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// QualityCache keeps the stars measured on frames on disk, so a frame is
// measured once. Entries are keyed by path, size and time of the file.
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

// Measurement is the stars of one frame.
type Measurement struct {
	Frame *Frame
	HFR   float64
	Stars int
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
	k := qualityKey(p, st)
	if cache != nil {
		cache.mu.Lock()
		e, ok := cache.m[k]
		cache.mu.Unlock()
		if ok && e.V == qualityVersion {
			return Measurement{f, e.HFR, e.Stars}, true
		}
	}
	r, err := stars.MeasureFile(p)
	if err != nil {
		return Measurement{}, false
	}
	if cache != nil {
		cache.mu.Lock()
		cache.m[k] = qualityEntry{r.HFR, r.Stars, qualityVersion}
		cache.mu.Unlock()
	}
	return Measurement{f, r.HFR, r.Stars}, true
}
