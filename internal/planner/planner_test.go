package planner

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

const lat, lon = 55.75, 37.62 // Moscow

func TestHorizon(t *testing.T) {
	h := Horizon{{Az: 350, Alt: 10}, {Az: 10, Alt: 20}, {Az: 180, Alt: 30}}
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
	return Point{Az: p.Az, Alt: p.Alt}
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
	sky := Sky{Horizon: Horizon{{Az: 0, Alt: 15}, {Az: 45, Alt: 30}, {Az: 135, Alt: 30}, {Az: 180, Alt: 15}}, Zones: []Zone{wires}, MinAlt: 15}
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

// One sample does not lift the horizon all round.
func TestInferLoneSample(t *testing.T) {
	m31 := Obs{Object: "M 31", Night: "2026-10-15", Exposure: 300, RA: 10.68, Dec: 41.27, Lat: lat, Lon: lon}
	dusk, _, _ := Night("2026-10-15", lat, lon, -18)
	var obs []Obs
	for i := 0; i < 24; i++ {
		o := m31
		o.UTC = dusk.Add(time.Duration(60+5*i) * time.Minute) // begun an hour after dusk, still rising
		obs = append(obs, o)
	}
	g := Infer(obs, 15)
	first := at(Target{RA: m31.RA, Dec: m31.Dec}, obs[0].UTC)
	if h := g.Horizon.At(first.Az); math.Abs(h-(first.Alt-1)) > 1 {
		t.Errorf("at the sample: %v, frame at %+v", h, first)
	}
	if h := g.Horizon.At(first.Az + 120); h != 15 {
		t.Errorf("far from the sample: %v", h)
	}
}

// A rising object begun right after another one: the switch was the plan,
// the horizon is not there.
func TestInferSwitch(t *testing.T) {
	m31 := Obs{Object: "M 31", Night: "2026-10-15", Exposure: 300, RA: 10.68, Dec: 41.27, Lat: lat, Lon: lon}
	ic443 := Obs{Object: "IC 443", Night: "2026-10-15", Exposure: 300, RA: 94.25, Dec: 22.53, Lat: lat, Lon: lon}
	_, dawn, _ := Night("2026-10-15", lat, lon, -18)
	switchAt := dawn.Add(-3 * time.Hour)
	var obs []Obs
	for tm := switchAt.Add(-2 * time.Hour); tm.Before(dawn); tm = tm.Add(5 * time.Minute) {
		o := m31
		if !tm.Before(switchAt) {
			o = ic443
		}
		o.UTC = tm
		obs = append(obs, o)
	}
	first := at(Target{RA: ic443.RA, Dec: ic443.Dec}, switchAt)
	if first.Alt < 25 {
		t.Fatalf("IC 443 too low at the switch: %+v", first)
	}
	g := Infer(obs, 10)
	if h := g.Horizon.At(first.Az); h > 10 {
		t.Errorf("horizon %v at the switch to IC 443 %+v", h, first)
	}
}

func TestReguess(t *testing.T) {
	g := Guess{
		Horizon: Horizon{{Az: 0, Alt: 20}, {Az: 90, Alt: 30}, {Az: 95, Alt: 31}, {Az: 180, Alt: 20}},
		Zones:   []Zone{{Az1: 100, Az2: 110, Alt1: 40, Alt2: 50, Auto: true}, {Az1: 200, Az2: 210, Alt1: 40, Alt2: 50, Auto: true}},
	}
	sky := Sky{MinAlt: 12,
		Horizon: Horizon{{Az: 0, Alt: 20}, {Az: 92, Alt: 12, Manual: true}, {Az: 180, Alt: 25}},
		Zones:   []Zone{{Az1: 105, Az2: 115, Alt1: 45, Alt2: 55}, {Az1: 300, Az2: 310, Alt1: 40, Alt2: 50, Auto: true}}}
	got := sky.Reguess(g, true)
	want := Horizon{{Az: 0, Alt: 20}, {Az: 92, Alt: 12, Manual: true}, {Az: 180, Alt: 20}}
	if !reflect.DeepEqual(got.Horizon, want) {
		t.Errorf("horizon %+v, want %+v", got.Horizon, want)
	}
	// the zone moved by hand stays and the guessed one under it gives way;
	// the old guessed zone goes
	wantZ := []Zone{sky.Zones[0], g.Zones[1]}
	if !reflect.DeepEqual(got.Zones, wantZ) || got.MinAlt != 12 || !got.Manual() {
		t.Errorf("zones %+v, want %+v", got.Zones, wantZ)
	}

	// saved before the points were marked: what the guess lacks was edited
	old := Sky{Horizon: Horizon{{Az: 0, Alt: 20}, {Az: 92, Alt: 12}, {Az: 180, Alt: 20}}}
	got = old.Reguess(g, true)
	want = Horizon{{Az: 0, Alt: 20}, {Az: 92, Alt: 12, Manual: true}, {Az: 180, Alt: 20}}
	if !reflect.DeepEqual(got.Horizon, want) {
		t.Errorf("old sky: %+v, want %+v", got.Horizon, want)
	}
	// never edited: all from the guess
	if got = old.Reguess(g, false); !reflect.DeepEqual(got.Horizon, g.Horizon) || got.Manual() {
		t.Errorf("not edited: %+v", got.Horizon)
	}
}
