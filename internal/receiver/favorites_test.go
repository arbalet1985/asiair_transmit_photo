package receiver

import (
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestUIVisibility(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	m31 := planner.Favorite{Name: "NGC 224", Label: "M 31", RA: 10.68, Dec: 41.27}
	req := map[string]any{"night": "2026-10-15", "objects": []planner.Favorite{m31}, "sky": planner.Sky{MinAlt: 20}}
	if code := uiCall(t, ui, "POST", "/ui/api/visibility", req, nil); code != 400 {
		t.Fatalf("without a site: %d", code)
	}
	if err := s.state.SetSite(Site{Lat: 55.75, Lon: 37.62, Set: true}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		From, To, Dusk, Dawn int64
		Objects              []planner.Vis
	}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || len(got.Objects) != 1 {
		t.Fatalf("visibility: %+v", got)
	}
	v := got.Objects[0]
	// M 31 never sets at Moscow and culminates high near midnight
	if v.Rise != 0 || v.Set != 0 || !v.Up || v.TransitAlt < 70 || v.Dark < 8*3600 || !v.Match || len(v.Alt) != len(v.Open) {
		t.Fatalf("M 31: %+v", v)
	}
	// it culminates in the south; the azimuth goes with the altitude
	if len(v.Az) != len(v.Alt) || v.TransitAz < 179 || v.TransitAz > 181 || planner.SideName(v.TransitAz) != "Ю" {
		t.Fatalf("M 31 azimuth: %v %v", v.TransitAz, len(v.Az))
	}
	tr := time.UnixMilli(v.Transit).UTC()
	if tr.Hour() < 19 || tr.Hour() > 22 {
		t.Fatalf("transit at %v", tr)
	}
	// the filter: high in the south-east or the south, then only to the north
	req["filter"] = planner.Filter{AltMin: 60, AltMax: 90, Dirs: 8 | 16}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || !got.Objects[0].Match {
		t.Fatalf("south: %+v", got.Objects[0])
	}
	req["filter"] = planner.Filter{AltMin: 60, AltMax: 90, Dirs: 1}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || got.Objects[0].Match {
		t.Fatalf("north: %+v", got.Objects[0])
	}
	// in the evening only M 31 is still low in the north-east
	eve := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)
	req["filter"] = planner.Filter{AltMin: 0, AltMax: 90, Dirs: 2, From: eve.UnixMilli(), To: eve.Add(30 * time.Minute).UnixMilli()}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || !got.Objects[0].Match || got.Objects[0].MatchSec > 35*60 {
		t.Fatalf("evening north-east: %+v", got.Objects[0])
	}
	req["filter"] = planner.Filter{AltMin: 0, AltMax: 90, Dirs: 2, From: eve.Add(6 * time.Hour).UnixMilli(), To: eve.Add(7 * time.Hour).UnixMilli()}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &got) != 200 || got.Objects[0].Match {
		t.Fatalf("night north-east: %+v", got.Objects[0])
	}
	// the best of the night: nebulae in the north-east from 30°
	var best struct{ List []planner.Favorite }
	req = map[string]any{"night": "2026-10-15", "best": true, "filter": planner.Filter{AltMin: 30, AltMax: 90, Dirs: 2, Kind: "nebula"}}
	if uiCall(t, ui, "POST", "/ui/api/visibility", req, &best) != 200 || len(best.List) == 0 {
		t.Fatal("best")
	}
	for _, o := range best.List {
		if o.Kind == "G" {
			t.Fatalf("a galaxy among the nebulae: %+v", o)
		}
	}
	// changed by name, and not lost when the page posts the planner
	var fav struct{ Favorites []planner.Favorite }
	if uiCall(t, ui, "POST", "/ui/api/favorites", map[string]any{"add": []planner.Favorite{m31, m31}}, &fav) != 200 || len(fav.Favorites) != 1 {
		t.Fatalf("add: %+v", fav)
	}
	st := s.state.Planner()
	st.Favorites = nil
	if uiCall(t, ui, "POST", "/ui/api/planner", st, nil); len(s.state.Planner().Favorites) != 1 {
		t.Fatal("favorites lost")
	}
	if uiCall(t, ui, "POST", "/ui/api/favorites", map[string]any{"remove": []string{"NGC 224"}}, &fav) != 200 || len(fav.Favorites) != 0 {
		t.Fatalf("remove: %+v", fav)
	}
}

// TestFavoriteNotes: a note is set and taken away by name, cut to its
// length, and kept with a favorite added.
func TestFavoriteNotes(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	var got struct{ Favorites []planner.Favorite }
	add := map[string]any{"add": []planner.Favorite{{Name: "NGC 224", Note: " в Ha "}, {Name: "IC 1805"}}}
	if uiCall(t, ui, "POST", "/ui/api/favorites", add, &got) != 200 || len(got.Favorites) != 2 || got.Favorites[0].Note != "в Ha" {
		t.Fatalf("added: %+v", got)
	}
	long := strings.Repeat("я", planner.MaxNote+20)
	notes := map[string]any{"notes": map[string]string{"NGC 224": "", "IC 1805": long, "M 1": "нет такого"}}
	got.Favorites = nil
	if uiCall(t, ui, "POST", "/ui/api/favorites", notes, &got) != 200 || len(got.Favorites) != 2 ||
		got.Favorites[0].Note != "" || len([]rune(got.Favorites[1].Note)) != planner.MaxNote {
		t.Fatalf("notes: %+v", got)
	}
}
