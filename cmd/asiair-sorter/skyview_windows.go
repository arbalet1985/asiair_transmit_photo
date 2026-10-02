package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// skyView draws the sky of the observatory as a strip — azimuth from the
// north through the east, south in the middle, altitude up to the zenith:
// the horizon and the closed zones (edited with the mouse), where the
// frames were taken, the paths of the targets this night, the Moon and,
// at the moment of the animation, the telescope.
type skyView struct {
	cw *walk.CustomWidget
	p  *plannerUI

	drag   int // what the mouse drags: dragNode, …
	node   int // the horizon point dragged
	zone   int // the zone dragged
	corner int // 0: Az1/Alt1, 1: Az2/Alt1, 2: Az1/Alt2, 3: Az2/Alt2
	start  planner.Point
	orig   planner.Zone

	bgKey string
	bg    *walk.Bitmap
}

const (
	dragNone = iota
	dragNode
	dragZone
	dragCorner
	dragNewZone
)

const altMin = -10.0 // the bottom of the strip

func (v *skyView) invalidate() {
	if v.cw != nil {
		v.cw.Invalidate()
	}
}

func (v *skyView) dpi() float64 { return float64(v.cw.DPI()) / 96 }

func (v *skyView) px(f float64) int { return int(math.Round(f * v.dpi())) }

// area: where the strip is drawn, in pixels of the widget.
func (v *skyView) area() (x0, y0, x1, y1 int) {
	cr := v.cw.ClientBoundsPixels()
	return v.px(30), v.px(20), cr.Width - v.px(8), cr.Height - v.px(18)
}

func (v *skyView) xy(az, alt float64) (int, int) {
	x0, y0, x1, y1 := v.area()
	az = math.Mod(az+360, 360)
	return x0 + int(math.Round(az/360*float64(x1-x0))), y1 - int(math.Round((alt-altMin)/(90-altMin)*float64(y1-y0)))
}

func (v *skyView) azAlt(x, y int) planner.Point {
	x0, y0, x1, y1 := v.area()
	az := float64(x-x0) / float64(max(x1-x0, 1)) * 360
	alt := altMin + float64(y1-y)/float64(max(y1-y0, 1))*(90-altMin)
	return planner.Point{Az: math.Min(math.Max(az, 0), 359.99), Alt: math.Min(math.Max(alt, altMin), 90)}
}

// palette of the targets
var targetColors = []walk.Color{
	walk.RGB(90, 200, 255), walk.RGB(255, 170, 60), walk.RGB(120, 230, 120), walk.RGB(240, 110, 200),
	walk.RGB(250, 230, 90), walk.RGB(170, 140, 255), walk.RGB(255, 110, 100), walk.RGB(100, 230, 210),
}

func targetColor(i int) walk.Color {
	if i < 0 {
		return walk.RGB(160, 160, 160)
	}
	return targetColors[i%len(targetColors)]
}

// skyColor: darker with the Sun lower.
func skyColor(sunAlt float64) color.RGBA {
	f := math.Min(math.Max((sunAlt+18)/18, 0), 1) // 0 at night, 1 at sunset
	mix := func(a, b float64) uint8 { return uint8(a + (b-a)*f) }
	return color.RGBA{mix(10, 70), mix(14, 95), mix(28, 150), 255}
}

