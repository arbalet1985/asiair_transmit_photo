package receiver

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// The tilt of each camera over many frames (stars.SummarizeTilt): a
// background job measures the tilt of up to tiltPerCamera frames of each
// optical train, spread over the nights, and sums them up.

const (
	tiltPerCamera = 40
	tiltMinStars  = 80 // frames with fewer stars (clouds) are not taken
)

// cameraTilt: the summary of one camera with the lens it was on.
type cameraTilt struct {
	Camera  string            `json:"camera"`
	Focal   float64           `json:"focal,omitempty"` // mm
	Binning int               `json:"binning,omitempty"`
	Width   int               `json:"width"`
	Height  int               `json:"height"`
	Frames  int               `json:"frames"` // of this camera here
	Failed  int               `json:"failed,omitempty"`
	OK      bool              `json:"ok"`
	Summary stars.TiltSummary `json:"summary"`
	Bigger  string            `json:"bigger,omitempty"`
	First   time.Time         `json:"first"`
	Last    time.Time         `json:"last"`
}

// tiltJob: the job and its result.
type tiltJob struct {
	mu      sync.Mutex
	running bool
	done    int
	total   int
	result  []cameraTilt
	at      time.Time
	err     string
}

type tiltJobInfo struct {
	Running bool         `json:"running"`
	Done    int          `json:"done"`
	Total   int          `json:"total"`
	Result  []cameraTilt `json:"result"`
	At      time.Time    `json:"at,omitempty"`
	Error   string       `json:"error,omitempty"`
}

func (j *tiltJob) info() tiltJobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	return tiltJobInfo{Running: j.running, Done: j.done, Total: j.total, Result: append([]cameraTilt{}, j.result...), At: j.at, Error: j.err}
}

// handleUICameraTilt: GET the state of the job, POST starts it.
func (s *Server) handleUICameraTilt(w http.ResponseWriter, r *http.Request) {
	j := &s.camTilt
	if r.Method == http.MethodPost {
		var req struct{}
		if !readJSON(w, r, &req) {
			return
		}
		j.mu.Lock()
		start := !j.running
		if start {
			j.running, j.done, j.total, j.err = true, 0, 0, ""
		}
		j.mu.Unlock()
		if start {
			go s.cameraTilts(context.Background())
		}
	}
	writeJSON(w, j.info())
}

// tiltFrame: a frame that may be measured.
type tiltFrame struct {
	path, local string
	start       time.Time
}

// cameraTilts measures the tilts of the frames of each camera and sums
// them up.
func (s *Server) cameraTilts(ctx context.Context) {
	j := &s.camTilt
	defer func() {
		if p := recover(); p != nil {
			log.Printf("Приёмник: тилт камер прерван: %v", p)
			j.mu.Lock()
			j.running, j.err = false, fmt.Sprint(p)
			j.mu.Unlock()
		}
	}()
	store := s.getStore()
	// the Light frames measured here with enough stars, by camera
	type group struct {
		ct     cameraTilt
		frames []tiltFrame
	}
	groups := map[string]*group{}
	s.quality.mu.Lock()
	type cand struct {
		path string
		e    qualityEntry
	}
	var cands []cand
	for p, e := range s.quality.m {
		if e.Err == "" && e.Stars >= tiltMinStars && measurable(p) {
			cands = append(cands, cand{p, e})
		}
	}
	s.quality.mu.Unlock()
	for _, c := range cands {
		local := store.Local(c.path)
		f, err := os.Open(local)
		if err != nil {
			continue
		}
		h, err := fits.ReadHeader(f)
		f.Close()
		if err != nil {
			continue
		}
		cam := strings.TrimSpace(h.Get("INSTRUME"))
		if cam == "" {
			cam = "камера не указана"
		}
		num := func(k string) float64 {
			v, _ := strconv.ParseFloat(strings.Trim(h.Get(k), "' "), 64)
			return v
		}
		focal := math.Round(num("FOCALLEN"))
		bin := int(num("XBINNING"))
		w, hh := int(num("NAXIS1")), int(num("NAXIS2"))
		key := fmt.Sprintf("%s|%.0f|%d|%dx%d", cam, focal, bin, w, hh)
		g := groups[key]
		if g == nil {
			g = &group{ct: cameraTilt{Camera: cam, Focal: focal, Binning: bin, Width: w, Height: hh}}
			groups[key] = g
		}
		g.frames = append(g.frames, tiltFrame{c.path, local, time.UnixMilli(c.e.Start)})
	}
	// up to tiltPerCamera frames of each, spread evenly over time
	total := 0
	for _, g := range groups {
		sort.Slice(g.frames, func(a, b int) bool { return g.frames[a].start.Before(g.frames[b].start) })
		g.ct.Frames = len(g.frames)
		if n := len(g.frames); n > 0 {
			g.ct.First, g.ct.Last = g.frames[0].start, g.frames[n-1].start
		}
		if n := len(g.frames); n > tiltPerCamera {
			pick := make([]tiltFrame, tiltPerCamera)
			for i := range pick {
				pick[i] = g.frames[i*(n-1)/(tiltPerCamera-1)]
			}
			g.frames = pick
		}
		total += len(g.frames)
	}
	j.mu.Lock()
	j.total = total
	j.mu.Unlock()
	var out []cameraTilt
	for _, g := range groups {
		var ts []stars.Tilt
		for _, f := range g.frames {
			if ctx.Err() != nil {
				return
			}
			if t, err := s.tiltOf(f.path, f.local); err == nil {
				ts = append(ts, t)
			} else {
				g.ct.Failed++
			}
			j.mu.Lock()
			j.done++
			j.mu.Unlock()
			time.Sleep(100 * time.Millisecond) // the transfer and the page have the disk and CPU too
		}
		g.ct.Summary, g.ct.OK = stars.SummarizeTilt(ts)
		if g.ct.OK {
			g.ct.Bigger = g.ct.Summary.Bigger()
		}
		out = append(out, g.ct)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Frames > out[b].Frames })
	j.mu.Lock()
	j.running, j.result, j.at = false, out, time.Now()
	j.mu.Unlock()
	for _, c := range out {
		if c.OK {
			log.Printf("Приёмник: тилт камеры %s (%.0f мм): %.2f px HFR (%.0f%%) по %d кадрам из %d", c.Camera, c.Focal, c.Summary.Tilt, c.Summary.Percent, c.Summary.Used, c.Summary.Frames)
		}
	}
}

// tiltOf measures the tilt of a frame (or takes it from the cache).
func (s *Server) tiltOf(path, local string) (stars.Tilt, error) {
	st, err := os.Stat(local)
	if err != nil {
		return stars.Tilt{}, err
	}
	key := fmt.Sprintf("%s|%d|%d", local, st.Size(), st.ModTime().UnixNano())
	c := &s.tilts
	c.mu.Lock()
	t, ok := c.items[key]
	c.mu.Unlock()
	if ok {
		return t, nil
	}
	t, err = stars.TiltFile(local)
	if err != nil {
		return t, err
	}
	c.mu.Lock()
	if c.items == nil || len(c.items) >= tiltCacheMax {
		c.items = map[string]stars.Tilt{}
	}
	c.items[key] = t
	c.mu.Unlock()
	return t, nil
}
