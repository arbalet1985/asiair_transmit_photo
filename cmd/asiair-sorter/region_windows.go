package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// Measurement of a star: the right button on the preview shows next to the
// mouse the statistics of a small area there, read from the file itself
// (the preview is binned, which lowers the peaks of stars): is the star
// saturated. The point stays while paging and blinking.

const probeRadius = 10

// probe is the point measured and its result.
type probe struct {
	fx, fy float64 // 0..1 of the frame
	res    *fits.RegionStats
	frame  *sorter.Frame // the result is of this frame
	gen    int
}

// probeAt measures the point fx, fy of the frame shown (from the zoom view).
func (w *window) probeAt(fx, fy float64) {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.model.rows) || w.zoom.bmp == nil {
		return
	}
	if w.probe == nil {
		w.probe = &probe{}
	}
	w.probe.fx, w.probe.fy = fx, fy
	var pt win.POINT
	win.GetCursorPos(&pt)
	w.showProbeWindow(int(pt.X), int(pt.Y))
	w.measureProbe()
}

// measureProbe reads the area of the point on the frame selected, in the
// background.
func (w *window) measureProbe() {
	p := w.probe
	i := w.table.CurrentIndex()
	if p == nil || i < 0 || i >= len(w.model.rows) {
		return
	}
	r := w.model.rows[i]
	f, local := r.f, w.localPaths(r)
	srcW := w.zoom.srcW
	srcH := 0
	if w.zoom.w > 0 {
		srcH = int(math.Round(float64(srcW) * float64(w.zoom.h) / float64(w.zoom.w)))
	}
	x, y := int(math.Round(p.fx*float64(srcW-1))), int(math.Round(p.fy*float64(max(srcH, 1)-1)))
	p.gen++
	gen := p.gen
	if p.frame != f {
		w.setProbeText("Считаю…")
	}
	go func() {
		res, err := readRegion(f, local, x, y)
		w.mw.Synchronize(func() {
			if w.probe != p || p.gen != gen {
				return // another point or frame meanwhile
			}
			if err != nil {
				p.res, p.frame = nil, f
				w.setProbeText("Замер: " + err.Error())
			} else {
				p.res, p.frame = res, f
				w.setProbeText(probeText(res))
			}
			w.zoom.cw.Invalidate()
		})
	}()
}

// readRegion: from the file on disk (or the copy of a frame of a receiver
// downloaded already), else on the receiver.
func readRegion(f *sorter.Frame, local []string, x, y int) (*fits.RegionStats, error) {
	file := f.Path
	if f.Src != nil {
		file = onDisk(local, f.Size)
		if file == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return f.Src.Region(ctx, f.Path, x, y, probeRadius)
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
	return fits.ReadRegion(fh, st.Size(), x, y, probeRadius)
}

func saturatedArea(r *fits.RegionStats) bool { return r.Saturation > 0 && r.Saturated > 0 }

func probeText(r *fits.RegionStats) string {
	num := func(v float64) string { return strings.Replace(fmt.Sprintf("%.0f", v), ".", ",", 1) }
	var b strings.Builder
	switch {
	case r.Saturation <= 0:
		fmt.Fprintf(&b, "Пик %g — уровень насыщения неизвестен (данные с плавающей точкой)\r\n", r.Peak)
	case saturatedArea(r):
		fmt.Fprintf(&b, "Пик %s АЦП — ПЕРЕСВЕЧЕНА: %d пикс. не ниже %s\r\n", num(r.Peak), r.Saturated, num(r.Saturation))
	default:
		fmt.Fprintf(&b, "Пик %s АЦП — %.0f%% насыщения, не пересвечена\r\n", num(r.Peak), 100*r.Peak/r.Saturation)
	}
	fmt.Fprintf(&b, "Над фоном: %s (фон %s)\r\n", num(r.Peak-r.Background), num(r.Background))
	fmt.Fprintf(&b, "Область %d×%d в (%d, %d), пик в (%d, %d)\r\n\r\n", r.W, r.H, r.X, r.Y, r.PeakX, r.PeakY)
	b.WriteString(fits.StatsTable(r.Channels))
	return b.String()
}

// showProbeWindow opens the window of the measurement (or moves it) next
// to the mouse, inside the screen.
func (w *window) showProbeWindow(x, y int) {
	if w.probeDlg == nil {
		var dlg *walk.Dialog
		var te *walk.TextEdit
		mono := Font{Family: "Consolas", PointSize: 9}
		if err := (Dialog{
			AssignTo: &dlg,
			Title:    "Замер звезды",
			MinSize:  Size{Width: 360, Height: 300},
			Size:     Size{Width: 400, Height: 330},
			Layout:   VBox{MarginsZero: true},
			Children: []Widget{
				TextEdit{AssignTo: &te, ReadOnly: true, VScroll: true, Font: mono},
			},
		}).Create(w.mw); err != nil {
			w.warn(err.Error())
			return
		}
		dlg.Closing().Attach(func(*bool, walk.CloseReason) {
			w.probeDlg, w.probeEdit, w.probe = nil, nil, nil
			w.zoom.cw.Invalidate()
		})
		w.probeDlg, w.probeEdit = dlg, te
	}
	b := w.probeDlg.BoundsPixels()
	mon := win.MonitorFromWindow(w.mw.Handle(), win.MONITOR_DEFAULTTONEAREST)
	var mi win.MONITORINFO
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	left, top := x+20, y+20
	if win.GetMonitorInfo(mon, &mi) {
		wa := mi.RcWork
		if left+b.Width > int(wa.Right) {
			left = max(int(wa.Left), x-20-b.Width)
		}
		if top+b.Height > int(wa.Bottom) {
			top = max(int(wa.Top), y-20-b.Height)
		}
	}
	w.probeDlg.SetBoundsPixels(walk.Rectangle{X: left, Y: top, Width: b.Width, Height: b.Height})
	w.probeDlg.Show()
}

func (w *window) setProbeText(t string) {
	if w.probeEdit != nil {
		w.probeEdit.SetText(t)
	}
}

// paintProbe draws the area measured on the frame: red when saturated.
func (w *window) paintProbe(c *walk.Canvas, x0, y0, dw, dh float64) {
	p := w.probe
	i := w.table.CurrentIndex()
	if p == nil || p.res == nil || i < 0 || i >= len(w.model.rows) || w.model.rows[i].f != p.frame || p.res.Width == 0 {
		return
	}
	r := p.res
	k := dw / float64(r.Width)
	col := walk.RGB(48, 209, 88)
	if saturatedArea(r) {
		col = walk.RGB(255, 59, 48)
	}
	b, err := walk.NewSolidColorBrush(col)
	if err != nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid, 2, b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	rx, ry := x0+float64(r.X)*k-2, y0+float64(r.Y)*k-2
	rw, rh := math.Max(float64(r.W)*k+4, 8), math.Max(float64(r.H)*k+4, 8)
	c.DrawRectanglePixels(pen, walk.Rectangle{X: int(rx), Y: int(ry), Width: int(rw), Height: int(rh)})
}
