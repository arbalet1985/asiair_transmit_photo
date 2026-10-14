package aircraft

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient(t *testing.T) {
	tokens := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "s" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			tokens++
			w.Write([]byte(`{"access_token":"T","expires_in":1800}`))
		case "/api/states/all":
			if r.Header.Get("Authorization") != "Bearer T" {
				http.Error(w, "", http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("lamin") != "55.3000" {
				http.Error(w, "box "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"time":1760470000,"states":[
				["4ca7b3","AFL1234 ","Russia",1760469998,1760469999,60.5,56.9,10350.0,false,230.5,70.2,0.0,null,10500.0,"1000",false,0],
				["aaaaaa","","Russia",1760469990,1760469999,60.6,56.7,0.0,true,0,0,0,null,null,null,false,0],
				["bbbbbb","","Russia",null,1760469999,null,null,null,false,null,null,null,null,null,null,false,0]]}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	bad := &Client{ID: "x", Secret: "no", TokenURL: srv.URL + "/token", APIURL: srv.URL + "/api"}
	if _, _, err := bad.States(ctx, Around(56.8, 60.3, 1.5)); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad client: %v", err)
	}
	c := &Client{ID: "x", Secret: "s", TokenURL: srv.URL + "/token", APIURL: srv.URL + "/api"}
	st, at, err := c.States(ctx, Around(56.8, 60.3, 1.5))
	if err != nil || len(st) != 1 || at.Unix() != 1760470000 {
		t.Fatalf("%v %+v", err, st)
	}
	s := st[0]
	if s.ICAO != "4ca7b3" || s.Callsign != "AFL1234" || s.AltM != 10500 || s.Speed != 230.5 || s.T.Unix() != 1760469998 {
		t.Fatalf("%+v", s)
	}
	c.States(ctx, Around(56.8, 60.3, 1.5))
	if tokens != 1 {
		t.Fatalf("token asked %d times", tokens)
	}
	b := Around(56.8, 60.3, 1.5)
	if area := (b.LatMax - b.LatMin) * (b.LonMax - b.LonMin); area > 25 {
		t.Fatalf("area %v", area)
	}
}

func TestLog(t *testing.T) {
	l := &Log{Dir: t.TempDir(), Keep: 60}
	t0 := time.Date(2026, 10, 14, 23, 59, 0, 0, time.UTC) // across midnight
	for i := 0; i < 8; i++ {
		at := t0.Add(time.Duration(i) * 25 * time.Second)
		var st []State
		if i%2 == 0 {
			st = []State{{T: at.Add(-time.Second), ICAO: "4ca7b3", Callsign: "AFL1234", Lat: 56.9, Lon: 60.5 + 0.01*float64(i), AltM: 10500, Speed: 230}}
		}
		if err := l.Add(at, st); err != nil {
			t.Fatal(err)
		}
	}
	planes, covered, err := l.Window(t0.Add(30*time.Second), t0.Add(2*time.Minute), 90*time.Second)
	if err != nil || !covered || len(planes) != 1 || len(planes[0].Points) != 4 || planes[0].Callsign != "AFL1234" {
		t.Fatalf("%v %v %+v", err, covered, planes)
	}
	if _, covered, _ := l.Window(t0.Add(time.Minute), t0.Add(10*time.Minute), 90*time.Second); covered {
		t.Fatal("covered past the recording")
	}
	l.Prune(t0.AddDate(0, 0, 62))
	if planes, _, _ := l.Window(t0, t0.Add(time.Minute), time.Minute); len(planes) != 0 || !strings.HasPrefix(l.file(t0), l.Dir) {
		t.Fatalf("not pruned: %+v", planes)
	}
}
