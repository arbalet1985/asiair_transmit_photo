package planner

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

const lat, lon = 55.75, 37.62 // Moscow

func TestHorizon(t *testing.T) {
	h := Horizon{{350, 10}, {10, 20}, {180, 30}}
	for az, want := range map[float64]float64{0: 15, 10: 20, 95: 25, 180: 30, 265: 20, 355: 12.5} {
		if got := h.At(az); math.Abs(got-want) > 1e-9 {
			t.Errorf("At(%v) = %v, want %v", az, got, want)
		}
	}
	z := Zone{Az1: 350, Az2: 20, Alt1: 30, Alt2: 40}
	if !z.Contains(0, 35) || !z.Contains(355, 30) || z.Contains(30, 35) || z.Contains(0, 45) {
		t.Error("zone through the north")
	}
	s := Sky{Horizon: h, Zones: []Zone{z}, MinAlt: 12}
	if s.Visible(0, 35) || !s.Visible(0, 50) || s.Visible(180, 25) || !s.Visible(180, 31) || s.Visible(355, 11) {
		t.Error("visible")
	}
}

func TestNight(t *testing.T) {
	dusk, dawn, err := Night("2026-10-15", lat, lon, -18)
	if err != nil {
		t.Fatal(err)
	}
	// Moscow, mid-October: astronomical night about 16:45 – 01:45 UTC
	if dusk.Hour() < 16 || dusk.Hour() > 17 || dawn.Hour() < 1 || dawn.Hour() > 2 {
		t.Errorf("night %v – %v", dusk, dawn)
	}
	if _, _, err := Night("2026-06-21", lat, lon, -18); !errors.Is(err, ErrNoNight) {
		t.Errorf("white night: %v", err)
	}
}

// track of the target at t
func at(tg Target, tm time.Time) Point {
	p := moon.Object(tm, lat, lon, tg.RA, tg.Dec)
	return Point{p.Az, p.Alt}
}

// M 31 first; the wires hide it for an hour, then a spare target is shot;
// M 31 again; the Medusa, the most wanted, as soon as it is over the
// trees in the east.
func TestSchedule(t *testing.T) {
	medusa := Target{Name: "Medusa", RA: 112.26, Dec: 13.25, Exposure: 300, Priority: 1}
	m31 := Target{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 180, Priority: 2}
	spare := Target{Name: "NGC 7000", RA: 314.82, Dec: 44.53, Exposure: 300, Priority: 3}
	targets := []Target{spare, m31, medusa}
	prm := DefaultParams()
	prm.Night, prm.Lat, prm.Lon, prm.Zone = "2026-10-15", lat, lon, time.UTC
	dusk, _, _ := Night(prm.Night, lat, lon, prm.SunAlt)
	// the wires: over the path of M 31 from 18:30 to 19:30 UTC
	w1, w2 := at(m31, dusk.Add(105*time.Minute)), at(m31, dusk.Add(165*time.Minute))
	wires := Zone{Az1: math.Min(w1.Az, w2.Az), Az2: math.Max(w1.Az, w2.Az), Alt1: math.Min(w1.Alt, w2.Alt) - 1, Alt2: math.Max(w1.Alt, w2.Alt) + 1}
	sky := Sky{Horizon: Horizon{{0, 15}, {45, 30}, {135, 30}, {180, 15}}, Zones: []Zone{wires}, MinAlt: 15}
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	var slews []string
	for _, s := range p.Steps {
		if s.Kind == Slew {
			slews = append(slews, targets[s.Target].Name)
		}
		if s.Kind == Shoot {
			for u := s.Start; u.Before(s.End); u = u.Add(time.Minute) {
				if w := p.Where(s.Target, u); !sky.Visible(w.Az, w.Alt) {
					t.Fatalf("%s shot while hidden at %v (%+v)", targets[s.Target].Name, u, w)
				}
			}
		}
	}
	want := "M 31, NGC 7000, M 31, Medusa"
	if got := strings.Join(slews, ", "); got != want {
		t.Errorf("slews: %s, want %s\n%s", got, want, p.Text())
	}
	flips := 0
	for _, s := range p.Steps {
		if s.Kind == Flip {
			flips++
			if ha := moon.HourAngle(s.Start, lon, targets[s.Target].RA); ha < 0 || ha > 0.5 {
				t.Errorf("flip at hour angle %v", ha)
			}
		}
	}
	if flips == 0 {
		t.Errorf("no meridian flip of M 31\n%s", p.Text())
	}
	for i := range targets {
		if p.Frames[i] == 0 {
			t.Errorf("%s not shot", targets[i].Name)
		}
	}
	// the telescope moves straight from target to target
	for _, s := range p.Steps {
		if s.Kind == Slew {
			mid := p.At(s.Start.Add(s.End.Sub(s.Start) / 2))
			if mid.Step < 0 || mid.Target != s.Target {
				t.Errorf("state during the slew: %+v", mid)
			}
		}
	}
	if st := p.At(dusk.Add(-time.Hour)); st.Step != -1 || st.Point != p.Park() {
		t.Errorf("before the night: %+v", st)
	}
}

