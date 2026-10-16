package main

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// The tilt of the sensor, as Siril's "tilt" command measures it: the HFR
// of the stars of the four quarters of the frame, drawn as a quadrilateral
// over the preview.

// tiltCache keeps the tilts measured (a measurement reads the whole frame).
type tiltCache struct {
	mu    sync.Mutex
	items map[string]stars.Tilt
}

func (c *tiltCache) get(k string) (stars.Tilt, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.items[k]
	return t, ok
}

func (c *tiltCache) put(k string, t stars.Tilt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil || len(c.items) > 2000 { // the camera tilt takes up to 300 frames of each camera
		c.items = map[string]stars.Tilt{}
	}
	c.items[k] = t
}

func (w *window) tiltToggled() {
	on := w.tiltBox.Checked()
	w.mu.Lock()
	w.saved.ShowTilt = on
	w.mu.Unlock()
	if on {
		w.measureTilt(w.table.CurrentIndex())
	} else {
		w.showTilt(nil, "")
	}
}

// measureTilt measures the tilt of row i, when the box is ticked and no
// blink runs, in the background; the text and the drawing follow.
func (w *window) measureTilt(i int) {
	if !w.tiltBox.Checked() || w.playing || i < 0 || i >= len(w.model.rows) {
		return
	}
	r := w.model.rows[i]
	f, local := r.f, w.localPaths(r)
	key := fmt.Sprintf("%p|%s", f.Src, f.Path)
	if t, ok := w.tilts.get(key); ok {
		w.showTilt(&t, "")
		return
	}
	gen := w.previewGen.Load()
	w.showTilt(nil, "считаю тилт…")
	go func() {
		t, err := tiltOf(f, local)
		if err == nil {
			w.tilts.put(key, t)
		}
		w.mw.Synchronize(func() {
			if w.previewGen.Load() != gen || !w.tiltBox.Checked() {
				return // another frame meanwhile
			}
			if err != nil {
				w.showTilt(nil, "тилт: "+err.Error())
				return
			}
			w.showTilt(&t, "")
		})
	}()
}

// tiltOf measures a frame: on disk here, or on the receiver (unless it is
// downloaded already).
func tiltOf(f *sorter.Frame, local []string) (stars.Tilt, error) {
	if f.Src != nil {
		if l := onDisk(local, f.Size); l != "" {
			return stars.TiltFile(l)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return f.Src.Tilt(ctx, f.Path)
	}
	return stars.TiltFile(f.Path)
}

// showTilt shows the tilt measured (nil: none) or a note, with the text of
// the preview.
func (w *window) showTilt(t *stars.Tilt, note string) {
	w.tilt = t
	info, head := w.previewText, w.previewHead
	switch {
	case t != nil:
		info += fmt.Sprintf(" · тилт %.2f px (%.0f%%), вне оси %.2f px", t.Tilt, t.Percent, t.OffAxis)
		head = fmt.Sprintf("Тилт (как в Siril), HFR в пикселях кадра, FWHM ≈ 1,5×HFR:\r\n"+
			"  ↖ %.2f   ↗ %.2f\r\n  ↙ %.2f   ↘ %.2f\r\n"+
			"тилт      %.2f px (%.0f%% от среднего по углам)\r\n"+
			"вне оси   %.2f px (центр %.2f, края %.2f)\r\n"+
			"звёзд     %d, HFR %.2f px\r\n\r\n",
			t.Corners[0], t.Corners[1], t.Corners[2], t.Corners[3], t.Tilt, t.Percent,
			t.OffAxis, t.Center, t.Edge, t.Stars, t.HFR) + head
	case note != "":
		info += " · " + note
	}
	if info != "" {
		setLong(w.previewInfo, info)
		w.header.SetText(w.withTracks(head))
	}
	w.zoom.cw.Invalidate()
}

// paintTilt draws the tilt as Siril does: a point toward each corner, as
// far from the centre as the stars of that quarter are big (half way for
// the mean), with the HFR of each quarter and of the centre.
func (w *window) paintTilt(c *walk.Canvas, x0, y0, dw, dh float64) {
	t := w.tilt
	if t == nil || !w.tiltBox.Checked() {
		return
	}
	cx, cy := x0+dw/2, y0+dh/2
	half := math.Hypot(dw/2, dh/2)
	mean := (t.Corners[0] + t.Corners[1] + t.Corners[2] + t.Corners[3]) / 4
	dirs := [4][2]float64{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}}
	var pts [4]walk.Point
	for q, d := range dirs {
		r := half / 2 * t.Corners[q] / mean
		pts[q] = walk.Point{X: int(cx + d[0]*dw/2/half*r), Y: int(cy + d[1]*dh/2/half*r)}
	}
	yellow := walk.RGB(255, 204, 0)
	centre := walk.Point{X: int(cx), Y: int(cy)}
	if dash, err := walk.NewCosmeticPen(walk.PenDash, yellow); err == nil {
		for _, p := range pts {
			c.DrawLinePixels(dash, centre, p)
		}
		dash.Dispose()
	}
	if b, err := walk.NewSolidColorBrush(yellow); err == nil {
		if pen, err := walk.NewGeometricPen(walk.PenSolid, 2, b); err == nil {
			c.DrawPolylinePixels(pen, []walk.Point{pts[0], pts[1], pts[3], pts[2], pts[0]})
			pen.Dispose()
		}
		b.Dispose()
	}
	font, err := walk.NewFont("Segoe UI", 10, walk.FontBold)
	if err != nil {
		return
	}
	defer font.Dispose()
	label := func(s string, x, y int, right bool) {
		r := walk.Rectangle{X: x, Y: y - 10, Width: 60, Height: 20}
		format := walk.TextLeft | walk.TextSingleLine | walk.TextVCenter
		if right {
			r.X -= 60
			format = walk.TextRight | walk.TextSingleLine | walk.TextVCenter
		}
		shadow := r
		shadow.X, shadow.Y = r.X+1, r.Y+1
		c.DrawTextPixels(s, font, walk.RGB(0, 0, 0), shadow, format)
		c.DrawTextPixels(s, font, yellow, r, format)
	}
	for q, p := range pts {
		left := dirs[q][0] < 0
		dx := 6
		if left {
			dx = -6
		}
		label(fmt.Sprintf("%.2f", t.Corners[q]), p.X+dx, p.Y+int(dirs[q][1]*10), left)
	}
	label(fmt.Sprintf("%.2f", t.Center), centre.X-30, centre.Y-14, false)
}
