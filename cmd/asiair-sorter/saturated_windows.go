package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The saturated areas of the frame, circled over the preview: found in the
// file itself at its full resolution (the preview is binned).

// satAreasCache keeps the areas found (a search reads the whole frame).
type satAreasCache struct {
	mu    sync.Mutex
	items map[string]*fits.SaturatedAreas
}

func (c *satAreasCache) get(k string) (*fits.SaturatedAreas, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.items[k]
	return a, ok
}

func (c *satAreasCache) put(k string, a *fits.SaturatedAreas) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) > 200 {
		c.items = map[string]*fits.SaturatedAreas{}
	}
	c.items[k] = a
}

func (w *window) satAreasToggled() {
	on := w.satAreasBox.Checked()
	w.mu.Lock()
	w.saved.ShowSaturated = on
	w.mu.Unlock()
	if on {
		w.findSatAreas(w.table.CurrentIndex())
	} else {
		w.showSatAreas(nil, "")
	}
}

// findSatAreas finds the saturated areas of row i, when the box is ticked
// and no blink runs, in the background.
func (w *window) findSatAreas(i int) {
	if !w.satAreasBox.Checked() || w.playing || i < 0 || i >= len(w.model.rows) {
		return
	}
	r := w.model.rows[i]
	f, local := r.f, w.localPaths(r)
	key := fmt.Sprintf("%p|%s", f.Src, f.Path)
	if a, ok := w.satAreasCache.get(key); ok {
		w.showSatAreas(a, "")
		return
	}
	gen := w.previewGen.Load()
	w.showSatAreas(nil, "ищу пересвет…")
	go func() {
		a, err := satAreasOf(f, local)
		if err == nil {
			w.satAreasCache.put(key, a)
		}
		w.mw.Synchronize(func() {
			if w.previewGen.Load() != gen || !w.satAreasBox.Checked() {
				return // another frame meanwhile
			}
			if err != nil {
				w.showSatAreas(nil, "пересвет: "+err.Error())
				return
			}
			w.showSatAreas(a, "")
		})
	}()
}

// satAreasOf reads a frame: on disk here, or on the receiver (unless it is
// downloaded already).
func satAreasOf(f *sorter.Frame, local []string) (*fits.SaturatedAreas, error) {
	file := f.Path
	if f.Src != nil {
		file = onDisk(local, f.Size)
		if file == "" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return f.Src.Saturated(ctx, f.Path)
		}
	}
	fh, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	return fits.ReadSaturated(fh, st.Size(), hotFor(true))
}

// hotPixels: the hot pixels of the cameras, found on the darks of the
// frames read (a frame of a receiver gets them there).
var hotPixels fits.HotCache

// hotFor: the hot pixels of a frame; wait: read the darks now if not yet.
func hotFor(wait bool) func(*fits.Header) *fits.HotPixels {
	return func(h *fits.Header) *fits.HotPixels {
		hp, _ := hotPixels.For(h, wait)
		return hp
	}
}

// setDarks tells the darks of the layout (in the background: their headers
// are read).
func setDarks(l *sorter.Layout, out string) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("Дарки для горячих пикселей не прочитаны: %v", p)
		}
	}()
	hotPixels.SetDarks(l.DarkFiles(out))
}

// showSatAreas shows the areas found (nil: none) or a note, with the rest
// of the text of the preview.
func (w *window) showSatAreas(a *fits.SaturatedAreas, note string) {
	w.satAreas, w.satAreasNote = a, note
	w.showTilt(w.tilt, "")
}

// satAreasText: what the line under the preview tells of them.
func (w *window) satAreasText() string {
	a := w.satAreas
	switch {
	case !w.satAreasBox.Checked():
		return ""
	case a == nil:
		return w.satAreasNote
	}
	t := fmt.Sprintf("пересвеченных областей %d (%d пикс.)", a.Count, a.Pixels)
	switch {
	case a.Count == 0:
		t = "пересвеченных пикселей нет"
	case a.Count > len(a.Spots):
		t += fmt.Sprintf(", обведены %d крупнейших", len(a.Spots))
	}
	if a.Hot > 0 {
		t += fmt.Sprintf("; горячих пикселей не считается: %d", a.Hot)
		if a.HotDarks > 0 {
			t += fmt.Sprintf(" (по даркам: %d, и одиночные)", a.HotDarks)
		} else {
			t += " (одиночные, без света вокруг)"
		}
	}
	return t
}

// paintSatAreas circles the saturated areas in red.
func (w *window) paintSatAreas(c *walk.Canvas, x0, y0, dw, dh float64) {
	a := w.satAreas
	if a == nil || a.Width == 0 || len(a.Spots) == 0 || !w.satAreasBox.Checked() {
		return
	}
	b, err := walk.NewSolidColorBrush(walk.RGB(255, 59, 48))
	if err != nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid, 2, b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	k := dw / float64(a.Width)
	for _, s := range a.Spots {
		x, y := x0+(s.X+0.5)*k, y0+(s.Y+0.5)*k
		r := math.Max(7, s.R*k+2)
		c.DrawEllipsePixels(pen, walk.Rectangle{X: int(x - r), Y: int(y - r), Width: int(2 * r), Height: int(2 * r)})
	}
}
