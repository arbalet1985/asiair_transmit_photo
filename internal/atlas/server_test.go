package atlas

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestServer(t *testing.T) {
	s := NewServer()
	got := make(chan string, 4)
	s.OnFrame = func(i int, ra, dec, angle float64) {
		if i == 1 && ra == 10.5 && dec == 41.25 && angle == 30 {
			got <- "frame"
		}
	}
	s.OnOptics = func(o planner.Optics) {
		if o.Focal == 400 && o.Reducer == 0.8 {
			got <- "optics"
		}
	}
	ts := httptest.NewServer(s)
	defer ts.Close()
	q := "?t=" + s.token
	get := func(p string) (int, string) {
		r, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	if c, _ := get("/state?t=wrong"); c != http.StatusForbidden {
		t.Fatalf("a wrong token: %d", c)
	}
	if c, b := get("/" + q); c != 200 || !strings.Contains(b, "atlas.js") {
		t.Fatalf("%d %.80s", c, b)
	}
	if c, b := get("/atlas.js"); c != 200 || !strings.Contains(b, "aladin-lite@") {
		t.Fatalf("%d %.80s", c, b)
	}
	s.Publish(State{Targets: []planner.Target{{Name: "M 31"}, {Name: "M 33"}}, Sel: 1})
	if _, b := get("/state" + q + "&rev=0"); !strings.Contains(b, `"rev":1`) || !strings.Contains(b, "M 33") {
		t.Fatal(b)
	}
	// the state the page has: answered when it changes
	pollWait = 5 * time.Second
	done := make(chan string)
	go func() { _, b := get("/state" + q + "&rev=1"); done <- b }()
	time.Sleep(100 * time.Millisecond)
	s.Publish(State{Targets: []planner.Target{{Name: "NGC 7000"}}})
	select {
	case b := <-done:
		if !strings.Contains(b, "NGC 7000") || !strings.Contains(b, `"rev":2`) {
			t.Fatal(b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the change not told")
	}
	post := func(p, body string) int {
		r, err := http.Post(ts.URL+p+q, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := post("/frame", `{"i":1,"ra":10.5,"dec":41.25,"angle":30}`); c != http.StatusNoContent || <-got != "frame" {
		t.Fatal(c)
	}
	if c := post("/optics", `{"focal":400,"reducer":0.8,"pixel":3.76,"width":3008,"height":3008}`); c != http.StatusNoContent || <-got != "optics" {
		t.Fatal(c)
	}
	if c := post("/frame", `{`); c != http.StatusBadRequest {
		t.Fatal(c)
	}
}
