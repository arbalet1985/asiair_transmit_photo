package fits

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Hot pixels are pixels of the sensor saturated with no light: they are not
// counted as saturated. They are told by the darks of the camera (bright in
// more than half of them) and, without darks, as a lone saturated pixel with
// no light around it (a hot pixel or a cosmic ray hit: a star spreads its
// light over its neighbours).

// HotPixels are the hot pixels of a sensor, found on its darks.
type HotPixels struct {
	Width, Height int
	Darks         int                // the darks they were found on
	idx           map[int32]struct{} // y*Width+x, rows in the order of the file
}

// Count: how many pixels are hot.
func (hp *HotPixels) Count() int {
	if hp == nil {
		return 0
	}
	return len(hp.idx)
}

// usable: the map is of a frame of this size.
func (hp *HotPixels) usable(w, h int) bool {
	return hp != nil && hp.Width == w && hp.Height == h && len(hp.idx) > 0
}

func (hp *HotPixels) has(x, y int) bool {
	_, ok := hp.idx[int32(y*hp.Width+x)]
	return ok
}

// hotLevel: a pixel of a dark at this share of saturation is hot: with the
// sky of a light frame on top it is saturated there.
const hotLevel = 0.5

// hotMaxShare: a "dark" with more bright pixels than this share is not one
// (a light or a flat named so).
const hotMaxShare = 0.005

// HotFromDarks finds the hot pixels of the darks: bright in more than half
// of them. Darks of another size than the first are left out.
func HotFromDarks(darks []io.ReaderAt, sizes []int64) (*HotPixels, error) {
	count := map[int32]int{}
	hp := &HotPixels{}
	var errs []string
	for i, f := range darks {
		w, h, idx, err := darkBright(f, sizes[i])
		if err == nil && hp.Darks > 0 && (w != hp.Width || h != hp.Height) {
			err = fmt.Errorf("размер %d×%d, а не %d×%d", w, h, hp.Width, hp.Height)
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		hp.Width, hp.Height = w, h
		hp.Darks++
		for _, k := range idx {
			count[k]++
		}
	}
	if hp.Darks == 0 {
		if len(errs) == 0 {
			return nil, errors.New("нет дарков")
		}
		return nil, errors.New(strings.Join(errs, "; "))
	}
	hp.idx = map[int32]struct{}{}
	for k, n := range count {
		if 2*n > hp.Darks {
			hp.idx[k] = struct{}{}
		}
	}
	return hp, nil
}

// darkBright: the pixels of a dark at hotLevel of saturation or above (the
// first plane).
func darkBright(f io.ReaderAt, size int64) (w, ht int, idx []int32, err error) {
	sr := io.NewSectionReader(f, 0, size)
	h, err := ReadHeader(sr)
	if err != nil {
		return 0, 0, nil, err
	}
	start, _ := sr.Seek(0, io.SeekCurrent)
	bitpix := h.int("BITPIX", 0)
	w, ht = h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	if h.int("NAXIS", 0) < 2 || w <= 0 || ht <= 0 {
		return 0, 0, nil, errors.New("в файле нет изображения")
	}
	bpp := map[int]int{8: 1, 16: 2, 32: 4, -32: 4, -64: 8}[bitpix]
	if bpp == 0 {
		return 0, 0, nil, fmt.Errorf("неподдерживаемый BITPIX=%d", bitpix)
	}
	bscale, bzero := h.float("BSCALE", 1), h.float("BZERO", 0)
	sat := saturation(h, bitpix, bscale, bzero)
	if sat <= 0 {
		return 0, 0, nil, errors.New("уровень насыщения неизвестен")
	}
	thr := hotLevel * sat
	limit := int(float64(w*ht)*hotMaxShare) + 1
	row := make([]byte, w*bpp)
	dec := make([]float64, w)
	for y := 0; y < ht; y++ {
		if _, err := f.ReadAt(row, start+int64(y)*int64(w*bpp)); err != nil {
			return 0, 0, nil, fmt.Errorf("файл обрезан: %w", err)
		}
		decodeRow(row, bitpix, bscale, bzero, dec)
		for x, v := range dec {
			if v >= thr {
				idx = append(idx, int32(y*w+x))
			}
		}
		if len(idx) > limit {
			return 0, 0, nil, errors.New("слишком много ярких пикселей — не дарк")
		}
	}
	return w, ht, idx, nil
}

// loneShare: a saturated pixel is lone when none of its 8 neighbours rises
// above the background around it by this share of the way to saturation.
const loneShare = 0.15

// lone tells a saturated sample with no light around it. at gives the
// value of the neighbour (dx, dy), false outside the frame.
func lone(at func(dx, dy int) (float64, bool), sat float64) bool {
	// the background: the median of the ring of 16 pixels two away
	var ring [16]float64
	n := 0
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if dx != -2 && dx != 2 && dy != -2 && dy != 2 {
				continue
			}
			if v, ok := at(dx, dy); ok {
				ring[n] = v
				n++
			}
		}
	}
	if n < 5 {
		return false
	}
	sort.Float64s(ring[:n])
	bg := ring[n/2]
	if bg >= sat {
		return false
	}
	lim := bg + loneShare*(sat-bg)
	seen := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			if v, ok := at(dx, dy); ok {
				if v >= lim {
					return false
				}
				seen++
			}
		}
	}
	return seen >= 3
}

