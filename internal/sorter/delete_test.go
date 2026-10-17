package sorter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestDeleteEverywhere(t *testing.T) {
	var queued, here []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ui/api/state":
			w.Write([]byte(`{"files":[
				{"path":"A/on_asiair.fit","local":true,"remote":true},
				{"path":"A/only_here.fit","local":true},
				{"path":"A/locked.fit","local":true},
				{"path":"A/queued.fit","local":true,"remote":true,"pending":true},
				{"path":"A/queued_keep.fit","local":true,"remote":true,"pending":true}],
				"commands":[{"type":"delete","path":"A/queued.fit","also_local":true,"status":"pending"},
				{"type":"delete","path":"A/queued_keep.fit","status":"pending"}]}`))
		case "/ui/api/delete":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			var req struct {
				Paths     []string `json:"paths"`
				AlsoLocal bool     `json:"also_local"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if !req.AlsoLocal {
				t.Error("the copy of the receiver is not deleted")
			}
			queued = req.Paths
			w.Write([]byte(`{"queued":1}`))
		case "/ui/api/delete_local":
			var req struct{ Paths []string }
			json.NewDecoder(r.Body).Decode(&req)
			here = req.Paths
			w.Write([]byte(`{"deleted":1,"errors":["A/locked.fit: занят"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, err := NewReceiver(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"A/on_asiair.fit", "A/only_here.fit", "A/locked.fit", "A/queued.fit", "A/queued_keep.fit", "A/gone.fit"}
	res, err := r.DeleteEverywhere(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queued, []string{"A/on_asiair.fit"}) {
		t.Errorf("queued %v", queued)
	}
	if !slices.Equal(here, []string{"A/only_here.fit", "A/locked.fit"}) {
		t.Errorf("deleted here %v", here)
	}
	if res.Queued != 2 || res.Deleted != 1 || res.Absent != 1 {
		t.Errorf("result %+v", res)
	}
	for _, p := range paths {
		want := p != "A/locked.fit" && p != "A/queued_keep.fit"
		if res.Done(p) != want {
			t.Errorf("%s: done %v, want %v (%v)", p, res.Done(p), want, res.Failed[p])
		}
	}
}

func TestSatInfoFromReceiver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ui/api/satinfo":
			if r.URL.Query().Get("norad") != "25544" || r.URL.Query().Get("name") != "ISS" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"norad":25544,"name":"ISS (ZARYA)","photo":{"source":"SatNOGS DB"},"links":[{"name":"N2YO","url":"u"}],"rows":[["a","b"]]}`))
		case "/ui/api/satinfo/photo":
			w.Write([]byte("jpeg"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, _ := NewReceiver(srv.URL, "", "")
	i, err := r.SatInfo(context.Background(), 25544, "ISS")
	if err != nil || i.Name != "ISS (ZARYA)" || i.Photo == nil || len(i.Links) != 1 {
		t.Fatalf("%+v %v", i, err)
	}
	if b, err := r.SatPhoto(context.Background(), 25544); err != nil || string(b) != "jpeg" {
		t.Errorf("photo %q %v", b, err)
	}
	if _, err := r.SatInfo(context.Background(), 1, "x"); err == nil {
		t.Error("an error is not told")
	}
}
