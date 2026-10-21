package receiver

import (
	"net/http"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestUIPlanner(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	// frames of M 31 at Moscow, every 5 minutes for 3 hours
	start := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	for i := 0; i < 36; i++ {
		ut := start.Add(time.Duration(i) * 5 * time.Minute)
		s.quality.set(time.Duration(i).String(), qualityEntry{Sky: &skyEntry{Object: "M 31", Night: "2026-10-15", UTC: ut.UnixMilli(),
			Wall: ut.Add(3 * time.Hour).UnixMilli(), Exp: 300, RA: 10.68, Dec: 41.27, Lat: 55.75, Lon: 37.62,
			Cam: "ZWO ASI533MC Pro", Focal: 400, Pix: 3.76, W: 3008, H: 3008}})
	}
	var got struct {
		State   PlannerState `json:"state"`
		TZ      float64      `json:"tz_guess"`
		Objects []plannerObject
		Guess   plannerGuess
		Setups  []planner.Setup
	}
	if uiCall(t, ui, "GET", "/ui/api/planner", nil, &got) != 200 {
		t.Fatal("planner")
	}
	if len(got.Setups) != 1 || got.Setups[0].Frames != 36 || got.Setups[0].Focal != 400 || got.Setups[0].Width != 3008 {
		t.Fatalf("setups: %+v", got.Setups)
	}
	if got.TZ != 3 || len(got.Objects) != 1 || got.Objects[0].Name != "M 31" || len(got.Guess.Points) != 36 || len(got.State.Sky.Horizon) == 0 {
		t.Fatalf("planner: %+v", got)
	}
	var found []plannerObject
	if uiCall(t, ui, "GET", "/ui/api/catalog?q=медуза", nil, &found) != 200 || len(found) == 0 || found[0].Name != "Abell 21" {
		t.Fatalf("catalog: %+v", found)
	}

	st := got.State
	st.Targets = []planner.Target{{Name: "M 31", RA: 10.68, Dec: 41.27, Exposure: 300, Priority: 1}}
	if code := uiCall(t, ui, "POST", "/ui/api/plan", map[string]any{"night": "2026-10-15", "state": st}, nil); code != http.StatusBadRequest {
		t.Fatalf("plan without a site: %d", code)
	}
	if err := s.state.SetSite(Site{Lat: 55.75, Lon: 37.62, Set: true}); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Dusk, Dawn int64
		Steps      []planStep
		Scope      [][4]float64
		Tracks     [][][2]float64
		Moon       [][2]float64
		Text       string
	}
	if uiCall(t, ui, "POST", "/ui/api/plan", map[string]any{"night": "2026-10-15", "state": st}, &plan) != 200 {
		t.Fatal("plan")
	}
	if len(plan.Steps) == 0 || len(plan.Scope) == 0 || len(plan.Tracks) != 1 || len(plan.Moon) != len(plan.Scope) || plan.Text == "" {
		t.Fatalf("plan: %d steps, %d samples", len(plan.Steps), len(plan.Scope))
	}
	// kept, with the framing and the optics of the atlas
	st.Targets[0].Angle = 30
	st.Optics = &planner.Optics{Focal: 400, Reducer: 0.8, Pixel: 3.76, Width: 3008, Height: 3008}
	if code := uiCall(t, ui, "POST", "/ui/api/planner", st, nil); code != http.StatusNoContent || len(s.state.Planner().Targets) != 1 {
		t.Fatalf("not kept: %d", code)
	}
	if p := s.state.Planner(); p.Targets[0].Angle != 30 || p.Optics == nil || p.Optics.Reducer != 0.8 {
		t.Fatalf("framing not kept: %+v", p)
	}
	if uiCall(t, ui, "GET", "/ui/atlas.js", nil, nil) != 200 {
		t.Fatal("atlas.js")
	}
}
