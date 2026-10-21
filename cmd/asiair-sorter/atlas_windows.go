package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/atlas"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The atlas of the planner opens in the browser: a page of this program on
// 127.0.0.1 (internal/atlas) shows the targets of the planner with the
// field of view of the optics, and its framing changes them here.

// openAtlas starts the page (once) and opens it.
func (p *plannerUI) openAtlas() { p.showAtlas(true) }

// showAtlas starts the page and opens it in the browser: always, or
// (always false) only when no page of it is open.
func (p *plannerUI) showAtlas(always bool) {
	p.ensureAtlas()
	p.atlasChanged()
	u, err := p.atlas.Start()
	if err != nil {
		p.w.warn("Атлас не открыт: " + err.Error())
		return
	}
	if always || !p.atlas.Watched() {
		openURL(u)
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
		Optics: p.state.Optics, Setups: p.setups, Photo: p.photo, FrameReq: p.frameReq, FrameI: p.frameI}
	for i := range st.Targets {
		c := targetColor(i)
		st.Colors = append(st.Colors, fmt.Sprintf("#%02x%02x%02x", c.R(), c.G(), c.B()))
	}
	p.atlas.Publish(st)
}