// FrameTraits: what a dark shares with a frame to tell its hot pixels: key
// (the camera, gain, binning and size; "" without an image), and the
// exposure (s, 0 unknown) and the sensor temperature (°C, NaN unknown) to
// choose the closest darks.
func FrameTraits(h *Header) (key string, exp, temp float64) {
	w, ht := h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	if h.int("NAXIS", 0) < 2 || w <= 0 || ht <= 0 {
		return "", 0, math.NaN()
	}
	key = fmt.Sprintf("%s|%s|%d|%dx%d", strings.TrimSpace(h.Get("INSTRUME")), strings.TrimSpace(h.Get("GAIN")),
		h.int("XBINNING", 1), w, ht)
	exp = h.float("EXPTIME", h.float("EXPOSURE", 0))
	temp = h.float("CCD-TEMP", math.NaN())
	return key, exp, temp
}

// HotCache finds the hot pixels of frames on the darks on disk and keeps
// them: the darks closest to a frame are read once for all its frames. The
// zero value is ready to use.
type HotCache struct {
	mu       sync.Mutex
	darks    map[string]darkFile   // path → what its header tells
	maps     map[string]*HotPixels // the darks used (their paths) → hot pixels
	failed   map[string]bool
	building map[string]chan struct{}
}

type darkFile struct {
	size      int64
	mtime     time.Time
	key       string
	exp, temp float64
}

// hotDarks: the darks a map is made of at most.
const hotDarks = 8

// SetDarks tells the dark files there are now; the headers of the new or
// changed ones are read.
func (c *HotCache) SetDarks(paths []string) {
	c.mu.Lock()
	old := c.darks
	c.mu.Unlock()
	next := make(map[string]darkFile, len(paths))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if d, ok := old[p]; ok && d.size == st.Size() && d.mtime.Equal(st.ModTime()) {
			next[p] = d
			continue
		}
		d := darkFile{size: st.Size(), mtime: st.ModTime()}
		if f, err := os.Open(p); err == nil {
			if h, err := ReadHeader(f); err == nil {
				d.key, d.exp, d.temp = FrameTraits(h)
			}
			f.Close()
		}
		next[p] = d
	}
	c.mu.Lock()
	c.darks = next
	c.mu.Unlock()
}

// For returns the hot pixels for a frame with the header h, found on the
// darks closest to it; nil when no dark fits. wait: found now if not yet;
// else they are found in the background and pending is true meanwhile.
func (c *HotCache) For(h *Header, wait bool) (hp *HotPixels, pending bool) {
	if c == nil {
		return nil, false
	}
	key, exp, temp := FrameTraits(h)
	if key == "" {
		return nil, false
	}
	for {
		c.mu.Lock()
		pick := c.pickLocked(key, exp, temp)
		if len(pick) == 0 {
			c.mu.Unlock()
			return nil, false
		}
		id := strings.Join(pick, "\n")
		if hp, ok := c.maps[id]; ok {
			c.mu.Unlock()
			return hp, false
		}
		if c.failed[id] {
			c.mu.Unlock()
			return nil, false
		}
		if ch, ok := c.building[id]; ok {
			c.mu.Unlock()
			if !wait {
				return nil, true
			}
			<-ch
			continue
		}
		if c.building == nil {
			c.building = map[string]chan struct{}{}
		}
		ch := make(chan struct{})
		c.building[id] = ch
		c.mu.Unlock()
		if !wait {
			go c.build(id, pick, ch)
			return nil, true
		}
		c.build(id, pick, ch)
	}
}

// pickLocked: the darks of the key closest to the exposure and the
// temperature, sorted by path. Caller holds c.mu.
func (c *HotCache) pickLocked(key string, exp, temp float64) []string {
	type cand struct {
		p      string
		de, dt float64
		t      time.Time
	}
	var cs []cand
	for p, d := range c.darks {
		if d.key != key {
			continue
		}
		x := cand{p: p, t: d.mtime}
		if exp > 0 && d.exp > 0 {
			x.de = math.Abs(math.Log(d.exp / exp))
		}
		x.dt = math.Abs(d.temp - temp)
		if math.IsNaN(x.dt) {
			x.dt = 100
		}
		cs = append(cs, x)
	}
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.de != b.de {
			return a.de < b.de
		}
		if a.dt != b.dt {
			return a.dt < b.dt
		}
		return a.t.After(b.t)
	})
	// darks of other exposures only when those of the closest are too few:
	// a pixel hot in long exposures only is not in short darks
	same := 0
	for same < len(cs) && cs[same].de == cs[0].de {
		same++
	}
	if same >= 2 {
		cs = cs[:same]
	}
	var out []string
	for _, x := range cs[:min(len(cs), hotDarks)] {
		out = append(out, x.p)
	}
	sort.Strings(out)
	return out
}

func (c *HotCache) build(id string, paths []string, done chan struct{}) {
	var hp *HotPixels
	defer func() {
		recover() // a broken dark never stops the frames from being read
		c.mu.Lock()
		if hp != nil {
			if c.maps == nil || len(c.maps) >= 16 {
				c.maps = map[string]*HotPixels{}
			}
			c.maps[id] = hp
		} else {
			if c.failed == nil {
				c.failed = map[string]bool{}
			}
			c.failed[id] = true
		}
		delete(c.building, id)
		c.mu.Unlock()
		close(done)
	}()
	var rs []io.ReaderAt
	var sizes []int64
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			continue
		}
		rs = append(rs, f)
		sizes = append(sizes, st.Size())
	}
	hp, _ = HotFromDarks(rs, sizes)
}