// background: the sky, the ground under the horizon, the closed zones
// hatched; cached while nothing of it changes.
func (v *skyView) background(sky color.RGBA) *walk.Bitmap {
	cr := v.cw.ClientBoundsPixels()
	s := v.p.state.Sky
	key := fmt.Sprintf("%d %d %v %v %v %v %v", cr.Width, cr.Height, sky, s.Horizon, s.Zones, s.MinAlt, v.p.guess.Maybe)
	if v.bg != nil && key == v.bgKey {
		return v.bg
	}
	if v.bg != nil {
		v.bg.Dispose()
		v.bg = nil
	}
	x0, y0, x1, y1 := v.area()
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return nil
	}
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	sorted := s.Horizon.Sorted()
	ground, below := color.RGBA{58, 44, 32, 255}, color.RGBA{38, 30, 24, 255}
	for x := 0; x < w; x++ {
		az := (float64(x) + 0.5) / float64(w) * 360
		hz := math.Max(sorted.At(az), s.MinAlt)
		for y := 0; y < h; y++ {
			alt := altMin + float64(h-y)/float64(h)*(90-altMin)
			c := sky
			switch {
			case alt < 0:
				c = below
			case alt < hz:
				c = ground
			default:
				for _, z := range s.Zones {
					if z.Contains(az, alt) {
						c = color.RGBA{90, 34, 34, 255}
						if (x+y)%8 < 3 {
							c = color.RGBA{170, 60, 60, 255}
						}
						break
					}
				}
				if c == sky {
					for _, z := range v.p.guess.Maybe {
						if z.Contains(az, alt) && (x+y)%10 < 2 {
							c = color.RGBA{120, 100, 60, 255}
						}
					}
				}
			}
			im.SetRGBA(x, y, c)
		}
	}
	bmp, err := walk.NewBitmapFromImageForDPI(im, 96)
	if err != nil {
		return nil
	}
	v.bg, v.bgKey = bmp, key
	return bmp
}