func TestOverheadsCost(t *testing.T) {
	tg := []Target{{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 120, Priority: 1}}
	prm := DefaultParams()
	prm.Night, prm.Lat, prm.Lon = "2026-10-15", lat, lon
	cheap := prm
	cheap.DitherEvery, cheap.RefocusEveryMin, cheap.Flip = 0, 0, false
	a, _ := Schedule(tg, Sky{}, cheap)
	b, _ := Schedule(tg, Sky{}, prm)
	if b.Frames[0] >= a.Frames[0] || a.Frames[0] == 0 {
		t.Errorf("frames without overheads %d, with %d", a.Frames[0], b.Frames[0])
	}
	// want: stops at the asked exposure
	tg[0].WantSec = 3600
	c, _ := Schedule(tg, Sky{}, prm)
	if c.Seconds[0] != 3600 {
		t.Errorf("wanted an hour, got %v s", c.Seconds[0])
	}
	if !strings.Contains(c.Text(), "M 31: 30 кадров, 1 ч 00 мин из 1 ч 00 мин") {
		t.Errorf("text:\n%s", c.Text())
	}
}

func TestInfer(t *testing.T) {
	m31 := Obs{Object: "M 31", Night: "2026-10-15", Exposure: 300, RA: 10.68, Dec: 41.27, Lat: lat, Lon: lon}
	dusk, dawn, _ := Night("2026-10-15", lat, lon, -18)
	gapFrom, gapTo := dusk.Add(2*time.Hour), dusk.Add(3*time.Hour)
	var obs []Obs
	for tm := dusk; tm.Before(dawn); tm = tm.Add(5 * time.Minute) {
		if tm.After(gapFrom) && tm.Before(gapTo) {
			continue
		}
		o := m31
		o.UTC = tm
		obs = append(obs, o)
	}
	g := Infer(obs, 10)
	if len(g.Points) != len(obs) || len(g.Zones) != 1 || len(g.Maybe) != 0 {
		t.Fatalf("zones %+v, maybe %+v", g.Zones, g.Maybe)
	}
	mid := at(Target{RA: m31.RA, Dec: m31.Dec}, gapFrom.Add(30*time.Minute))
	if !g.Zones[0].Contains(mid.Az, mid.Alt) {
		t.Errorf("zone %+v misses %+v", g.Zones[0], mid)
	}
	// every frame stays above the guessed horizon
	for _, o := range g.Points {
		if h := g.Horizon.At(o.Az); h > o.Alt {
			t.Errorf("horizon %v over the frame at %+v", h, o.Point)
		}
	}
	// the same gap while another object was shot: only maybe
	other := m31
	other.Object, other.RA, other.Dec, other.UTC = "NGC 7000", 314.82, 44.53, gapFrom.Add(20*time.Minute)
	g = Infer(append(obs, other), 10)
	if len(g.Zones) != 0 || len(g.Maybe) != 1 {
		t.Errorf("with another object: zones %+v, maybe %+v", g.Zones, g.Maybe)
	}
}

// A rising object begun late shows the horizon in the east.
func TestInferHorizon(t *testing.T) {
	medusa := Obs{Object: "Medusa", Night: "2026-10-15", Exposure: 300, RA: 112.26, Dec: 13.25, Lat: lat, Lon: lon}
	_, dawn, _ := Night("2026-10-15", lat, lon, -18)
	var obs []Obs
	start := time.Time{}
	for tm := dawn.Add(-6 * time.Hour); tm.Before(dawn); tm = tm.Add(5 * time.Minute) {
		if p := at(Target{RA: medusa.RA, Dec: medusa.Dec}, tm); p.Alt < 25 {
			continue
		}
		if start.IsZero() {
			start = tm
		}
		o := medusa
		o.UTC = tm
		obs = append(obs, o)
	}
	g := Infer(obs, 10)
	first := at(Target{RA: medusa.RA, Dec: medusa.Dec}, start)
	if h := g.Horizon.At(first.Az); math.Abs(h-(first.Alt-1)) > 1 {
		t.Errorf("horizon at the start %v, the first frame at %+v", h, first)
	}
	if h := g.Horizon.At(first.Az + 180); h != 10 && h > first.Alt {
		t.Errorf("horizon opposite: %v", h)
	}
}
