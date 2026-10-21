package main

import (
	"fmt"

	"github.com/arbalet1985/asiair_transmit_photo/internal/atlas"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// The atlas of the planner opens in the browser: a page of this program on
// 127.0.0.1 (internal/atlas) shows the targets of the planner with the
// field of view of the optics, and its framing changes them here.

// openAtlas starts the page (once) and opens it.
func (p *plannerUI) openAtlas() {
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
	p.atlasChanged()
	u, err := p.atlas.Start()
	if err != nil {
		p.w.warn("Атлас не открыт: " + err.Error())
		return
	}
	openURL(u)
}

// atlasChanged gives the open atlas the targets, the one chosen and the
// optics.
func (p *plannerUI) atlasChanged() {
	if p.atlas == nil || !p.ready {
		return
	}
	st := atlas.State{Targets: append([]planner.Target(nil), p.state.Targets...), Sel: p.targetTable.CurrentIndex(),
		Optics: p.state.Optics, Setups: p.setups}
	for i := range st.Targets {
		c := targetColor(i)
		st.Colors = append(st.Colors, fmt.Sprintf("#%02x%02x%02x", c.R(), c.G(), c.B()))
	}
	p.atlas.Publish(st)
}
