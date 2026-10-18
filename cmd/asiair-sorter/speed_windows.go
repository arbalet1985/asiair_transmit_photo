package main

import (
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The download from a receiver: how many frames at once, and a window with
// the speed of the download going on (or of the last one) second by second.

// parallelChoices are the numbers of downloads at once offered.
var parallelChoices = []int{1, 2, 3, 4, 6, 8}

const defaultParallel = 3

// parallelOf: the number kept, the default when none.
func parallelOf(s settings) int {
	if s.Parallel < 1 || s.Parallel > sorter.MaxParallel {
		return defaultParallel
	}
	return s.Parallel
}

func parallelIndex(n int) int {
	for i, v := range parallelChoices {
		if v == n {
			return i
		}
	}
	return 0
}

func parallelLabels() []string {
	var l []string
	for _, v := range parallelChoices {
		l = append(l, strconv.Itoa(v))
	}
	return l
}

// speedPoint: the bytes per second of a second of the download.
type speedPoint struct {
	at  float64 // seconds from the start
	bps float64
}

// speedLog is the speed of a download (touched by the UI thread only).
type speedLog struct {
	start    time.Time
	pts      []speedPoint
	got      int64 // downloaded so far
	total    int64 // to download in all
	parallel int
	running  bool
}

// speedWatch samples the bytes downloaded once a second while Apply runs;
// bytes is set by its progress, stop ends the sampling.
type speedWatch struct {
	bytes atomic.Int64
	stop  chan struct{}
}

// startSpeed begins a new log for a download of total bytes.
func (w *window) startSpeed(total int64, parallel int) *speedWatch {
	w.speed = &speedLog{start: time.Now(), total: total, parallel: parallel, running: true}
	log := w.speed
	sw := &speedWatch{stop: make(chan struct{})}
	w.invalidateSpeed()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var prev int64
		prevAt := time.Now()
		for {
			select {
			case <-sw.stop:
				b := sw.bytes.Load()
				w.mw.Synchronize(func() {
					log.got = b
					log.running = false
					w.invalidateSpeed()
				})
				return
			case now := <-t.C:
				b := sw.bytes.Load()
				bps := float64(b-prev) / now.Sub(prevAt).Seconds()
				prev, prevAt = b, now
				at := now.Sub(log.start).Seconds()
				w.mw.Synchronize(func() {
					log.pts = append(log.pts, speedPoint{at: at, bps: math.Max(bps, 0)})
					log.got = b
					w.invalidateSpeed()
				})
			}
		}
	}()
	return sw
}

func (w *window) invalidateSpeed() {
	if w.speedView != nil {
		w.speedView.Invalidate()
	}
	if w.speedInfo != nil {
		w.speedInfo.SetText(w.speedText())
	}
}

// speedText: the figures above the chart.
func (w *window) speedText() string {
	l := w.speed
	if l == nil {
		return "Скачиваний с приёмника ещё не было: график появится после «Разложить»."
	}
	state := "скачивание идёт"
	if !l.running {
		state = "скачивание закончено"
	}
	t := fmt.Sprintf("%s, одновременно файлов: %d. Скачано %s из %s", state, l.parallel, sorter.FormatBytes(l.got), sorter.FormatBytes(l.total))
	if n := len(l.pts); n > 0 {
		last := l.pts[n-1]
		if l.running {
			t += fmt.Sprintf(", сейчас %s/с", sorter.FormatBytes(int64(recentSpeed(l.pts, 5))))
		}
		if last.at > 0 {
			t += fmt.Sprintf(", в среднем %s/с за %s", sorter.FormatBytes(int64(float64(l.got)/last.at)), sorter.FormatDuration(last.at))
		}
	}
	return t + "."
}

// recentSpeed: the mean of the last n seconds.
func recentSpeed(pts []speedPoint, n int) float64 {
	pts = pts[max(0, len(pts)-n):]
	if len(pts) == 0 {
		return 0
	}
	var s float64
	for _, p := range pts {
		s += p.bps
	}
	return s / float64(len(pts))
}

