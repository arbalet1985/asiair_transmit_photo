package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/atlas"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The atlas of the planner opens in the browser: a page of this program on
// 127.0.0.1 (internal/atlas) shows the targets of the planner with the
// field of view of the optics, and its framing changes them here.

// openAtlas starts the page (once) and opens it.
func (p *plannerUI) openAtlas() { p.showAtlas(true) }

// showAtlas starts the page and opens it in a window of its own (or the
// browser): always, or (always false) only when no page of it is open.
func (p *plannerUI) showAtlas(always bool) {
	p.ensureAtlas()
	p.atlasChanged()
	u, err := p.atlas.Start()
	if err != nil {
		p.w.warn("Атлас не открыт: " + err.Error())
		return
	}
	if always || !p.atlas.Watched() {
		if b := openAppWindow(u); b != "" {
			log.Printf("Атлас открыт отдельным окном (%s)", b)
		} else {
			log.Printf("Атлас открыт в браузере: Edge и другие браузеры на Chromium не найдены")
		}
	}
}

// ensureAtlas makes the server of the page (once), not started yet.
func (p *plannerUI) ensureAtlas() {
	if p.atlas == nil {
		s := atlas.NewServer()
		s.OnFrame = func(i int, ra, dec, angle float64) {
			p.w.mw.Synchronize(func() {
				if i < 0 || i >= len(p.state.Targets) {
					return
				}
				t := &p.state.Targets[i]
				t.RA, t.Dec, t.Angle = ra, dec, angle
				p.renumber()
				p.targetTable.SetCurrentIndex(i)
				p.targetChosen()
				setLong(p.planInfo, fmt.Sprintf("%s: рамка %s %s, угол %g° — составьте план заново", t.Name, formatRA(ra), formatDec(dec), angle))
			})
		}
		s.OnOptics = func(o planner.Optics) {
			p.w.mw.Synchronize(func() {
				p.state.Optics = &o
				p.atlasChanged()
			})
		}
		s.OnFavorite = func(f planner.Favorite, on bool) {
			p.w.mw.Synchronize(func() {
				if (favIndex(p.state.Favorites, f.Name) >= 0) == on {
					return
				}
				p.toggleFavorite(f)
				p.syncFavorites(func(msg string) {
					if msg != "" {
						setLong(p.planInfo, msg)
					}
					p.atlasChanged()
				})
			})
		}
		s.OnAdd = func(f planner.Favorite) {
			p.w.mw.Synchronize(func() { setLong(p.planInfo, p.addObject(f)) })
		}
		s.OnSelect = func(i int) {
			p.w.mw.Synchronize(func() {
				if i >= 0 && i < len(p.state.Targets) && i != p.targetTable.CurrentIndex() {
					p.targetTable.SetCurrentIndex(i)
				}
			})
		}
		p.atlas = s
	}
}

// fromFrame takes a Light frame to the planner: its target with the centre
// and the camera angle of the frame (added, or the one of that name moved),
// the frame laid over the sky of the atlas and the framing started there.
// The frame is read in the background.
func (p *plannerUI) fromFrame(f *sorter.Frame, local string) {
	setLong(p.planInfo, "Читаю кадр для кадрирования…")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Кадрирование по кадру: сбой: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		fr, err := sorter.Framing(ctx, f, local)
		p.w.mw.Synchronize(func() {
			if err != nil {
				setLong(p.planInfo, "Кадр не перенесён в планировщик: "+err.Error())
				return
			}
			ra, dec, angle := math.Round(fr.RA*1e5)/1e5, math.Round(fr.Dec*1e5)/1e5, math.Round(fr.Angle*10)/10
			i := -1
			for k, t := range p.state.Targets {
				if t.Name == fr.Name {
					i = k
				}
			}
			if i >= 0 {
				t := &p.state.Targets[i]
				t.RA, t.Dec, t.Angle = ra, dec, angle
			} else {
				exp := fr.Exposure
				if exp <= 0 {
					exp = 300
				}
				p.state.Targets = append(p.state.Targets, planner.Target{Name: fr.Name, RA: ra, Dec: dec, Angle: angle, Exposure: exp, Filter: fr.Filter})
				i = len(p.state.Targets) - 1
			}
			p.renumber()
			p.targetTable.SetCurrentIndex(i)
			p.targetChosen()
			p.ensureAtlas()
			p.atlas.SetPhoto(fr.JPG)
			p.photoV++
			p.photo = &atlas.Photo{V: p.photoV, Name: filepath.Base(filepath.FromSlash(f.Path)), WCS: fr.WCS, Width: fr.Width, Height: fr.Height}
			p.frameReq++
			p.frameI = i
			p.showAtlas(false)
			how := "по решению кадра"
			if fr.Approx {
				how = "по наведению (кадр не решён — центр может отличаться)"
			}
			setLong(p.planInfo, fmt.Sprintf("%s: центр и угол взяты из кадра %s; снимок — поверх неба в атласе", fr.Name, how))
		})
	}()
}

