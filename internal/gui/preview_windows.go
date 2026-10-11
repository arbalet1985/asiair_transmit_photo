package gui

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"path"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"github.com/arbalet1985/asiair_transmit_photo/internal/agent"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

// The tray preview: while a frame is being sent, holding the mouse over the
// agent's tray icon shows the frame as far as it has gone (built from the
// bytes sent, see agent.Preview) with its name and progress. A tooltip
// cannot hold a picture, so this is a small borderless window of its own;
// the tray's mouse-over is not reported by walk, so the cursor is checked
// against the icon's place a few times a second.

const (
	previewDelay = 300 * time.Millisecond // the mouse rests on the icon this long
	previewKeep  = 400 * time.Millisecond // shown this long after the mouse left
	previewW     = 320                    // picture width, logical pixels
)

var procNotifyIconGetRect = syscall.NewLazyDLL("shell32.dll").NewProc("Shell_NotifyIconGetRect")

// notifyIconIdentifier is NOTIFYICONIDENTIFIER.
type notifyIconIdentifier struct {
	cbSize uint32
	hWnd   win.HWND
	uID    uint32
	guid   [16]byte
}

// trayIconRect is where the tray icon of the window hwnd is on the screen.
// walk adds its icon with ID 0.
func trayIconRect(hwnd win.HWND) (win.RECT, bool) {
	var r win.RECT
	if procNotifyIconGetRect.Find() != nil {
		return r, false
	}
	id := notifyIconIdentifier{hWnd: hwnd}
	id.cbSize = uint32(unsafe.Sizeof(id))
	hr, _, _ := procNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r)))
	return r, hr == 0 && r.Right > r.Left
}

func inRect(p win.POINT, r win.RECT) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

type trayPreview struct {
	w     *window
	mw    *walk.MainWindow
	img   *walk.ImageView
	lbl   *walk.Label
	bmp   *walk.Bitmap
	shown bool

	overSince time.Time // the mouse came onto the icon
	outSince  time.Time // the mouse left the icon and the preview
	path      string    // the file shown
	done      int64     // its bytes when the picture was made
}

func (p *trayPreview) create() error {
	err := MainWindow{
		AssignTo: &p.mw,
		Title:    "ASIAIR Agent — кадр",
		Visible:  false,
		Layout:   VBox{Margins: Margins{Left: 6, Top: 6, Right: 6, Bottom: 6}, Spacing: 4},
		Children: []Widget{
			ImageView{AssignTo: &p.img, Mode: ImageViewModeZoom, Background: SolidColorBrush{Color: walk.RGB(16, 16, 16)}},
			Label{AssignTo: &p.lbl},
		},
	}.Create()
	if err != nil {
		return err
	}
	h := p.mw.Handle()
	style := uint32(win.WS_POPUP | win.WS_BORDER)
	win.SetWindowLong(h, win.GWL_STYLE, int32(style))
	win.SetWindowLong(h, win.GWL_EXSTYLE, win.WS_EX_TOOLWINDOW|win.WS_EX_TOPMOST|win.WS_EX_NOACTIVATE)
	return nil
}

// tick runs a few times a second on the window thread.
func (p *trayPreview) tick() {
	if p.mw == nil {
		return
	}
	var cur win.POINT
	win.GetCursorPos(&cur)
	icon, ok := trayIconRect(p.w.mw.Handle())
	over := ok && inRect(cur, icon)
	var self win.RECT
	if p.shown {
		win.GetWindowRect(p.mw.Handle(), &self)
	}
	now := time.Now()
	switch {
	case over:
		p.outSince = time.Time{}
		if p.overSince.IsZero() {
			p.overSince = now
		}
	case p.shown && inRect(cur, self):
		p.outSince = time.Time{}
	default:
		p.overSince = time.Time{}
		if p.outSince.IsZero() {
			p.outSince = now
		}
	}
	var up *agent.UploadPreview
	if a, _ := p.w.run.state(); a != nil {
		up = a.Preview()
	}
	switch {
	case up == nil || !p.shown && (!over || now.Sub(p.overSince) < previewDelay):
		p.hide()
	case !over && p.shown && now.Sub(p.outSince) > previewKeep && !p.outSince.IsZero():
		p.hide()
	default:
		p.show(up, icon)
	}
}

