package main

import (
	"fmt"
	"math"

	"github.com/lxn/walk"
)

// The histogram of the frame shown, under the preview. A click switches
// what it shows: the samples (zoomed to where they are), the same on a log
// scale, the screen after the stretch.

const (
	histBins  = 512
	histModes = 3
)

var histModeNames = [histModes]string{"значения кадра", "значения, лог. шкала", "после растяжки"}

func (w *window) histToggled() {
	on := w.histBox.Checked()
	w.histView.SetVisible(on)
	w.mu.Lock()
	w.saved.ShowHist = on
	w.mu.Unlock()
}

func (w *window) histClicked(_, _ int, b walk.MouseButton) {
	if b != walk.LeftButton {
		return
	}
	w.mu.Lock()
	w.saved.HistMode = (w.saved.HistMode + 1) % histModes
	w.mu.Unlock()
	w.histView.Invalidate()
}

func (w *window) paintHist(canvas *walk.Canvas, _ walk.Rectangle) error {
	cr := w.histView.ClientBoundsPixels()
	if b, err := walk.NewSolidColorBrush(walk.RGB(16, 16, 16)); err == nil {
		canvas.FillRectanglePixels(b, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		b.Dispose()
	}
	font, fg := w.histView.Font(), walk.RGB(170, 170, 170)
	text := func(s string, r walk.Rectangle, f walk.DrawTextFormat) {
		canvas.DrawTextPixels(s, font, fg, r, f|walk.TextSingleLine)
	}
	h := w.hist
	if h == nil {
		text("нет гистограммы", walk.Rectangle{X: 6, Y: 4, Width: cr.Width - 12, Height: 18}, walk.TextLeft)
		return nil
	}
	w.mu.Lock()
	mode := w.saved.HistMode
	w.mu.Unlock()
	const top, bottom = 18, 16 // room for the text
	W, H := cr.Width, cr.Height-top-bottom
	if W < 20 || H < 20 {
		return nil
	}
	counts := h.Counts
	if mode == 2 {
		counts = h.Shown
	}
	// each channel as a line over the columns of the widget
	cols := make([][]float64, len(counts))
	peak := 1.0
	for c, bins := range counts {
		// the bins of each column: several, or one shared by several columns
		col := make([]float64, W)
		n := len(bins)
		for x := range col {
			b0 := x * n / W
			b1 := max(b0+1, (x+1)*n/W)
			for _, k := range bins[b0:b1] {
				col[x] += float64(k)
			}
			col[x] /= float64(b1 - b0)
		}
		for x := range col {
			if mode == 1 {
				col[x] = math.Log1p(col[x])
			}
			peak = math.Max(peak, col[x])
		}
		cols[c] = col
	}
	colors := []walk.Color{walk.RGB(229, 72, 77), walk.RGB(48, 164, 108), walk.RGB(80, 120, 230)}
	if len(cols) == 1 {
		colors = []walk.Color{walk.RGB(220, 220, 220)}
	}
	for c, col := range cols {
		pts := make([]walk.Point, W)
		for x, v := range col {
			pts[x] = walk.Point{X: x, Y: top + H - int(v/peak*float64(H-1))}
		}
		if pen, err := walk.NewCosmeticPen(walk.PenSolid, colors[c]); err == nil {
			canvas.DrawPolylinePixels(pen, pts)
			pen.Dispose()
		}
	}
	lo, hi := h.Lo*65535, h.Hi*65535
	if mode != 2 {
		// the black and white points of the stretch
		line := func(v float64, c walk.Color) {
			x := int((v*65535 - lo) / (hi - lo + 1) * float64(W))
			if x < 0 || x >= W {
				return
			}
			if pen, err := walk.NewCosmeticPen(walk.PenDash, c); err == nil {
				canvas.DrawLinePixels(pen, walk.Point{X: x, Y: top}, walk.Point{X: x, Y: top + H})
				pen.Dispose()
			}
		}
		grey := walk.RGB(150, 150, 150)
		if len(h.Blacks) == len(cols) && len(cols) > 1 {
			// channels stretched each on its own: a black point of each
			for c, v := range h.Blacks {
				line(v, colors[c])
			}
		} else {
			line(h.Black, grey)
		}
		line(h.White, grey)
	} else {
		lo, hi = 0, 255
	}
	text(histModeNames[mode]+" — щелчок меняет", walk.Rectangle{X: 6, Y: 1, Width: W - 12, Height: 16}, walk.TextLeft)
	text(fmt.Sprintf("%.0f", lo), walk.Rectangle{X: 4, Y: cr.Height - bottom, Width: 80, Height: bottom}, walk.TextLeft)
	text(fmt.Sprintf("%.0f", hi), walk.Rectangle{X: W - 84, Y: cr.Height - bottom, Width: 80, Height: bottom}, walk.TextRight)
	return nil
}
