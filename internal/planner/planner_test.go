package planner

import (
	"encoding/json"
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

// Allowed to come back: M 31 first; the wires hide it for an hour, then a
// spare target is shot; M 31 again; the Medusa, the most wanted, as soon
// as it is over the trees in the east.
func TestSchedule(t *testing.T) {
	medusa := Target{Name: "Medusa", RA: 112.26, Dec: 13.25, Exposure: 300, Priority: 1}
	m31 := Target{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 180, Priority: 2}
	spare := Target{Name: "NGC 7000", RA: 314.82, Dec: 44.53, Exposure: 300, Priority: 3}
	targets := []Target{spare, m31, medusa}
	prm := DefaultParams()
	prm.Night, prm.Lat, prm.Lon, prm.Zone = "2026-10-15", lat, lon, time.UTC
	prm.Repeat = true
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

// sky and targets of TestSchedule, to plan them other ways.
func wiredSky(t *testing.T) ([]Target, Sky, Params) {
	t.Helper()
	medusa := Target{Name: "Medusa", RA: 112.26, Dec: 13.25, Exposure: 300, Priority: 1}
	m31 := Target{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 180, Priority: 2}
	spare := Target{Name: "NGC 7000", RA: 314.82, Dec: 44.53, Exposure: 300, Priority: 3}
	prm := DefaultParams()
	prm.Night, prm.Lat, prm.Lon, prm.Zone = "2026-10-15", lat, lon, time.UTC
	dusk, _, _ := Night(prm.Night, lat, lon, prm.SunAlt)
	w1, w2 := at(m31, dusk.Add(105*time.Minute)), at(m31, dusk.Add(165*time.Minute))
	wires := Zone{Az1: math.Min(w1.Az, w2.Az), Az2: math.Max(w1.Az, w2.Az), Alt1: math.Min(w1.Alt, w2.Alt) - 1, Alt2: math.Max(w1.Alt, w2.Alt) + 1}
	sky := Sky{Horizon: Horizon{{Az: 0, Alt: 15}, {Az: 45, Alt: 30}, {Az: 135, Alt: 30}, {Az: 180, Alt: 15}}, Zones: []Zone{wires}, MinAlt: 15}
	return []Target{spare, m31, medusa}, sky, prm
}

// Shot once (the default): no target is gone back to, and M 31 is begun
// after the wires, where it is seen the longest.
func TestScheduleOnce(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	var slews []string
	seen := map[string]int{}
	for _, s := range p.Steps {
		if s.Kind == Slew {
			slews = append(slews, targets[s.Target].Name)
			seen[targets[s.Target].Name]++
		}
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s gone to %d times: %s\n%s", name, n, strings.Join(slews, ", "), p.Text())
		}
	}
	for i := range targets {
		if p.Frames[i] == 0 {
			t.Errorf("%s not shot\n%s", targets[i].Name, p.Text())
		}
	}
	// M 31 is not begun before the wires, which would end it after an hour
	for _, s := range p.Steps {
		if s.Kind != Shoot || targets[s.Target].Name != "M 31" {
			continue
		}
		if w := p.Where(s.Target, s.Start); !sky.Visible(w.Az, w.Alt) {
			t.Errorf("M 31 shot while hidden")
		}
		break
	}
	// allowed to come back, it gets more of the night
	prm.Repeat = true
	q, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	if sum(q.Seconds) <= sum(p.Seconds) {
		t.Errorf("coming back gave %v, once %v", sum(q.Seconds), sum(p.Seconds))
	}
}

func sum(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s
}

// The mount stops at the meridian and waits for the flip.
func TestMeridianPause(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	prm.FlipAfterMin, prm.FlipMin = 20, 5
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	flips := 0
	for k, s := range p.Steps {
		if s.Kind != Flip {
			continue
		}
		flips++
		if ha := moon.HourAngle(s.Start, lon, targets[s.Target].RA); math.Abs(ha-prm.FlipAfterMin/60) > 0.03 {
			t.Errorf("flip at hour angle %v, want %v", ha, prm.FlipAfterMin/60)
		}
		if k == 0 || p.Steps[k-1].Kind != Wait || p.Steps[k-1].Note != NoteMeridian {
			t.Errorf("no wait before the flip: %+v", p.Steps[max(k-1, 0)])
		}
	}
	if flips == 0 {
		t.Fatalf("no flip\n%s", p.Text())
	}
	// no frame runs through the meridian before the flip
	for _, s := range p.Steps {
		if s.Kind != Shoot {
			continue
		}
		ha1, ha2 := moon.HourAngle(s.Start, lon, targets[s.Target].RA), moon.HourAngle(s.End, lon, targets[s.Target].RA)
		if ha1 < 0 && ha2 > 0 {
			t.Errorf("%s shot through the meridian: %v – %v", targets[s.Target].Name, ha1, ha2)
		}
	}
}

func TestBlocks(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	prm.Repeat = true
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	bs := p.Blocks()
	if len(bs) < 4 {
		t.Fatalf("blocks: %d\n%s", len(bs), p.Text())
	}
	frames, secs := make([]int, len(targets)), make([]float64, len(targets))
	for k, b := range bs {
		if b.First >= b.Last || b.Last > len(p.Steps) {
			t.Fatalf("block %d: steps %d..%d of %d", k, b.First, b.Last, len(p.Steps))
		}
		if !b.Start.Equal(p.Steps[b.First].Start) || !b.End.Equal(p.Steps[b.Last-1].End) {
			t.Errorf("block %d: %v – %v", k, b.Start, b.End)
		}
		if k > 0 && bs[k-1].Last != b.First {
			t.Errorf("block %d does not follow %d", k, k-1)
		}
		for _, s := range p.Steps[b.First:b.Last] {
			if s.Target != b.Target {
				t.Errorf("block %d of target %d has a step of %d", k, b.Target, s.Target)
			}
		}
		if b.Target >= 0 {
			frames[b.Target] += b.Frames
			secs[b.Target] += b.Seconds
		}
	}
	if !reflect.DeepEqual(frames, p.Frames) || !reflect.DeepEqual(secs, p.Seconds) {
		t.Errorf("by blocks %v %v, by plan %v %v", frames, secs, p.Frames, p.Seconds)
	}
}

// Stopped early: nothing is shot in the minutes before the meridian either.
func TestMeridianStopBefore(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	prm.FlipBeforeMin, prm.FlipAfterMin, prm.FlipMin = 20, 10, 5
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	stop, flip := -prm.FlipBeforeMin/60, prm.FlipAfterMin/60
	flips := 0
	for k, s := range p.Steps {
		switch s.Kind {
		case Flip:
			flips++
			if k == 0 || p.Steps[k-1].Kind != Wait || p.Steps[k-1].Note != NoteMeridian {
				t.Errorf("no wait before the flip of %s", targets[s.Target].Name)
			}
			if ha := moon.HourAngle(s.Start, lon, targets[s.Target].RA); math.Abs(ha-flip) > 0.03 {
				t.Errorf("flip at hour angle %v, want %v", ha, flip)
			}
		case Shoot:
			// before its flip a target is shot only up to the stop line
			if afterFlip(p, k) {
				continue
			}
			ha := moon.HourAngle(s.End, lon, targets[s.Target].RA)
			if ha > stop && ha < 6 {
				t.Errorf("%s shot to hour angle %v, stopped at %v\n%s", targets[s.Target].Name, ha, stop, p.Text())
			}
		}
	}
	if flips == 0 {
		t.Fatalf("no flip\n%s", p.Text())
	}
	// without the early stop the frames go up to the meridian itself
	prm.FlipBeforeMin = 0
	q, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	late := false
	for k, s := range q.Steps {
		if s.Kind == Shoot && !afterFlip(q, k) {
			if ha := moon.HourAngle(s.End, lon, targets[s.Target].RA); ha > stop && ha <= 0 {
				late = true
			}
		}
	}
	if !late {
		t.Errorf("without the early stop nothing was shot in the last minutes before the meridian")
	}
}

// afterFlip tells the step k comes after the flip of its block.
func afterFlip(p *Plan, k int) bool {
	for i := k; i >= 0; i-- {
		if p.Steps[i].Kind == Slew {
			return false
		}
		if p.Steps[i].Kind == Flip {
			return true
		}
	}
	return false
}

// Stopped at the meridian the mount stands still: it cannot follow the
// object past it without the tube meeting the tripod.
func TestMeridianHold(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	prm.FlipBeforeMin, prm.FlipAfterMin, prm.FlipMin = 20, 10, 5
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	waits := 0
	for k, s := range p.Steps {
		if s.Kind != Wait || s.Note != NoteMeridian {
			continue
		}
		waits++
		held := p.Where(s.Target, s.Start)
		moved := false
		for u := s.Start; u.Before(s.End); u = u.Add(time.Minute) {
			st := p.At(u)
			if st.Point != held {
				t.Fatalf("the mount moved while stopped: %+v, held %+v", st.Point, held)
			}
			if w := p.Where(s.Target, u); math.Abs(wrap180(w.Az-held.Az)) > 1 {
				moved = true // the object has gone on without it
			}
		}
		// the flip brings it back onto the object
		if k+1 < len(p.Steps) && p.Steps[k+1].Kind == Flip {
			f := p.Steps[k+1]
			if st := p.At(f.Start); st.Point != held {
				t.Errorf("the flip does not begin where the mount stood: %+v, held %+v", st.Point, held)
			}
			end := p.At(f.End.Add(-time.Second))
			if w := p.Where(f.Target, f.End); math.Abs(wrap180(end.Point.Az-w.Az)) > 1 || math.Abs(end.Point.Alt-w.Alt) > 1 {
				t.Errorf("after the flip the mount is at %+v, the object at %+v", end.Point, w)
			}
			if !p.At(f.End).West {
				t.Errorf("not west once the flip is done")
			}
		}
		if !moved {
			t.Errorf("the object did not go on during the stop: nothing is proven")
		}
	}
	if waits == 0 {
		t.Fatalf("no stop at the meridian\n%s", p.Text())
	}
	// the other waits do follow the object
	for _, s := range p.Steps {
		if s.Kind != Wait || s.Note == NoteMeridian || s.Target < 0 {
			continue
		}
		mid := s.Start.Add(s.End.Sub(s.Start) / 2)
		if st, w := p.At(mid), p.Where(s.Target, mid); st.Point != w {
			t.Errorf("wait %q: the mount at %+v, the object at %+v", s.Note, st.Point, w)
		}
	}
}

func TestStart(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*3600)
	dusk, dawn, err := Night("2026-10-15", lat, lon, -18)
	if err != nil {
		t.Fatal(err)
	}
	hm := func(x time.Time) string { return x.In(zone).Format("15:04") }
	cases := []struct {
		at, want string
		bad      bool
	}{
		{at: "", want: hm(dusk)},
		{at: "  ", want: hm(dusk)},
		{at: "23:30", want: "23:30"},
		{at: "23.30", want: "23:30"}, // a dot for a colon
		{at: "01:30", want: "01:30"}, // after midnight, the next day
		{at: "4:00", want: "04:00"},  // one digit for the hour
		{at: "25:00", bad: true},     // not a time
		{at: "вечером", bad: true},   // not a time
		{at: "19:00", bad: true},     // before the dusk, so outside the night
		{at: "08:00", bad: true},     // after the dawn
	}
	for _, c := range cases {
		got, err := Start("2026-10-15", c.at, zone, dusk, dawn)
		if c.bad {
			if err == nil {
				t.Errorf("Start(%q) = %s, want an error", c.at, hm(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("Start(%q): %v", c.at, err)
			continue
		}
		if hm(got) != c.want {
			t.Errorf("Start(%q) = %s, want %s", c.at, hm(got), c.want)
		}
		if got.Before(dusk) || !got.Before(dawn) {
			t.Errorf("Start(%q) = %v, outside the night %v – %v", c.at, got, dusk, dawn)
		}
	}
}

// Begun late (clouds waited out): nothing is planned before the time asked.
func TestScheduleStart(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	prm.Zone = time.UTC
	full, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	late := full.Dusk.Add(3 * time.Hour).Truncate(time.Minute)
	prm.StartAt = late.In(prm.Zone).Format("15:04")
	p, err := Schedule(targets, sky, prm)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Start.Equal(late) {
		t.Errorf("start %v, want %v", p.Start, late)
	}
	if len(p.Steps) == 0 {
		t.Fatalf("no steps\n%s", p.Text())
	}
	if p.Steps[0].Start.Before(p.Start) {
		t.Errorf("the first step at %v, before the start %v", p.Steps[0].Start, late)
	}
	// the telescope is parked until then, and the night itself is unchanged
	if st := p.At(late.Add(-time.Minute)); st.Step != -1 || st.Point != p.Park() {
		t.Errorf("before the start: %+v", st)
	}
	if !p.Dusk.Equal(full.Dusk) || !p.Dawn.Equal(full.Dawn) {
		t.Errorf("the night moved: %v – %v", p.Dusk, p.Dawn)
	}
	if sum(p.Seconds) >= sum(full.Seconds) {
		t.Errorf("a late start gathered %v, the whole night %v", sum(p.Seconds), sum(full.Seconds))
	}
	if !strings.Contains(p.Text(), "Начало съёмки: "+prm.StartAt) {
		t.Errorf("the start is not in the text:\n%s", p.Text())
	}
	// a start outside the night is an error, not a silent full night
	prm.StartAt = "12:00"
	if _, err := Schedule(targets, sky, prm); err == nil {
		t.Errorf("a start at noon was taken")
	}
}

// Shot once must not leave the telescope idle while a target is open: when
// the best stretch of every target is later, what is open now is shot.
func TestOnceFillsIdle(t *testing.T) {
	targets, sky, prm := wiredSky(t)
	for _, startAt := range []string{"", "20:00", "21:30", "23:00"} {
		prm.StartAt = startAt
		p, err := Schedule(targets, sky, prm)
		if err != nil {
			continue
		}
		for _, s := range p.Steps {
			if s.Kind != Wait || s.Note != "нечего снимать" {
				continue
			}
			for i := range targets {
				if p.Seconds[i] == 0 && p.openFor(i, s.Start, minutes(prm.MinBlockMin)) {
					t.Errorf("start %q: idle at %s though %s is open\n%s", startAt, s.Start.Format("15:04"), targets[i].Name, p.Text())
				}
			}
		}
	}
}

// A polygon zone across the north: inside and outside, its box for the
// older programs, its edges cut at the north.
func TestPolygonZone(t *testing.T) {
	z := Zone{Poly: []Point{{Az: 350, Alt: 40}, {Az: 10, Alt: 40}, {Az: 0, Alt: 60}}}
	for _, c := range []struct {
		az, alt float64
		in      bool
	}{{0, 45, true}, {355, 41, true}, {5, 41, true}, {359.9, 59, true}, {180, 45, false}, {0, 39, false}, {15, 45, false}, {345, 45, false}, {5, 55, false}} {
		if z.Contains(c.az, c.alt) != c.in {
			t.Errorf("%v,%v: inside %v", c.az, c.alt, !c.in)
		}
	}
	z.Fit()
	if z.Az1 != 350 || z.Az2 != 10 || z.Alt1 != 40 || z.Alt2 != 60 {
		t.Fatalf("box %+v", z)
	}
	out := z.Outline()
	if len(out) != 4 { // the bottom edge crosses the north: cut in two
		t.Fatalf("outline %v", out)
	}
	for _, s := range out {
		for _, p := range s {
			if p.Az < 0 || p.Az > 360 {
				t.Fatalf("outside the strip: %v", out)
			}
		}
	}
	// the sky closes it
	sky := Sky{Zones: []Zone{z}}
	if sky.Visible(0, 45) || !sky.Visible(90, 45) {
		t.Fatal("visible")
	}
	// a zone of an older program (a box) still works, and a polygon saved
	// reads back
	b, _ := json.Marshal(z)
	var back Zone
	if err := json.Unmarshal(b, &back); err != nil || !back.Polygon() || !back.Contains(0, 45) {
		t.Fatalf("%s %+v", b, back)
	}
	var old Zone
	json.Unmarshal([]byte(`{"az1":100,"az2":120,"alt1":20,"alt2":30}`), &old)
	if old.Polygon() || !old.Contains(110, 25) {
		t.Fatalf("%+v", old)
	}
}

func TestRefocusAfterFilter(t *testing.T) {
	tg := []Target{
		{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 120, Priority: 1, WantSec: 3600, Filter: "L"},
		{Name: "NGC 7000", RA: 314.82, Dec: 44.53, Exposure: 300, Priority: 2, Filter: "Ha"},
	}
	prm := DefaultParams()
	prm.Night, prm.Lat, prm.Lon = "2026-10-15", lat, lon
	prm.RefocusEveryMin, prm.RefocusAfterSwitch, prm.Flip = 0, false, false
	count := func(p *Plan) (n int) {
		for _, s := range p.Steps {
			if s.Kind == Refocus {
				n++
				if s.Note != "смена фильтра" || s.Target != 1 {
					t.Errorf("refocus %+v", s)
				}
			}
		}
		return n
	}
	a, _ := Schedule(tg, Sky{}, prm)
	if n := count(a); n != 0 {
		t.Errorf("without the option: %d refocus\n%s", n, a.Text())
	}
	prm.RefocusAfterFilter = true
	b, _ := Schedule(tg, Sky{}, prm)
	if n := count(b); n != 1 {
		t.Errorf("with the option: %d refocus\n%s", n, b.Text())
	}
	// the same filter, or one not given, is no change
	tg[1].Filter = "l"
	if c, _ := Schedule(tg, Sky{}, prm); count(c) != 0 {
		t.Errorf("same filter refocused\n%s", c.Text())
	}
	if FilterChanged("", "Ha") || FilterChanged("Ha", " ") || !FilterChanged("L", "Ha") {
		t.Error("FilterChanged")
	}
}

// NGC 7635 and IC 1805 are higher than the pole at Moscow: they cross the
// meridian in the north, between the pole and the zenith.
func TestMeridianNorth(t *testing.T) {
	dusk, dawn, _ := Night("2026-10-27", lat, lon, -18)
	for _, tg := range []Target{{Name: "NGC 7635", RA: 350.2, Dec: 61.2}, {Name: "IC 1805", RA: 38.2, Dec: 61.45}} {
		m, ok := Meridian(tg, lon, dusk, dawn)
		if !ok {
			t.Fatalf("%s: no meridian in the night", tg.Name)
		}
		p := moon.Object(m, lat, lon, tg.RA, tg.Dec)
		if a := math.Min(p.Az, 360-p.Az); a > 2 || p.Alt < lat || p.Alt > 90 {
			t.Errorf("%s on the meridian at %+v", tg.Name, p)
		}
	}
	m31 := Target{RA: 10.68, Dec: 41.27}
	if m, ok := Meridian(m31, lon, dusk, dawn); !ok || math.Abs(moon.Object(m, lat, lon, m31.RA, m31.Dec).Az-180) > 2 {
		t.Errorf("M 31 on the meridian: %v %v", m, ok)
	}
	if mm := Meridians(lat); mm[0][0].Az != 180 || mm[0][0].Alt != 0 || mm[1][0].Az != 0 || mm[1][0].Alt != lat {
		t.Errorf("meridians %+v", mm)
	}
}