func (p *trayPreview) hide() {
	if !p.shown {
		return
	}
	p.shown = false
	win.ShowWindow(p.mw.Handle(), win.SW_HIDE)
	p.w.update() // the tooltip comes back
}

func (p *trayPreview) show(up *agent.UploadPreview, icon win.RECT) {
	dpi := p.mw.DPI()
	scale := func(v int) int { return v * dpi / 96 }
	if up.Path != p.path || up.Done != p.done || p.bmp == nil {
		p.path, p.done = up.Path, up.Done
		pic := previewPicture(up)
		if bmp, err := walk.NewBitmapFromImageForDPI(pic, 96); err == nil {
			p.img.SetImage(bmp)
			if p.bmp != nil {
				p.bmp.Dispose()
			}
			p.bmp = bmp
		}
		p.lbl.SetText(previewText(up))
	}
	b := p.bmp.Size()
	iw := scale(previewW)
	ih := iw * b.Height / max(b.Width, 1)
	ww, wh := iw+scale(14), ih+scale(14)+scale(4)+scale(52)
	// above the icon (taskbar at the bottom) or below it, inside the screen
	var mi win.MONITORINFO
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	work := win.RECT{Left: 0, Top: 0, Right: 1 << 15, Bottom: 1 << 15}
	if mon := monitorAt(win.POINT{X: icon.Left, Y: icon.Top}); mon != 0 && win.GetMonitorInfo(mon, &mi) {
		work = mi.RcWork
	}
	x := int(icon.Right) - ww
	y := int(icon.Top) - wh - scale(8)
	if y < int(work.Top) {
		y = int(icon.Bottom) + scale(8)
	}
	x = max(int(work.Left), min(x, int(work.Right)-ww))
	y = max(int(work.Top), min(y, int(work.Bottom)-wh))
	win.SetWindowPos(p.mw.Handle(), win.HWND_TOPMOST, int32(x), int32(y), int32(ww), int32(wh), win.SWP_NOACTIVATE|win.SWP_SHOWWINDOW)
	if !p.shown {
		p.shown = true
		win.ShowWindow(p.mw.Handle(), win.SW_SHOWNOACTIVATE)
		p.w.ni.SetToolTip("") // the preview says it all
	}
}

var procMonitorFromPoint = syscall.NewLazyDLL("user32.dll").NewProc("MonitorFromPoint")

func monitorAt(pt win.POINT) win.HMONITOR {
	r, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, win.MONITOR_DEFAULTTONEAREST)
	return win.HMONITOR(r)
}

// previewPicture is the stretched frame, the part not sent yet dark.
func previewPicture(up *agent.UploadPreview) image.Image {
	if up.Image == nil {
		// not a FITS frame, or its first rows have not gone yet
		g := image.NewRGBA(image.Rect(0, 0, previewW, previewW*2/3))
		draw.Draw(g, g.Bounds(), &image.Uniform{color.RGBA{24, 24, 28, 255}}, image.Point{}, draw.Src)
		return g
	}
	part := up.Image.Stretched()
	w, full := up.Image.Width, max(up.Full, up.Image.Height)
	canvas := image.NewRGBA(image.Rect(0, 0, w, full))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{24, 24, 28, 255}}, image.Point{}, draw.Src)
	y0 := 0
	if up.FromBottom {
		y0 = full - up.Image.Height
	}
	draw.Draw(canvas, image.Rect(0, y0, w, y0+up.Image.Height), part, image.Point{}, draw.Src)
	return canvas
}

func previewText(up *agent.UploadPreview) string {
	pct := int64(0)
	if up.Size > 0 {
		pct = up.Done * 100 / up.Size
	}
	dir := path.Base(path.Dir(up.Path))
	text := fmt.Sprintf("%s\n%s · %d%% · %s из %s", path.Base(up.Path), dir, pct,
		config.FormatBytes(up.Done), config.FormatBytes(up.Size))
	if up.Others > 0 {
		text += fmt.Sprintf("\nодновременно передаётся ещё %d", up.Others)
	}
	return text
}