// showSpeed opens the window of the chart (one; it may stay open while the
// sorter works).
func (w *window) showSpeed() {
	if w.speedDlg != nil {
		w.speedDlg.Activate()
		return
	}
	var dlg *walk.Dialog
	var cw *walk.CustomWidget
	var info *walk.Label
	err := Dialog{
		AssignTo: &dlg,
		Title:    "Скорость скачивания с приёмника",
		MinSize:  Size{Width: 720, Height: 360},
		Layout:   VBox{},
		Children: []Widget{
			Label{AssignTo: &info, Text: w.speedText(), EllipsisMode: EllipsisEnd},
			CustomWidget{AssignTo: &cw, Paint: func(c *walk.Canvas, _ walk.Rectangle) error { return w.paintSpeed(c) },
				ClearsBackground: true, InvalidatesOnResize: true, DoubleBuffering: true, MinSize: Size{Width: 300, Height: 200}},
			Label{Text: "Синяя линия — скорость по секундам, белая — в среднем за 10 секунд. " +
				"Число файлов, скачиваемых одновременно, — в главном окне рядом с «Как класть файлы».",
				TextColor: walk.RGB(110, 110, 110)},
		},
	}.Create(w.mw)
	if err != nil {
		w.warn(err.Error())
		return
	}
	w.speedDlg, w.speedView, w.speedInfo = dlg, cw, info
	dlg.Disposing().Attach(func() { w.speedDlg, w.speedView, w.speedInfo = nil, nil, nil })
	dlg.Show()
}

func (w *window) paintSpeed(canvas *walk.Canvas) error {
	cr := w.speedView.ClientBoundsPixels()
	if b, err := walk.NewSolidColorBrush(walk.RGB(16, 16, 16)); err == nil {
		canvas.FillRectanglePixels(b, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		b.Dispose()
	}
	font, fg := w.speedView.Font(), walk.RGB(170, 170, 170)
	text := func(s string, r walk.Rectangle, f walk.DrawTextFormat) {
		canvas.DrawTextPixels(s, font, fg, r, f|walk.TextSingleLine)
	}
	const left, top, bottom, right = 80, 10, 20, 10
	W, H := cr.Width-left-right, cr.Height-top-bottom
	if W < 40 || H < 40 {
		return nil
	}
	l := w.speed
	if l == nil || len(l.pts) == 0 {
		text("нет данных", walk.Rectangle{X: left, Y: top, Width: W, Height: 20}, walk.TextLeft)
		return nil
	}
	// the scale: a round speed above the peak, the time from the start
	peak := 0.0
	for _, p := range l.pts {
		peak = math.Max(peak, p.bps)
	}
	// steps of round KB or MB: 1024 bytes each
	unit := 1.0
	for unit*1024 <= peak {
		unit *= 1024
	}
	step := niceStep(math.Max(peak, 1024)*1.1/4/unit) * unit
	hi := math.Ceil(math.Max(peak, 1024)*1.05/step) * step
	span := math.Max(l.pts[len(l.pts)-1].at, 10)
	x := func(at float64) int { return left + int(at/span*float64(W-1)) }
	y := func(v float64) int { return top + H - 1 - int(v/hi*float64(H-1)) }
	grid := walk.RGB(55, 55, 55)
	if pen, err := walk.NewCosmeticPen(walk.PenSolid, grid); err == nil {
		for v := 0.0; v <= hi+step/2; v += step {
			canvas.DrawLinePixels(pen, walk.Point{X: left, Y: y(v)}, walk.Point{X: left + W, Y: y(v)})
			text(sorter.FormatBytes(int64(v))+"/с", walk.Rectangle{X: 0, Y: y(v) - 8, Width: left - 6, Height: 16}, walk.TextRight)
		}
		pen.Dispose()
	}
	text("0:00", walk.Rectangle{X: left, Y: top + H + 2, Width: 80, Height: bottom - 2}, walk.TextLeft)
	text(clock(span), walk.Rectangle{X: left + W - 80, Y: top + H + 2, Width: 80, Height: bottom - 2}, walk.TextRight)
	text(clock(span/2), walk.Rectangle{X: left + W/2 - 40, Y: top + H + 2, Width: 80, Height: bottom - 2}, walk.TextCenter)
	line := func(pts []walk.Point, c walk.Color) {
		if len(pts) < 2 {
			return
		}
		b, err := walk.NewSolidColorBrush(c)
		if err != nil {
			return
		}
		defer b.Dispose()
		if pen, err := walk.NewGeometricPen(walk.PenSolid|walk.PenJoinRound, 2, b); err == nil {
			canvas.DrawPolylinePixels(pen, pts)
			pen.Dispose()
		}
	}
	var raw, avg []walk.Point
	for i, p := range l.pts {
		raw = append(raw, walk.Point{X: x(p.at), Y: y(p.bps)})
		avg = append(avg, walk.Point{X: x(p.at), Y: y(recentSpeed(l.pts[:i+1], 10))})
	}
	line(raw, walk.RGB(80, 140, 240))
	line(avg, walk.RGB(235, 235, 235))
	return nil
}

// niceStep rounds a step of the scale to 1, 2 or 5 times a power of ten.
func niceStep(v float64) float64 {
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

// clock: seconds as h:mm:ss or m:ss.
func clock(sec float64) string {
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
