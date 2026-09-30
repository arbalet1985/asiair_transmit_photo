package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// skyObject is where the telescope pointed when the frame was taken.
type skyObject struct {
	Name string
	moon.Pos
}

// frameSky is the Moon when the frame was taken (nil without a time) and,
// with the coordinates of the site and of the target in the header, the
// object. get reads the FITS header.
func frameSky(f *sorter.Frame, get func(string) string) (*moon.Info, *skyObject) {
	m, t := frameMoon(f, get)
	if m == nil || !m.HasPos {
		return m, nil
	}
	ra, dec, ok := moon.Target(get)
	if !ok {
		return m, nil
	}
	lat, lon, _ := moon.Site(get)
	name := strings.Trim(get("OBJECT"), "' ")
	if name == "" {
		name = f.Object
	}
	if name == "" {
		name = "объект"
	}
	return m, &skyObject{name, moon.Object(t, lat, lon, ra, dec)}
}

func frameMoon(f *sorter.Frame, get func(string) string) (*moon.Info, time.Time) {
	var t time.Time
	if s := strings.TrimSuffix(strings.Trim(get("DATE-OBS"), "' "), "Z"); s != "" {
		for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
			if v, err := time.ParseInLocation(l, s, time.UTC); err == nil {
				t = v
				break
			}
		}
	}
	if t.IsZero() && f.Time.Year() > 2000 {
		// the clock reading at the observatory, taken as this computer's zone
		t = time.Date(f.Time.Year(), f.Time.Month(), f.Time.Day(), f.Time.Hour(), f.Time.Minute(), f.Time.Second(), 0, time.Local)
	}
	if t.IsZero() {
		return nil, t
	}
	lat, lon, ok := moon.Site(get)
	m := moon.At(t, lat, lon, ok)
	return &m, t
}

// moonDisc draws the lit part of the Moon (lit on the right when waxing,
// as seen from the northern hemisphere) on the background bg.
func moonDisc(m *moon.Info, r int, bg color.RGBA) image.Image {
	im := image.NewRGBA(image.Rect(0, 0, 2*r+1, 2*r+1))
	dark, lit := color.RGBA{70, 75, 86, 255}, color.RGBA{243, 230, 176, 255}
	for y := 0; y <= 2*r; y++ {
		for x := 0; x <= 2*r; x++ {
			nx, ny := float64(x-r)/float64(r), float64(y-r)/float64(r)
			c := bg
			if nx*nx+ny*ny <= 1 {
				t := (1 - 2*m.Illum) * math.Sqrt(1-ny*ny) // the terminator
				c = dark
				if m.Waxing && nx > t || !m.Waxing && nx < -t {
					c = lit
				}
			}
			im.SetRGBA(x, y, c)
		}
	}
	return im
}

func phaseName(m *moon.Info) string {
	switch {
	case m.Illum <= 0.03:
		return "новолуние"
	case m.Illum >= 0.97:
		return "полнолуние"
	case m.Waxing:
		return "растущая"
	}
	return "убывающая"
}