func (v *skyView) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	p := v.p
	cr := v.cw.ClientBoundsPixels()
	if b, err := walk.NewSolidColorBrush(walk.RGB(16, 16, 16)); err == nil {
		canvas.FillRectanglePixels(b, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		b.Dispose()
	}
	x0, y0, x1, y1 := v.area()
	if x1-x0 < 50 || y1-y0 < 30 {
		return nil
	}
	lat, lon := p.state.Lat, p.state.Lon
	t := p.animT
	sunAlt := -90.0
	if !t.IsZero() {
		sunAlt = moon.Sun(t, lat, lon).Alt
	}
	sky := skyColor(sunAlt)
	if bg := v.background(sky); bg != nil {
		canvas.DrawImagePixels(bg, walk.Point{X: x0, Y: y0})
	}
	font := v.cw.Font()
	text := func(s string, c walk.Color, r walk.Rectangle, f walk.DrawTextFormat) {
		canvas.DrawTextPixels(s, font, c, r, f|walk.TextSingleLine|walk.TextVCenter)
	}
	pen := func(c walk.Color, width int, f func(walk.Pen)) {
		var pn walk.Pen
		var err error
		if width <= 1 {
			pn, err = walk.NewCosmeticPen(walk.PenSolid, c)
		} else {
			b, e := walk.NewSolidColorBrush(c)
			if e != nil {
				return
			}
			defer b.Dispose()
			pn, err = walk.NewGeometricPen(walk.PenSolid|walk.PenCapRound|walk.PenJoinRound, width, b)
		}
		if err != nil {
			return
		}
		f(pn)
		pn.Dispose()
	}
	dot := func(x, y, r int, c walk.Color) {
		if b, err := walk.NewSolidColorBrush(c); err == nil {
			canvas.FillEllipsePixels(b, walk.Rectangle{X: x - r, Y: y - r, Width: 2*r + 1, Height: 2*r + 1})
			b.Dispose()
		}
	}

	// the grid
	gray := walk.RGB(70, 80, 100)
	pen(gray, 1, func(pn walk.Pen) {
		for alt := 0.0; alt <= 90; alt += 15 {
			_, y := v.xy(0, alt)
			canvas.DrawLinePixels(pn, walk.Point{X: x0, Y: y}, walk.Point{X: x1, Y: y})
			text(fmt.Sprintf("%g°", alt), walk.RGB(160, 160, 160), walk.Rectangle{X: 0, Y: y - v.px(8), Width: x0 - v.px(3), Height: v.px(16)}, walk.TextRight)
		}
		for i, name := range []string{"С", "СВ", "В", "ЮВ", "Ю", "ЮЗ", "З", "СЗ", "С"} {
			x, _ := v.xy(float64(i)*45, 0)
			if i == 8 {
				x = x1
			}
			canvas.DrawLinePixels(pn, walk.Point{X: x, Y: y0}, walk.Point{X: x, Y: y1})
			text(name, walk.RGB(180, 180, 180), walk.Rectangle{X: x - v.px(15), Y: y1 + v.px(1), Width: v.px(30), Height: v.px(16)}, walk.TextCenter)
		}
	})

	// the frames taken
	if p.pointsBox != nil && p.pointsBox.Checked() {
		names := map[string]int{}
		for _, o := range p.guess.Points {
			k, ok := names[o.Object]
			if !ok {
				k = len(names)
				names[o.Object] = k
			}
			x, y := v.xy(o.Az, o.Alt)
			c := targetColor(k)
			dot(x, y, v.px(1.5), walk.RGB(c.R()/2+40, c.G()/2+40, c.B()/2+40))
		}
	}

	// the horizon and its points
	hz := p.state.Sky.Horizon.Sorted()
	pen(walk.RGB(230, 190, 120), v.px(2), func(pn walk.Pen) {
		var pts []walk.Point
		for az := 0.0; az <= 360; az += 1 {
			x, y := v.xy(az, math.Max(hz.At(az), p.state.Sky.MinAlt))
			if az == 360 {
				x = x1
			}
			pts = append(pts, walk.Point{X: x, Y: y})
		}
		canvas.DrawPolylinePixels(pn, pts)
	})
	for _, pt := range hz {
		x, y := v.xy(pt.Az, pt.Alt)
		r := v.px(3)
		if b, err := walk.NewSolidColorBrush(walk.RGB(255, 220, 150)); err == nil {
			canvas.FillRectanglePixels(b, walk.Rectangle{X: x - r, Y: y - r, Width: 2*r + 1, Height: 2*r + 1})
			b.Dispose()
		}
	}
	// the zones: outlines and corners
	for _, z := range p.state.Sky.Zones {
		v.zoneOutline(canvas, z, walk.RGB(230, 90, 90))
	}

	// the night: paths of the targets, thick where they are shot
	if !p.dusk.IsZero() {
		for i, tg := range p.state.Targets {
			if tg.Off {
				continue
			}
			c := targetColor(i)
			at := func(u time.Time) moon.Pos { return moon.Object(u, lat, lon, tg.RA, tg.Dec) }
			pen(c, 1, func(pn walk.Pen) { v.track(canvas, pn, p.dusk, p.dawn, 5*time.Minute, at) })
			if pl := p.plan; pl != nil {
				pen(c, v.px(4), func(pn walk.Pen) {
					for _, s := range pl.Steps {
						if s.Kind == planner.Shoot && s.Target == i {
							v.track(canvas, pn, s.Start, s.End, 2*time.Minute, at)
						}
					}
				})
			}
			// the name at the highest point
			best, bt := -90.0, p.dusk
			for u := p.dusk; !u.After(p.dawn); u = u.Add(10 * time.Minute) {
				if a := at(u).Alt; a > best {
					best, bt = a, u
				}
			}
			if best > altMin {
				x, y := v.xy(at(bt).Az, best)
				text(tg.Name, c, walk.Rectangle{X: x - v.px(60), Y: y - v.px(20), Width: v.px(120), Height: v.px(16)}, walk.TextCenter)
			}
		}
		// the path of the Moon in the night, hourly
		for u := p.dusk; !u.After(p.dawn); u = u.Add(30 * time.Minute) {
			m := moon.At(u, lat, lon, true)
			if m.Alt > altMin {
				x, y := v.xy(m.Az, m.Alt)
				dot(x, y, v.px(1.5), walk.RGB(150, 150, 130))
			}
		}
	}

	if !t.IsZero() {
		// the Moon now, with its phase
		m := moon.At(t, lat, lon, true)
		if m.Alt > altMin {
			x, y := v.xy(m.Az, m.Alt)
			r := v.px(9)
			op := byte(255)
			if m.Alt < 0 {
				op = 110
			}
			if bmp, err := walk.NewBitmapFromImageForDPI(moonDisc(&m, r, sky), 96); err == nil {
				canvas.DrawBitmapWithOpacityPixels(bmp, walk.Rectangle{X: x - r, Y: y - r, Width: 2*r + 1, Height: 2*r + 1}, op)
				bmp.Dispose()
			}
		}
		if sunAlt > altMin {
			sp := moon.Sun(t, lat, lon)
			x, y := v.xy(sp.Az, sp.Alt)
			dot(x, y, v.px(8), walk.RGB(255, 210, 80))
		}
	}

	// the telescope
	line := ""
	if !t.IsZero() {
		line = fmt.Sprintf("%s  Солнце %d°", t.In(p.params().Zone).Format("15:04"), int(math.Round(sunAlt)))
	}
	if pl := p.plan; pl != nil && !t.IsZero() {
		st := pl.At(t)
		x, y := v.xy(st.Point.Az, st.Point.Alt)
		r := v.px(10)
		c := walk.RGB(255, 255, 255)
		pen(c, v.px(2), func(pn walk.Pen) {
			canvas.DrawEllipsePixels(pn, walk.Rectangle{X: x - r, Y: y - r, Width: 2 * r, Height: 2 * r})
			canvas.DrawLinePixels(pn, walk.Point{X: x - r - v.px(5), Y: y}, walk.Point{X: x - r + v.px(4), Y: y})
			canvas.DrawLinePixels(pn, walk.Point{X: x + r - v.px(4), Y: y}, walk.Point{X: x + r + v.px(5), Y: y})
			canvas.DrawLinePixels(pn, walk.Point{X: x, Y: y - r - v.px(5)}, walk.Point{X: x, Y: y - r + v.px(4)})
			canvas.DrawLinePixels(pn, walk.Point{X: x, Y: y + r - v.px(4)}, walk.Point{X: x, Y: y + r + v.px(5)})
		})
		what := "телескоп в парковке"
		if st.Step >= 0 {
			s := pl.Steps[st.Step]
			what = planner.KindLabel[s.Kind]
			if s.Target >= 0 {
				tg := pl.Targets[s.Target]
				what += " " + tg.Name
				if s.Kind == planner.Shoot {
					what += fmt.Sprintf(", %g с", tg.Exposure)
				}
				what += fmt.Sprintf(", высота %d°", int(math.Round(st.Point.Alt)))
				mn := moon.At(t, lat, lon, true)
				what += fmt.Sprintf(", до Луны %d°", int(math.Round(moon.Separation(moon.Pos{Alt: st.Point.Alt, Az: st.Point.Az}, moon.Pos{Alt: mn.Alt, Az: mn.Az}))))
			}
			if s.Kind == planner.Shoot || s.Kind == planner.Refocus || s.Kind == planner.Flip {
				if pl.Params.Flip {
					if st.West {
						what += "; после перекладки"
					} else {
						what += "; до перекладки"
					}
				}
			}
		}
		line += "  ·  " + what
		lx := x + r + v.px(4)
		if lx+v.px(110) > x1 {
			lx = x - r - v.px(114)
		}
		label := "телескоп"
		if st.Target >= 0 {
			label = pl.Targets[st.Target].Name
		}
		text(label, c, walk.Rectangle{X: lx, Y: y - v.px(8), Width: v.px(110), Height: v.px(16)}, walk.TextEndEllipsis)
	}
	if line != "" {
		text(line, walk.RGB(220, 220, 220), walk.Rectangle{X: x0, Y: 0, Width: x1 - x0, Height: y0}, walk.TextLeft|walk.TextEndEllipsis)
	}
	return nil
}

