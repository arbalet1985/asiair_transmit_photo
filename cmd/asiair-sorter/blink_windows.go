package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// preview is a frame read for showing: the stretched picture and its text.
type preview struct {
	pic        image.Image
	info, head string
	srcW       int  // width of the frame on the sensor
	full       bool // the picture has the full resolution
	moon       *moon.Info
	obj        *skyObject
	err        error
}

// previewCacheBytes caps the memory of the pictures kept for blinking and
// paging back.
const previewCacheBytes = 384 << 20

// previewCache keeps the last previews read; a preview being read is read
// once even when asked for twice (shown and read ahead while blinking).
type previewCache struct {
	mu    sync.Mutex
	items map[string]*cachedPreview
	order []string // oldest first
}

type cachedPreview struct {
	done  chan struct{}
	p     *preview
	bytes int64
}

func (c *previewCache) clear() {
	c.mu.Lock()
	c.items, c.order = nil, nil
	c.mu.Unlock()
}

// get returns the preview of the frame, reading it if needed (blocks).
func (c *previewCache) get(f *sorter.Frame, maxW int) *preview {
	key := fmt.Sprintf("%p|%s|%d", f.Src, f.Path, maxW)
	c.mu.Lock()
	if c.items == nil {
		c.items = map[string]*cachedPreview{}
	}
	if e, ok := c.items[key]; ok {
		c.mu.Unlock()
		<-e.done
		return e.p
	}
	e := &cachedPreview{done: make(chan struct{})}
	c.items[key] = e
	c.order = append(c.order, key)
	c.mu.Unlock()

	e.p = readPreview(f, maxW)
	c.mu.Lock()
	if e.p.err != nil {
		delete(c.items, key) // tried again next time
	} else if e.p.pic != nil {
		b := e.p.pic.Bounds()
		e.bytes = int64(b.Dx()) * int64(b.Dy()) * 4
	}
	c.trim(key)
	c.mu.Unlock()
	close(e.done)
	return e.p
}

// trim drops the oldest previews over the cap, never the one just read.
func (c *previewCache) trim(keep string) {
	var total int64
	for _, e := range c.items {
		total += e.bytes
	}
	kept := c.order[:0]
	for _, k := range c.order {
		e, ok := c.items[k]
		if !ok {
			continue
		}
		if total > previewCacheBytes && k != keep && e.bytes > 0 {
			total -= e.bytes
			delete(c.items, k)
			continue
		}
		kept = append(kept, k)
	}
	c.order = kept
}

// readPreview reads a frame: binned to at most maxW pixels wide and
// auto-stretched, with its header. A frame of a receiver is binned there,
// so only the small preview comes over the network.
func readPreview(f *sorter.Frame, maxW int) *preview {
	name := filepath.Base(filepath.FromSlash(f.Path))
	var im *fits.Image
	var err error
	p := &preview{}
	if f.Src != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		im, err = f.Src.Preview(ctx, f.Path, maxW)
		cancel()
		var b strings.Builder
		for _, c := range f.Cards {
			fmt.Fprintf(&b, "%-8s = %s\r\n", c.Key, c.Value)
		}
		p.head = b.String()
	} else {
		var file *os.File
		if file, err = os.Open(f.Path); err == nil {
			im, err = fits.Preview(file, maxW)
			file.Close()
		}
		if err == nil {
			var b strings.Builder
			for _, k := range im.Header.Keys() {
				fmt.Fprintf(&b, "%-8s = %s\r\n", k, im.Header.Get(k))
			}
			p.head = b.String()
		}
	}
	if err != nil {
		p.err = err
		p.info = fmt.Sprintf("%s: %v", name, err)
		return p
	}
	get := im.Header.Get
	if f.Src != nil {
		get = func(k string) string {
			for _, c := range f.Cards {
				if c.Key == k {
					return c.Value
				}
			}
			return ""
		}
	}
	p.moon, p.obj = frameSky(f, get)
	p.pic = im.Stretched()
	p.srcW = im.Width * im.Binning
	p.full = maxW >= fullWidth || im.Binning <= 1 || im.Bayer != "" && im.Binning <= 2
	p.info = fmt.Sprintf("%s — %d×%d", name, im.Width*im.Binning, im.Height*im.Binning)
	if im.Bayer != "" {
		p.info += ", " + im.Bayer
	}
	if f.Src != nil {
		p.info += " (с приёмника)"
	}
	return p
}

// fullWidth asks for the full resolution of any camera.
const fullWidth = 8000

// previewWidth: fitted, a small preview is enough; closer, the full resolution.
func (w *window) previewWidth() int {
	if w.zoom.scale > 1 {
		return fullWidth
	}
	return 1200
}

// Blink: the frames of the table, as filtered, one after another in a loop
// at the speed of the slider. The zoom and the point looked at stay.

// blinkFPS: the slider counts half frames per second.
func (w *window) blinkFPS() float64 { return float64(w.speedSlider.Value()) / 2 }

func (w *window) speedChanged() {
	if w.speedSlider == nil || w.speedLabel == nil {
		return // still being made
	}
	w.speedLabel.SetText(strings.Replace(fmt.Sprintf("%g к/с", w.blinkFPS()), ".", ",", 1))
	w.mu.Lock()
	w.saved.BlinkSpeed = w.speedSlider.Value()
	w.mu.Unlock()
}

func (w *window) togglePlay() { w.setPlay(!w.playing) }

func (w *window) setPlay(on bool) {
	on = on && len(w.model.rows) > 1
	w.playing = on
	w.playGen++
	if on {
		w.btnPlay.SetText("⏸ Стоп")
		if w.table.CurrentIndex() < 0 {
			w.table.SetCurrentIndex(0)
		}
		w.blinkShown = time.Now()
		w.scheduleBlink() // a frame being read schedules again when shown
	} else {
		w.btnPlay.SetText("▶ Блинк")
	}
}

// scheduleBlink: the next frame comes when the one shown has been shown
// long enough; a frame read slowly holds the next one back. Only the last
// one scheduled counts.
func (w *window) scheduleBlink() {
	if !w.playing {
		return
	}
	w.playGen++
	gen := w.playGen
	wait := time.Duration(float64(time.Second)/w.blinkFPS()) - time.Since(w.blinkShown)
	time.AfterFunc(max(wait, 0), func() {
		w.mw.Synchronize(func() {
			// a frame still being read schedules the next itself when shown
			if gen == w.playGen && w.playing && !w.previewPending {
				w.blinkNext()
			}
		})
	})
}

func (w *window) blinkNext() {
	n := len(w.model.rows)
	if n < 2 {
		w.setPlay(false)
		return
	}
	w.blinkShown = time.Now()
	w.table.SetCurrentIndex((w.table.CurrentIndex() + 1) % n) // shows the frame, then schedules the next
}

// readAhead reads the next frame of the blink while this one is shown.
func (w *window) readAhead() {
	n := len(w.model.rows)
	if !w.playing || n < 2 {
		return
	}
	f := w.model.rows[(w.table.CurrentIndex()+1)%n].f
	maxW := w.previewWidth()
	go w.previews.get(f, maxW)
}
