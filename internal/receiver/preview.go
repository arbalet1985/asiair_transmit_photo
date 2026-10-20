package receiver

import (
	"container/list"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// isFITS reports whether a file name looks like a FITS image.
func isFITS(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".fit", ".fits", ".fts":
		return true
	}
	return false
}

// previewInfo describes a preview; it precedes the pixel data in the answer.
type previewInfo struct {
	Width    int               `json:"width"`
	Height   int               `json:"height"`
	Channels int               `json:"channels"`
	Binning  int               `json:"binning"`
	Bayer    string            `json:"bayer,omitempty"`
	Source   [2]int            `json:"source"` // original width and height
	Header   map[string]string `json:"header"` // selected keywords
	// Stats: of the whole frame, per channel (PixInsight's Statistics)
	Stats []fits.ChannelStats `json:"stats,omitempty"`
	// HotDarks: the darks the hot pixels left out of Stats were told by
	HotDarks int `json:"hot_darks,omitempty"`
}

// headerKeys are shown under the preview.
var headerKeys = []string{
	"OBJECT", "IMAGETYP", "FRAME", "EXPTIME", "EXPOSURE", "GAIN", "OFFSET",
	"CCD-TEMP", "SET-TEMP", "FILTER", "XBINNING", "YBINNING", "INSTRUME",
	"TELESCOP", "FOCALLEN", "DATE-OBS", "BAYERPAT", "RA", "DEC", "OBJCTRA", "OBJCTDEC",
}

type cachedPreview struct {
	key  string
	info previewInfo
	data []uint16
}

// previewCache keeps the last previews so paging back and forth is instant.
type previewCache struct {
	mu    sync.Mutex
	max   int
	bytes int        // total size of the cached samples
	order *list.List // front = most recent
	items map[string]*list.Element
}

// previewCacheBytes caps the memory used by cached previews.
const previewCacheBytes = 200 << 20

func newPreviewCache(max int) *previewCache {
	return &previewCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (c *previewCache) get(key string) *cachedPreview {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*cachedPreview)
	}
	return nil
}

func (c *previewCache) put(p *cachedPreview) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[p.key]; ok {
		c.order.Remove(e)
		c.bytes -= len(e.Value.(*cachedPreview).data) * 2
	}
	c.items[p.key] = c.order.PushFront(p)
	c.bytes += len(p.data) * 2
	for c.order.Len() > 1 && (c.order.Len() > c.max || c.bytes > previewCacheBytes) {
		last := c.order.Back()
		c.order.Remove(last)
		old := last.Value.(*cachedPreview)
		delete(c.items, old.key)
		c.bytes -= len(old.data) * 2
	}
}

// buildPreview reads a received FITS file into a preview.
func (s *Server) buildPreview(p string, maxWidth int) (*cachedPreview, error) {
	local := s.getStore().Local(p)
	st, err := os.Stat(local)
	if err != nil {
		return nil, fmt.Errorf("файл не найден: %w", err)
	}
	key := fmt.Sprintf("%s|%d|%d|%d", local, st.Size(), st.ModTime().UnixNano(), maxWidth)
	if c := s.previews.get(key); c != nil {
		return c, nil
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pending := false
	im, err := fits.PreviewHot(f, maxWidth, func(h *fits.Header) *fits.HotPixels {
		hp, p := s.hot.For(h, false)
		pending = p
		return hp
	})
	if err != nil {
		return nil, err
	}
	info := previewInfo{
		Width: im.Width, Height: im.Height, Channels: im.Channels, Binning: im.Binning, Bayer: im.Bayer,
		Header: map[string]string{}, Stats: im.Stats, HotDarks: im.HotDarks,
	}
	info.Source[0], _ = strconv.Atoi(im.Header.Get("NAXIS1"))
	info.Source[1], _ = strconv.Atoi(im.Header.Get("NAXIS2"))
	for _, k := range headerKeys {
		if v := im.Header.Get(k); v != "" {
			info.Header[k] = v
		}
	}
	c := &cachedPreview{key: key, info: info, data: im.Uint16()}
	if !pending { // the darks are being read: the next look leaves out more
		s.previews.put(c)
	}
	return c, nil
}

// handleUIPreview answers with a small binary package:
// uint32 LE length of the JSON description, the JSON (padded with spaces to
// an even length), then the samples as uint16 LE, channels interleaved.
func (s *Server) handleUIPreview(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !isFITS(p) {
		http.Error(w, "не FITS-файл", http.StatusBadRequest)
		return
	}
	maxWidth, _ := strconv.Atoi(r.URL.Query().Get("max"))
	switch {
	case maxWidth <= 0:
		maxWidth = 1600
	case maxWidth > 12000: // enough for full resolution of any camera
		maxWidth = 12000
	}
	c, err := s.buildPreview(p, maxWidth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	meta, _ := json.Marshal(c.info)
	if len(meta)%2 == 1 {
		meta = append(meta, ' ')
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(meta)))
	w.Write(n[:])
	w.Write(meta)
	buf := make([]byte, len(c.data)*2)
	for i, v := range c.data {
		binary.LittleEndian.PutUint16(buf[i*2:], v)
	}
	w.Write(buf)
}