// paintMoon draws the phase and, with the coordinates in the header, where
// the Moon was on a strip of the horizon С–В–Ю–З.
func (w *window) paintMoon(canvas *walk.Canvas, _ walk.Rectangle) error {
	cr := w.moonView.ClientBoundsPixels()
	bg := color.RGBA{16, 16, 16, 255}
	if b, err := walk.NewSolidColorBrush(walk.RGB(bg.R, bg.G, bg.B)); err == nil {
		canvas.FillRectanglePixels(b, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		b.Dispose()
	}
	font, fg := w.moonView.Font(), walk.RGB(200, 200, 200)
	text := func(s string, r walk.Rectangle, f walk.DrawTextFormat) {
		canvas.DrawTextPixels(s, font, fg, r, f|walk.TextSingleLine|walk.TextVCenter)
	}
	m := w.moon
	if m == nil {
		text("Луна: время съёмки кадра неизвестно", walk.Rectangle{X: 8, Width: cr.Width - 16, Height: cr.Height}, walk.TextLeft)
		return nil
	}
	dpi := float64(canvas.DPI()) / 96
	px := func(v float64) int { return int(math.Round(v * dpi)) }
	disc := func(r int, at walk.Point, opacity byte) {
		if bmp, err := walk.NewBitmapFromImageForDPI(moonDisc(m, r, bg), 96); err == nil {
			canvas.DrawBitmapWithOpacityPixels(bmp, walk.Rectangle{X: at.X - r, Y: at.Y - r, Width: 2*r + 1, Height: 2*r + 1}, opacity)
			bmp.Dispose()
		}
	}
	line := fmt.Sprintf("%s, освещена на %d%%", phaseName(m), int(math.Round(m.Illum*100)))
	if !m.HasPos {
		r := px(14)
		disc(r, walk.Point{X: px(8) + r, Y: cr.Height / 2}, 255)
		text(line+"; положение — в заголовке нет координат (SITELAT, SITELONG)",
			walk.Rectangle{X: px(16) + 2*r, Width: cr.Width - px(24) - 2*r, Height: cr.Height}, walk.TextLeft)
		return nil
	}
	alt := fmt.Sprintf("высота %d°", int(math.Round(m.Alt)))
	if m.Alt < 0 {
		alt = fmt.Sprintf("под горизонтом (%d°)", int(math.Round(m.Alt)))
	}
	lines := []string{"Луна: " + line + fmt.Sprintf("; %s, азимут %d° (%s)", alt, int(math.Round(m.Az)), moon.Compass(m.Az))}
	o := w.skyObj
	if o != nil {
		lines = append(lines, fmt.Sprintf("%s: высота %d°, азимут %d° (%s), до Луны %d°", o.Name, int(math.Round(o.Alt)),
			int(math.Round(o.Az)), moon.Compass(o.Az), int(math.Round(moon.Separation(o.Pos, moon.Pos{Alt: m.Alt, Az: m.Az})))))
	}
	lineH := px(17)
	for i, l := range lines {
		text(l, walk.Rectangle{X: px(8), Y: cr.Height - (len(lines)-i)*lineH, Width: cr.Width - px(16), Height: lineH}, walk.TextLeft|walk.TextEndEllipsis)
	}
	lineH *= len(lines)

	// the strip: 0..360° of azimuth from the north, the horizon, 90° above it
	x0, x1 := px(16), cr.Width-px(16)
	horizon := cr.Height - lineH - px(18)
	up := horizon - px(10)
	X := func(az float64) int { return x0 + int(az/360*float64(x1-x0)) }
	Y := func(a float64) int { return horizon - int(math.Max(a, -20)/90*float64(up)) }
	if pen, err := walk.NewCosmeticPen(walk.PenSolid, walk.RGB(110, 110, 110)); err == nil {
		canvas.DrawLinePixels(pen, walk.Point{X: x0, Y: horizon}, walk.Point{X: x1, Y: horizon})
		for i, name := range []string{"С", "СВ", "В", "ЮВ", "Ю", "ЮЗ", "З", "СЗ", "С"} {
			x := X(float64(i) * 45)
			canvas.DrawLinePixels(pen, walk.Point{X: x, Y: horizon}, walk.Point{X: x, Y: horizon + px(4)})
			text(name, walk.Rectangle{X: x - px(15), Y: horizon + px(3), Width: px(30), Height: px(15)}, walk.TextCenter)
		}
		pen.Dispose()
	}
	op := byte(255)
	if m.Alt < 0 {
		op = 110
	}
	disc(px(8), walk.Point{X: X(m.Az), Y: Y(m.Alt)}, op)
	if o != nil {
		// the object: a diamond with its name
		x, y, r := X(o.Az), Y(o.Alt), px(6)
		col := walk.RGB(90, 200, 255)
		if o.Alt < 0 {
			col = walk.RGB(60, 110, 140)
		}
		if pen, err := walk.NewCosmeticPen(walk.PenSolid, col); err == nil {
			for i := 0; i <= r; i++ { // filled: nested outlines
				canvas.DrawPolylinePixels(pen, []walk.Point{{X: x, Y: y - i}, {X: x + i, Y: y}, {X: x, Y: y + i}, {X: x - i, Y: y}, {X: x, Y: y - i}})
			}
			pen.Dispose()
		}
		lx := x + r + px(3)
		if lx+px(90) > cr.Width {
			lx = x - r - px(93)
		}
		canvas.DrawTextPixels(o.Name, font, col, walk.Rectangle{X: lx, Y: y - px(8), Width: px(90), Height: px(16)}, walk.TextSingleLine|walk.TextVCenter|walk.TextEndEllipsis)
	}
	return nil
}

func (w *window) moonToggled() {
	w.moonView.SetVisible(w.moonBox.Checked())
	w.mu.Lock()
	w.saved.ShowMoon = w.moonBox.Checked()
	w.mu.Unlock()
}