// track draws the path of an object from a to b, broken where it goes
// through the north (the edges of the strip) or below the strip.
func (v *skyView) track(canvas *walk.Canvas, pn walk.Pen, a, b time.Time, step time.Duration, at func(time.Time) moon.Pos) {
	var pts []walk.Point
	lastAz := math.NaN()
	flush := func() {
		if len(pts) > 1 {
			canvas.DrawPolylinePixels(pn, pts)
		}
		pts = pts[:0]
	}
	for u := a; ; u = u.Add(step) {
		if u.After(b) {
			u = b
		}
		q := at(u)
		if q.Alt < altMin || !math.IsNaN(lastAz) && math.Abs(q.Az-lastAz) > 180 {
			flush()
		}
		lastAz = q.Az
		if q.Alt >= altMin {
			x, y := v.xy(q.Az, q.Alt)
			pts = append(pts, walk.Point{X: x, Y: y})
		}
		if !u.Before(b) {
			break
		}
	}
	flush()
}

func (v *skyView) zoneOutline(canvas *walk.Canvas, z planner.Zone, c walk.Color) {
	pn, err := walk.NewCosmeticPen(walk.PenSolid, c)
	if err != nil {
		return
	}
	defer pn.Dispose()
	rect := func(a1, a2 float64) {
		xa, ya := v.xy(a1, z.Alt2)
		xb, yb := v.xy(a2, z.Alt1)
		if a2 >= 360 {
			_, _, xb, _ = v.area()
		}
		canvas.DrawRectanglePixels(pn, walk.Rectangle{X: xa, Y: ya, Width: xb - xa, Height: yb - ya})
	}
	if z.Az1 <= z.Az2 {
		rect(z.Az1, z.Az2)
	} else {
		rect(z.Az1, 360)
		rect(0, z.Az2)
	}
}

