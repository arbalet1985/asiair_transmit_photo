package main

import (
	"fmt"
	"math"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// zoomView draws the preview fitted to its place or closer: the buttons
// and the mouse wheel zoom, the mouse drags the picture. The zoom and the
// point looked at stay when another frame is selected.
type zoomView struct {
	cw     *walk.CustomWidget
	bmp    *walk.Bitmap
	w, h   int     // pixels of the bitmap
	srcW   int     // width of the frame on the sensor
	scale  float64 // 1: fitted; more is closer
	cx, cy float64 // the point of the frame (0..1) in the middle when closer

	dragging       bool
	dragX, dragY   int
	dragCx, dragCy float64

	// changed is called after the zoom changed
	changed func()
	// overlay draws over the picture placed at x0, y0, dw × dh
	overlay func(c *walk.Canvas, x0, y0, dw, dh float64)
}

func newZoomView() *zoomView { return &zoomView{scale: 1, cx: 0.5, cy: 0.5} }

// setImage shows another picture, keeping the zoom; the old bitmap is freed.
func (z *zoomView) setImage(bmp *walk.Bitmap, w, h, srcW int) {
	if z.bmp != nil && z.bmp != bmp {
		z.bmp.Dispose()
	}
	z.bmp, z.w, z.h, z.srcW = bmp, w, h, srcW
	if srcW <= 0 {
		z.srcW = w
	}
	z.cw.Invalidate()
}

// fit: screen pixels per bitmap pixel for the picture fitted.
func (z *zoomView) fit() float64 {
	if z.w == 0 || z.h == 0 {
		return 0
	}
	cr := z.cw.ClientBoundsPixels()
	return math.Min(float64(cr.Width)/float64(z.w), float64(cr.Height)/float64(z.h))
}

// maxScale: up to 4 screen pixels per pixel of the frame, at least twice fitted.
func (z *zoomView) maxScale() float64 {
	f := z.fit()
	if f == 0 {
		return 8
	}
	return math.Max(2, 4*float64(z.srcW)/(float64(z.w)*f))
}

// percent: 100 is a pixel of the frame in a pixel of the screen.
func (z *zoomView) percent() int {
	f := z.fit()
	if f == 0 || z.srcW == 0 {
		return 0
	}
	return int(math.Round(100 * float64(z.w) * f * z.scale / float64(z.srcW)))
}

func (z *zoomView) label() string {
	if z.scale <= 1 {
		return "вписано"
	}
	return fmt.Sprintf("%d%%", z.percent())
}

func (z *zoomView) setScale(s float64) {
	s = math.Min(math.Max(s, 1), z.maxScale())
	if s < 1.05 {
		s = 1
	}
	if s == 1 {
		z.cx, z.cy = 0.5, 0.5
	}
	z.scale = s
	z.cw.Invalidate()
	if z.changed != nil {
		z.changed()
	}
}

func (z *zoomView) zoomIn()  { z.setScale(z.scale * 1.5) }
func (z *zoomView) zoomOut() { z.setScale(z.scale / 1.5) }
func (z *zoomView) fitAll()  { z.setScale(1) }

// actual: a pixel of the frame in a pixel of the screen.
func (z *zoomView) actual() {
	if f := z.fit(); f > 0 {
		z.setScale(float64(z.srcW) / (float64(z.w) * f))
	}
}

// place: where the picture is drawn, in pixels of the widget.
func (z *zoomView) place() (x0, y0, dw, dh float64) {
	cr := z.cw.ClientBoundsPixels()
	s := z.fit() * z.scale
	dw, dh = float64(z.w)*s, float64(z.h)*s
	cw, ch := float64(cr.Width), float64(cr.Height)
	// the middle point is kept inside, so no empty strip shows at an edge
	if dw > cw {
		z.cx = math.Min(math.Max(z.cx, cw/2/dw), 1-cw/2/dw)
		x0 = cw/2 - z.cx*dw
	} else {
		x0 = (cw - dw) / 2
	}
	if dh > ch {
		z.cy = math.Min(math.Max(z.cy, ch/2/dh), 1-ch/2/dh)
		y0 = ch/2 - z.cy*dh
	} else {
		y0 = (ch - dh) / 2
	}
	return
}

func (z *zoomView) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	cr := z.cw.ClientBoundsPixels()
	if b, err := walk.NewSolidColorBrush(walk.RGB(16, 16, 16)); err == nil {
		canvas.FillRectanglePixels(b, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		b.Dispose()
	}
	if z.bmp == nil || z.w == 0 || z.h == 0 {
		return nil
	}
	x0, y0, dw, dh := z.place()
	s := dw / float64(z.w)
	// only the part in view is stretched
	vx0, vy0 := math.Max(x0, 0), math.Max(y0, 0)
	vx1, vy1 := math.Min(x0+dw, float64(cr.Width)), math.Min(y0+dh, float64(cr.Height))
	if vx1 <= vx0 || vy1 <= vy0 {
		return nil
	}
	sx0, sy0 := math.Floor((vx0-x0)/s), math.Floor((vy0-y0)/s)
	sx1, sy1 := math.Min(math.Ceil((vx1-x0)/s), float64(z.w)), math.Min(math.Ceil((vy1-y0)/s), float64(z.h))
	src := walk.Rectangle{X: int(sx0), Y: int(sy0), Width: int(sx1 - sx0), Height: int(sy1 - sy0)}
	dst := walk.Rectangle{
		X: int(math.Round(x0 + sx0*s)), Y: int(math.Round(y0 + sy0*s)),
		Width: int(math.Round((sx1 - sx0) * s)), Height: int(math.Round((sy1 - sy0) * s)),
	}
	if err := canvas.DrawBitmapPart(z.bmp, dst, src); err != nil {
		return err
	}
	if z.overlay != nil {
		z.overlay(canvas, x0, y0, dw, dh)
	}
	return nil
}

func (z *zoomView) mouseDown(x, y int, b walk.MouseButton) {
	if b != walk.LeftButton || z.scale <= 1 {
		return
	}
	z.dragging, z.dragX, z.dragY, z.dragCx, z.dragCy = true, x, y, z.cx, z.cy
	z.cw.SetFocus() // the wheel goes to the focused window
}

func (z *zoomView) mouseMove(x, y int, b walk.MouseButton) {
	if !z.dragging || b&walk.LeftButton == 0 {
		z.dragging = false
		return
	}
	_, _, dw, dh := z.place()
	z.cx = z.dragCx - float64(x-z.dragX)/dw
	z.cy = z.dragCy - float64(y-z.dragY)/dh
	z.cw.Invalidate()
}

func (z *zoomView) mouseUp(int, int, walk.MouseButton) { z.dragging = false }

// wheel zooms keeping the point under the mouse in place.
func (z *zoomView) wheel(x, y int, b walk.MouseButton) {
	delta := int16(uint32(b) >> 16)
	if delta == 0 || z.bmp == nil {
		return
	}
	pt := win.POINT{X: int32(x), Y: int32(y)} // the wheel tells screen coordinates
	win.ScreenToClient(z.cw.Handle(), &pt)
	x0, y0, dw, dh := z.place()
	px, py := (float64(pt.X)-x0)/dw, (float64(pt.Y)-y0)/dh // the point under the mouse
	old := z.scale
	s := old * 1.25
	if delta < 0 {
		s = old / 1.25
	}
	s = math.Min(math.Max(s, 1), z.maxScale())
	if s == old {
		return
	}
	// the middle moves so the point stays under the mouse
	cr := z.cw.ClientBoundsPixels()
	k := s / old
	ndw, ndh := dw*k, dh*k
	z.cx = px - (float64(pt.X)-float64(cr.Width)/2)/ndw
	z.cy = py - (float64(pt.Y)-float64(cr.Height)/2)/ndh
	z.setScale(s)
}
