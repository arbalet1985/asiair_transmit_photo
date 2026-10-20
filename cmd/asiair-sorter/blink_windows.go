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
	hist       *fits.Histogram
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
// local: where a copy of a frame of a receiver may be on disk already;
// linked: the channels are stretched together.
func (c *previewCache) get(f *sorter.Frame, local []string, maxW int, linked bool) *preview {
	key := fmt.Sprintf("%p|%s|%d|%t", f.Src, f.Path, maxW, linked)
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

	e.p = readPreview(f, local, maxW, linked)
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

// localPaths: where the frame of a receiver of the row may be on disk
// already — its copy in the output found by the scan, or where it is
// downloaded to. nil for a file on disk.
func (w *window) localPaths(r row) []string {
	if r.f.Src == nil || r.it == nil || w.scannedOut == "" {
		return nil
	}
	var paths []string
	if r.it.Existing != "" {
		paths = append(paths, r.it.Existing)
	}
	return append(paths, filepath.Join(w.scannedOut, filepath.FromSlash(r.it.Dest)))
}

// onDisk is the first of the paths holding a file of the size.
func onDisk(paths []string, size int64) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() == size {
			return p
		}
	}
	return ""
}

// readPreview reads a frame: binned to at most maxW pixels wide and
// auto-stretched, with its header. A frame of a receiver is read from its
// copy on disk (local) when downloaded already, else binned on the
// receiver, so only the small preview comes over the network. The colour
// channels are stretched together (linked) or each on its own.
func readPreview(f *sorter.Frame, local []string, maxW int, linked bool) *preview {
	src, file := f.Src, f.Path
	if src != nil {
		if l := onDisk(local, f.Size); l != "" {
			src, file = nil, l
		}
	}
	name := filepath.Base(filepath.FromSlash(file))
	var im *fits.Image
	var err error
	p := &preview{}
	if src != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		im, err = src.Preview(ctx, file, maxW)
		cancel()
		var b strings.Builder
		for _, c := range f.Cards {
			fmt.Fprintf(&b, "%-8s = %s\r\n", c.Key, c.Value)
		}
		p.head = b.String()
	} else {
		var fh *os.File
		if fh, err = os.Open(file); err == nil {
			im, err = fits.PreviewHot(fh, maxW, hotFor(false))
			fh.Close()
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
	if src != nil {
		get = func(k string) string {
			for _, c := range f.Cards {
				if c.Key == k {
					return c.Value
				}
			}
			return ""
		}
	}
	if t := fits.StatsTable(im.Stats); t != "" {
		p.head = "Статистика кадра, " + statsUnit(im) + ":\r\n" + t + "\r\n" + p.head
	}
	p.moon, p.obj = frameSky(f, get)
	p.pic = im.StretchedChannels(linked)
	p.hist = im.HistogramChannels(histBins, linked)
	p.srcW = im.Width * im.Binning
	p.full = maxW >= fullWidth || im.Binning <= 1 || im.Bayer != "" && im.Binning <= 2
	p.info = fmt.Sprintf("%s — %d×%d", name, im.Width*im.Binning, im.Height*im.Binning)
	if im.Bayer != "" {
		p.info += ", " + im.Bayer
	}
	switch {
	case src != nil:
		p.info += " (с приёмника)"
	case f.Src != nil:
		p.info += " (уже скачан, с диска)"
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
		w.measureTilt(w.table.CurrentIndex()) // not measured while blinking
	}
}

// step goes to the frame before (-1) or after (+1) in the table; the blink
// stops, as in the receiver's viewer.
func (w *window) step(d int) {
	if w.playing {
		w.setPlay(false)
	}
	n := len(w.model.rows)
	if n == 0 {
		return
	}
	i := w.table.CurrentIndex() + d
	if w.table.CurrentIndex() < 0 {
		i = 0
	}
	if i < 0 || i >= n {
		return
	}
	w.table.SetCurrentIndex(i)
	w.table.EnsureItemVisible(i)
}

// linkedChannels: the colour channels are stretched together, as
// "Каналы вместе" in the receiver's viewer.
func (w *window) linkedChannels() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.saved.LinkedChannels
}

// linkedToggled shows the frame stretched again.
func (w *window) linkedToggled() {
	w.mu.Lock()
	w.saved.LinkedChannels = w.linkedBox.Checked()
	w.mu.Unlock()
	if w.zoom.bmp != nil || w.previewPending {
		w.showPreview()
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

// readAhead reads the next frames of the blink while this one is shown,
// several at once.
func (w *window) readAhead() {
	n := len(w.model.rows)
	if !w.playing || n < 2 {
		return
	}
	maxW, linked := w.previewWidth(), w.linkedChannels()
	for k := 1; k <= min(3, n-1); k++ {
		r := w.model.rows[(w.table.CurrentIndex()+k)%n]
		local := w.localPaths(r)
		go w.previews.get(r.f, local, maxW, linked)
	}
}

// statsUnit: the units of the statistics: ADU for integer data
func statsUnit(im *fits.Image) string {
	if len(im.Stats) > 0 && im.Stats[0].NoMedian {
		return "значения файла"
	}
	return "АЦП (ADU)"
}
