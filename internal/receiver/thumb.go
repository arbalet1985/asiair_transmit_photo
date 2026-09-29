package receiver

import (
	"bytes"
	"container/list"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNG files for their thumbnails
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// Thumbnails for the gallery: small JPEG pictures, FITS frames auto
// stretched like the viewer does (PixInsight STF).

const (
	thumbDefault = 320
	thumbMax     = 640
	thumbCacheN  = 800 // about 20 KB each
)

type thumbCache struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
}

type thumbItem struct {
	key string
	jpg []byte
}

func newThumbCache() *thumbCache {
	return &thumbCache{order: list.New(), items: map[string]*list.Element{}}
}

func (c *thumbCache) get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*thumbItem).jpg
	}
	return nil
}

func (c *thumbCache) put(key string, jpg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return
	}
	c.items[key] = c.order.PushFront(&thumbItem{key, jpg})
	for c.order.Len() > thumbCacheN {
		e := c.order.Back()
		c.order.Remove(e)
		delete(c.items, e.Value.(*thumbItem).key)
	}
}

// makeThumb builds the thumbnail of a received file, at most size wide.
func makeThumb(local string, size int) ([]byte, error) {
	f, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var src image.Image
	if isFITS(local) {
		im, err := fits.Preview(f, size)
		if err != nil {
			return nil, err
		}
		src = im.Stretched()
	} else {
		if src, _, err = image.Decode(f); err != nil {
			return nil, err
		}
	}
	if src.Bounds().Dx() > size {
		src = shrink(src, size)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// shrink scales an image down to width w, averaging a few samples per pixel.
func shrink(src image.Image, w int) image.Image {
	b := src.Bounds()
	h := max(1, b.Dy()*w/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sx, sy := float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)
	const n = 3 // samples per axis
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for j := 0; j < n; j++ {
				for i := 0; i < n; i++ {
					px := b.Min.X + int((float64(x)+(float64(i)+0.5)/n)*sx)
					py := b.Min.Y + int((float64(y)+(float64(j)+0.5)/n)*sy)
					cr, cg, cb, _ := src.At(px, py).RGBA()
					r, g, bl = r+cr, g+cg, bl+cb
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/(n*n)>>8), uint8(g/(n*n)>>8), uint8(bl/(n*n)>>8), 255
		}
	}
	return dst
}

// handleUIThumb: /ui/thumb?path=…&w=320 — a JPEG thumbnail of a received
// FITS, JPG or PNG file.
func (s *Server) handleUIThumb(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n := strings.ToLower(p)
	if !isFITS(p) && !strings.HasSuffix(n, ".jpg") && !strings.HasSuffix(n, ".jpeg") && !strings.HasSuffix(n, ".png") {
		http.Error(w, "нет миниатюры для такого файла", http.StatusBadRequest)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("w"))
	if size <= 0 {
		size = thumbDefault
	}
	size = min(size, thumbMax)
	local := s.getStore().Local(p)
	st, err := os.Stat(local)
	if err != nil {
		http.Error(w, "файла здесь нет", http.StatusNotFound)
		return
	}
	key := fmt.Sprintf("%s|%d|%d|%d", p, st.Size(), st.ModTime().UnixNano(), size)
	etag := `"` + strconv.FormatUint(hashString(key), 36) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	jpg := s.thumbs.get(key)
	if jpg == nil {
		s.thumbSem <- struct{}{} // a few at a time: each reads a whole frame
		jpg, err = makeThumb(local, size)
		<-s.thumbSem
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		s.thumbs.put(key, jpg)
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(jpg))
}

// hashString is FNV-1a.
func hashString(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
