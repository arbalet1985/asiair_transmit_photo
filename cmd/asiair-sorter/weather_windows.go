package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/weather"
)

// The forecast of the clouds of the night chosen in the planner, from
// Open-Meteo: asked only while the planner is shown, and kept in the folder
// of the settings for 3 hours (weather.Cache), so it is asked a few times a
// day at most however often the sorter is started.

var wxCache *weather.Cache // made when first needed (on the window's thread)

// weatherNeeded asks for the forecast of the night chosen when the planner
// is shown and the one held does not cover the site.
func (p *plannerUI) weatherNeeded() {
	if !p.ready || p.w.tabs == nil || p.w.tabs.CurrentIndex() != plannerTab {
		return
	}
	if p.dusk.IsZero() {
		p.showWeather()
		return
	}
	// Open-Meteo tells from yesterday to 7 days ahead
	if now := time.Now(); p.dawn.Before(now.Add(-24*time.Hour)) || p.dusk.After(now.Add(7*24*time.Hour)) {
		p.showWeather()
		return
	}
	lat, lon := p.latEdit.Value(), p.lonEdit.Value()
	key := fmt.Sprintf("%.2f|%.2f", lat, lon)
	if p.wxBusy || key == p.wxKey && time.Since(p.wxAt) < 30*time.Minute {
		p.showWeather()
		return
	}
	p.wxBusy, p.wxKey, p.wxAt = true, key, time.Now()
	if wxCache == nil {
		wxCache = &weather.Cache{File: filepath.Join(configDir(), "weather.json"), UserAgent: "asiair-sorter/" + protocol.Version}
	}
	cache := wxCache
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Прогноз облачности: %v", r)
			}
		}()
		f, err := cache.Get(context.Background(), lat, lon)
		p.w.mw.Synchronize(func() {
			p.wxBusy = false
			p.wx, p.wxErr = f, ""
			if err != nil {
				p.wxErr = err.Error()
			}
			p.showWeather()
		})
	}()
}

// showWeather writes the summary of the night and draws the strip again.
func (p *plannerUI) showWeather() {
	if p.wxInfo == nil {
		return
	}
	hours := p.wx.Between(p.dusk, p.dawn)
	switch {
	case p.dusk.IsZero():
		setLong(p.wxInfo, "")
	case len(hours) == 0 && p.wxErr != "":
		setLong(p.wxInfo, "Прогноз облачности не получен: "+p.wxErr)
	case len(hours) == 0:
		setLong(p.wxInfo, "Прогноза облачности на эту ночь нет: Open-Meteo даёт его на 7 дней вперёд.")
	default:
		zone := p.params().Zone
		t := "Облачность: " + weather.Summary(hours, p.dusk, p.dawn, zone)
		if c := weather.Clear(hours, p.dusk, p.dawn); c > 0 {
			t += " Ясного неба " + planner.Hours(c*3600) + "."
		} else {
			t += " Ясного неба нет."
		}
		t += " (прогноз Open-Meteo от " + p.wx.Fetched.Local().Format("02.01 15:04") + ")"
		if p.wxErr != "" {
			t += " Обновить не удалось: " + p.wxErr
		}
		setLong(p.wxInfo, t)
	}
	p.wxView.SetVisible(len(hours) > 0)
	p.wxView.Invalidate()
}

// paintWeather: an hour a cell, the darker the cloudier, the time of the
// animation as a line.
func (p *plannerUI) paintWeather(c *walk.Canvas, _ walk.Rectangle) error {
	cr := p.wxView.ClientBoundsPixels()
	W, H := float64(cr.Width), float64(cr.Height)
	bg, _ := walk.NewSolidColorBrush(walk.RGB(255, 255, 255))
	if bg != nil {
		c.FillRectanglePixels(bg, walk.Rectangle{Width: cr.Width, Height: cr.Height})
		bg.Dispose()
	}
	hours := p.wx.Between(p.dusk, p.dawn)
	if len(hours) == 0 || p.dusk.IsZero() {
		return nil
	}
	span := p.dawn.Sub(p.dusk).Seconds()
	x := func(t time.Time) float64 {
		s := math.Min(math.Max(t.Sub(p.dusk).Seconds(), 0), span)
		return s / span * W
	}
	bh := H - 16
	font, _ := walk.NewFont("Segoe UI", 8, 0)
	if font != nil {
		defer font.Dispose()
	}
	zone := p.params().Zone
	for _, h := range hours {
		t0 := time.UnixMilli(h.T)
		x0, x1 := x(t0), x(t0.Add(time.Hour))
		if x1 <= x0 {
			continue
		}
		// clear: pale blue; overcast: grey
		k := float64(h.Cloud) / 100
		col := walk.RGB(byte(200-90*k), byte(222-112*k), byte(250-130*k))
		if b, err := walk.NewSolidColorBrush(col); err == nil {
			c.FillRectanglePixels(b, walk.Rectangle{X: int(x0), Y: 0, Width: int(math.Ceil(x1 - x0)), Height: int(bh)})
			b.Dispose()
		}
		if font != nil && x1-x0 > 28 {
			tc := walk.RGB(40, 40, 40)
			if h.Cloud > 50 {
				tc = walk.RGB(255, 255, 255)
			}
			c.DrawTextPixels(fmt.Sprintf("%d%%", h.Cloud), font, tc, walk.Rectangle{X: int(x0), Y: 0, Width: int(x1 - x0), Height: int(bh)},
				walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
	}
	// round hours under the strip
	step := time.Hour
	if p.dawn.Sub(p.dusk) > 8*time.Hour {
		step = 2 * time.Hour
	}
	for t := p.dusk.In(zone).Truncate(time.Hour).Add(time.Hour); t.Before(p.dawn); t = t.Add(time.Hour) {
		if font == nil || t.In(zone).Hour()%int(step.Hours()) != 0 {
			continue
		}
		c.DrawTextPixels(t.In(zone).Format("15:04"), font, walk.RGB(100, 100, 100),
			walk.Rectangle{X: int(x(t)) - 25, Y: int(bh), Width: 50, Height: 16}, walk.TextCenter|walk.TextSingleLine)
	}
	if !p.animT.IsZero() && !p.animT.Before(p.dusk) && !p.animT.After(p.dawn) {
		if pen, err := walk.NewCosmeticPen(walk.PenSolid, walk.RGB(0, 0, 0)); err == nil {
			xx := int(x(p.animT))
			c.DrawLinePixels(pen, walk.Point{X: xx, Y: 0}, walk.Point{X: xx, Y: int(bh)})
			pen.Dispose()
		}
	}
	return nil
}
