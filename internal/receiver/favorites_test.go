package receiver

import (
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestSide(t *testing.T) {
	for az, want := range map[float64]uint8{0: 1, 350: 1, 23: 2, 90: 4, 180: 16, 270: 64, 314: 128, 338: 1} {
		if got := side(az); got != want {
			t.Errorf("side(%v) = %d, want %d", az, got, want)
		}
	}
}

func TestUIVisibility(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	m31 := Favorite{Name: "NGC 224", Label: "M 31", RA: 10.68, Dec: 41.27}
	req := map[string]any{"night": "2026-10-15", "objects": []Favorite{m31}, "sky": planner.Sky{MinAlt: 20}}
	if code := uiCall(t, ui, "POST", "/ui/api/visibility", req, nil); code != 400 {
		t.Fatalf("without a site: %d", code)
	}
	if err := s.state.SetSite(Site{Lat: 55.75, Lon: 37.62, Set: true}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		From, To, Dusk, Dawn int64
		Objects              []visObject
	}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || len(got.Objects) != 1 {
		t.Fatalf("visibility: %+v", got)
	}
	v := got.Objects[0]
	// M 31 never sets at Moscow and culminates high near midnight
	if v.Rise != 0 || v.Set != 0 || !v.Up || v.TransitAlt < 70 || v.Dark < 8*3600 || !v.Match || len(v.Alt) != len(v.Open) {
		t.Fatalf("M 31: %+v", v)
	}
	tr := time.UnixMilli(v.Transit).UTC()
	if tr.Hour() < 19 || tr.Hour() > 22 {
		t.Fatalf("transit at %v", tr)
	}
	// the filter: high in the south-east or the south, then only to the north
	req["filter"] = skyFilter{AltMin: 60, AltMax: 90, Dirs: 8 | 16}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || !got.Objects[0].Match {
		t.Fatalf("south: %+v", got.Objects[0])
	}
	req["filter"] = skyFilter{AltMin: 60, AltMax: 90, Dirs: 1}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || got.Objects[0].Match {
		t.Fatalf("north: %+v", got.Objects[0])
	}
	// in the evening only M 31 is still low in the north-east
	eve := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	req["filter"] = skyFilter{AltMin: 0, AltMax: 90, Dirs: 2, From: eve.UnixMilli(), To: eve.Add(30 * time.Minute).UnixMilli()}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || !got.Objects[0].Match || got.Objects[0].MatchSec > 35*60 {
		t.Fatalf("evening north-east: %+v", got.Objects[0])
	}
	req["filter"] = skyFilter{AltMin: 0, AltMax: 90, Dirs: 2, From: eve.Add(6 * time.Hour).UnixMilli(), To: eve.Add(7 * time.Hour).UnixMilli()}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || got.Objects[0].Match {
		t.Fatalf("night north-east: %+v", got.Objects[0])
	}
	// the best of the night: nebulae in the north-east from 30°
	var best struct{ List []Favorite }
	req = map[string]any{"night": "2026-10-15", "best": true, "filter": skyFilter{AltMin: 30, AltMax: 90, Dirs: 2, Kind: "nebula"}}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &best) != 200 || len(best.List) == 0 {
		t.Fatal("best")
	}
	for _, o := range best.List {
		if o.Kind == "G" {
			t.Fatalf("a galaxy among the nebulae: %+v", o)
		}
	}
	// kept, and not lost when an older page posts the planner
	st := s.state.Planner()
	st.Favorites = []Favorite{m31}
	if code := uiCall(t, ui, "POST", "/ui/api/planner", st, nil); code != 204 {
		t.Fatal(code)
	}
	st.Favorites = nil
	if uiCall(t, ui, "POST", "/ui/api/planner", st, nil); len(s.state.Planner().Favorites) != 1 {
		t.Fatal("favorites lost")
	}
	// the page sends the emptied list as []
	if uiCall(t, ui, "POST", "/ui/api/planner", map[string]any{"sky": st.Sky, "targets": st.Targets, "favorites": []Favorite{}}, nil); len(s.state.Planner().Favorites) != 0 {
		t.Fatal("favorites not removed")
	}
}