// atlasChanged gives the open atlas the targets, the one chosen and the
// optics.
func (p *plannerUI) atlasChanged() {
	if p.atlas == nil || !p.ready {
		return
	}
	st := atlas.State{Targets: append([]planner.Target(nil), p.state.Targets...), Sel: p.targetTable.CurrentIndex(),
		Optics: p.state.Optics, Setups: p.setups, Photo: p.photo, FrameReq: p.frameReq, FrameI: p.frameI,
		Lat: p.state.Lat, Lon: p.state.Lon, SiteSet: p.state.Lat != 0 || p.state.Lon != 0, TZ: p.tzEdit.Value(),
		Favorites: append([]planner.Favorite(nil), p.state.Favorites...)}
	sky := copySky(p.state.Sky)
	st.Sky = &sky
	for i := range st.Targets {
		c := targetColor(i)
		st.Colors = append(st.Colors, fmt.Sprintf("#%02x%02x%02x", c.R(), c.G(), c.B()))
	}
	p.atlas.Publish(st)
}

// receiverAddr: the receiver to ask: the input when it is one, else the
// last receiver used.
func (w *window) receiverAddr() string {
	if in := strings.TrimSpace(w.inEdit.Text()); sorter.IsURL(in) {
		return in
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.saved.RecentIn {
		if sorter.IsURL(a) {
			return a
		}
	}
	return ""
}

// skyFromReceiver takes the sky of the observatory (horizon, zones hidden
// by wires, site, clock) from the planner of the receiver.
func (p *plannerUI) skyFromReceiver() {
	addr := p.w.receiverAddr()
	if addr == "" {
		p.w.warn("Адрес приёмника не известен: впишите его во входной каталог (например https://дом.example:8443) и нажмите «Просмотреть кадры» — тогда сортировщик войдёт в приёмник.")
		return
	}
	if p.state.SkySet && walk.MsgBox(p.w.mw, "ASIAIR Sorter", "Заменить горизонт и зоны, поправленные здесь, небом с приёмника "+addr+"?",
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	setLong(p.planInfo, "Спрашиваю небо обсерватории у приёмника "+addr+"…")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Небо с приёмника: сбой: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var sky *sorter.ReceiverSky
		r, err := p.w.connect(ctx, addr, "", "")
		if err == nil {
			sky, err = r.PlannerSky(ctx)
		}
		p.w.mw.Synchronize(func() {
			if err != nil {
				setLong(p.planInfo, "Небо с приёмника не получено: "+err.Error())
				return
			}
			p.state.Sky.Horizon = append(planner.Horizon(nil), sky.Sky.Horizon...)
			p.state.Sky.Zones = append([]planner.Zone(nil), sky.Sky.Zones...)
			if sky.Sky.MinAlt > 0 {
				p.minAltEdit.SetValue(sky.Sky.MinAlt)
				p.state.Sky.MinAlt = sky.Sky.MinAlt
			}
			p.state.SkySet = true // kept: the frames read here do not replace it
			if sky.SiteSet {
				p.latEdit.SetValue(sky.Lat)
				p.lonEdit.SetValue(sky.Lon)
				p.state.SiteSet = true
			}
			if sky.TZ != nil {
				p.tzEdit.SetValue(*sky.TZ)
			}
			p.skyChanged()
			p.paramsChanged()
			what := "угадано по кадрам приёмника"
			if sky.Edited {
				what = "поправлено на странице приёмника"
			}
			setLong(p.planInfo, fmt.Sprintf("Небо обсерватории взято с приёмника (%s): точек горизонта %d, закрытых зон %d.", what, len(sky.Sky.Horizon), len(sky.Sky.Zones)))
		})
	}()
}
