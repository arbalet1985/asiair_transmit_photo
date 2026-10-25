package atlas

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestSkyMap(t *testing.T) {
	s := NewServer()
	got := make(chan string, 4)
	s.OnFavorite = func(f planner.Favorite, on bool) {
		if f.Name == "NGC 598" && on {
			got <- "favorite"
		}
	}
	s.OnAdd = func(f planner.Favorite) { got <- "add " + f.Name }
	ts := httptest.NewServer(s)
	defer ts.Close()
	q := "?t=" + s.token
	get := func(p string, hdr ...string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		if len(hdr) == 2 {
			req.Header.Set(hdr[0], hdr[1])
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, string(b)
	}
	// the map and its stars have no secret: no token; asked again, not sent again
	r, b := get("/skydata.js")
	if r.StatusCode != 200 || !strings.Contains(b, "window.SkyData") || !strings.Contains(b, "Вега") {
		t.Fatalf("skydata: %d %.80s", r.StatusCode, b)
	}
	if r2, _ := get("/skydata.js", "If-None-Match", r.Header.Get("ETag")); r2.StatusCode != http.StatusNotModified {
		t.Fatalf("not cached: %d", r2.StatusCode)
	}
	if r, b := get("/skystars8.js"); r.StatusCode != 200 || !strings.Contains(b, "window.SkyStars8") {
		t.Fatalf("skystars8: %d", r.StatusCode)
	}
	if r, b := get("/skymap.js"); r.StatusCode != 200 || !strings.Contains(b, "window.SkyMap") {
		t.Fatalf("skymap: %d", r.StatusCode)
	}
	// the objects need the token
	if r, _ := get("/objects"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("objects without a token: %d", r.StatusCode)
	}
	var objs [][]any
	if _, b := get("/objects" + q); json.Unmarshal([]byte(b), &objs) != nil || len(objs) < 300 {
		t.Fatalf("objects: %d", len(objs))
	}
	m31 := false
	for _, o := range objs {
		m31 = m31 || o[2] == "M 31" && o[0] == "NGC 224"
	}
	if !m31 {
		t.Fatal("no M 31 on the map")
	}
	var found []Found
	if _, b := get("/find" + q + "&q=" + "Медуза"); json.Unmarshal([]byte(b), &found) != nil || len(found) == 0 || found[0].Name != "Abell 21" {
		t.Fatalf("find: %s", b)
	}
	post := func(p string, v any) {
		body, _ := json.Marshal(v)
		r, err := http.Post(ts.URL+p+q, "application/json", bytes.NewReader(body))
		if err != nil || r.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %v %v", p, err, r)
		}
		r.Body.Close()
	}
	post("/favorite", map[string]any{"fav": planner.Favorite{Name: "NGC 598"}, "on": true})
	post("/add", planner.Favorite{Name: "NGC 7000"})
	if a, b := <-got, <-got; a != "favorite" || b != "add NGC 7000" {
		t.Fatal(a, b)
	}
}