// the mouse

func (v *skyView) near(x, y int, az, alt float64) bool {
	px, py := v.xy(az, alt)
	d := v.px(7)
	return abs(px-x) <= d && abs(py-y) <= d
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func (v *skyView) nodeAt(x, y int) int {
	for i, pt := range v.p.state.Sky.Horizon {
		if v.near(x, y, pt.Az, pt.Alt) {
			return i
		}
	}
	return -1
}

func corners(z planner.Zone) [4]planner.Point {
	return [4]planner.Point{{Az: z.Az1, Alt: z.Alt1}, {Az: z.Az2, Alt: z.Alt1}, {Az: z.Az1, Alt: z.Alt2}, {Az: z.Az2, Alt: z.Alt2}}
}

func (v *skyView) mouseDown(x, y int, b walk.MouseButton) {
	p := v.p
	sky := &p.state.Sky
	pt := v.azAlt(x, y)
	v.drag = dragNone
	if b == walk.RightButton {
		if i := v.nodeAt(x, y); i >= 0 {
			sky.Horizon = append(sky.Horizon[:i], sky.Horizon[i+1:]...)
		} else if i := zoneAt(sky.Zones, pt); i >= 0 {
			sky.Zones = append(sky.Zones[:i], sky.Zones[i+1:]...)
		} else if i := zoneAt(p.guess.Maybe, pt); i >= 0 {
			p.guess.Maybe = append(p.guess.Maybe[:i], p.guess.Maybe[i+1:]...)
		} else {
			return
		}
		p.state.SkySet = true
		p.skyChanged()
		return
	}
	if b != walk.LeftButton {
		return
	}
	v.start = pt
	if i := v.nodeAt(x, y); i >= 0 {
		v.drag, v.node = dragNode, i
		return
	}
	for i, z := range sky.Zones {
		for k, c := range corners(z) {
			if v.near(x, y, c.Az, c.Alt) {
				v.drag, v.zone, v.corner = dragCorner, i, k
				return
			}
		}
	}
	if walk.ModifiersDown()&walk.ModShift != 0 {
		sky.Zones = append(sky.Zones, planner.Zone{Az1: pt.Az, Az2: pt.Az, Alt1: pt.Alt, Alt2: pt.Alt})
		v.drag, v.zone = dragNewZone, len(sky.Zones)-1
		return
	}
	if i := zoneAt(sky.Zones, pt); i >= 0 {
		v.drag, v.zone, v.orig = dragZone, i, sky.Zones[i]
		return
	}
	if i := zoneAt(p.guess.Maybe, pt); i >= 0 {
		z := p.guess.Maybe[i]
		z.Auto = false // accepted by hand
		sky.Zones = append(sky.Zones, z)
		p.guess.Maybe = append(p.guess.Maybe[:i], p.guess.Maybe[i+1:]...)
		p.state.SkySet = true
		p.skyChanged()
		return
	}
	// a new point of the horizon, dragged at once
	sky.Horizon = append(sky.Horizon, planner.Point{Az: pt.Az, Alt: math.Max(pt.Alt, 0), Manual: true})
	v.drag, v.node = dragNode, len(sky.Horizon)-1
	v.invalidate()
}

func zoneAt(zs []planner.Zone, pt planner.Point) int {
	for i := len(zs) - 1; i >= 0; i-- {
		if zs[i].Contains(pt.Az, pt.Alt) {
			return i
		}
	}
	return -1
}

func (v *skyView) mouseMove(x, y int, b walk.MouseButton) {
	if v.drag == dragNone {
		return
	}
	if b&walk.LeftButton == 0 {
		v.mouseUp(x, y, b)
		return
	}
	sky := &v.p.state.Sky
	pt := v.azAlt(x, y)
	pt.Alt = math.Max(pt.Alt, 0)
	pt.Manual = true
	switch v.drag {
	case dragNode:
		if v.node < len(sky.Horizon) {
			sky.Horizon[v.node] = pt
		}
	case dragNewZone:
		z := &sky.Zones[v.zone]
		z.Az1, z.Az2 = math.Min(v.start.Az, pt.Az), math.Max(v.start.Az, pt.Az)
		z.Alt1, z.Alt2 = math.Min(v.start.Alt, pt.Alt), math.Max(v.start.Alt, pt.Alt)
	case dragCorner:
		z := &sky.Zones[v.zone]
		if v.corner%2 == 0 {
			z.Az1 = pt.Az
		} else {
			z.Az2 = pt.Az
		}
		if v.corner/2 == 0 {
			z.Alt1 = pt.Alt
		} else {
			z.Alt2 = pt.Alt
		}
		z.Auto = false
	case dragZone:
		z := &sky.Zones[v.zone]
		daz, dalt := pt.Az-v.start.Az, pt.Alt-v.start.Alt
		z.Az1, z.Az2 = math.Mod(v.orig.Az1+daz+360, 360), math.Mod(v.orig.Az2+daz+360, 360)
		z.Alt1, z.Alt2 = v.orig.Alt1+dalt, v.orig.Alt2+dalt
		z.Auto = false
	}
	v.invalidate()
}

func (v *skyView) mouseUp(int, int, walk.MouseButton) {
	if v.drag == dragNone {
		return
	}
	sky := &v.p.state.Sky
	if v.drag == dragCorner || v.drag == dragNewZone {
		z := &sky.Zones[v.zone]
		if z.Alt1 > z.Alt2 {
			z.Alt1, z.Alt2 = z.Alt2, z.Alt1
		}
		// a click with Shift, no drag: no zone
		if v.drag == dragNewZone && (math.Abs(z.Az2-z.Az1) < 0.5 || z.Alt2-z.Alt1 < 0.5) {
			sky.Zones = append(sky.Zones[:v.zone], sky.Zones[v.zone+1:]...)
		}
	}
	if v.drag == dragNode {
		sky.Horizon = sky.Horizon.Sorted()
	}
	v.drag = dragNone
	v.p.state.SkySet = true
	v.p.skyChanged()
}
