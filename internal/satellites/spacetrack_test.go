package satellites

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNearestElements(t *testing.T) {
	c := sgp4Cases[0] // epoch 26285.19 = 2026-10-12 04:36
	before := strings.Replace(c.l1, "26285.19178549", "26283.19178549", 1)
	after := strings.Replace(c.l1, "26285.19178549", "26286.00000000", 1)
	in := "0 " + tle(c.name, before, c.l2) + "0 " + tle(c.name, c.l1, c.l2) + "0 " + tle(c.name, after, c.l2) +
		"0 " + tle(sgp4Cases[1].name, sgp4Cases[1].l1, sgp4Cases[1].l2)
	// nearest the morning of the 12th: the elements of 04:36, not of the 13th
	text, n, err := NearestElements(strings.NewReader(in), time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC))
	if err != nil || n != 2 || !strings.Contains(string(text), c.l1) || strings.Contains(string(text), before) || strings.Contains(string(text), after) {
		t.Fatalf("%d %v\n%s", n, err, text)
	}
	o, _ := ParseTLE(strings.NewReader(string(text)))
	if len(o) != 2 || o[0].Name != c.name {
		t.Fatalf("%+v", o)
	}
	// nearest the 13th: the later elements
	text, _, _ = NearestElements(strings.NewReader(in), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(string(text), after) {
		t.Fatalf("%s", text)
	}
}

func TestSpaceTrack(t *testing.T) {
	c := sgp4Cases[1]
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ajaxauth/login":
			r.ParseForm()
			if r.Form.Get("identity") != "me@x" || r.Form.Get("password") != "pw" {
				w.Write([]byte(`{"Login":"Failed"}`))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "chocolatechip", Value: "1", Path: "/"})
			w.Write([]byte(`""`))
		case strings.HasPrefix(r.URL.Path, "/basicspacedata/"):
			if _, err := r.Cookie("chocolatechip"); err != nil {
				http.Error(w, "not logged in", http.StatusUnauthorized)
				return
			}
			query = r.URL.Path
			w.Write([]byte("0 " + tle(c.name, c.l1, c.l2)))
		case r.URL.Path == "/ajaxauth/logout":
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	bad := &SpaceTrack{User: "me@x", Password: "wrong", Base: srv.URL}
	if err := bad.Login(ctx); err == nil || !strings.Contains(err.Error(), "неверный") {
		t.Fatalf("bad login: %v", err)
	}
	s := &SpaceTrack{User: "me@x", Password: "pw", Base: srv.URL}
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 9, 12, 21, 0, 0, 0, time.UTC)
	text, n, err := s.History(ctx, night.Add(-72*time.Hour), night.Add(24*time.Hour), night)
	if err != nil || n != 1 || !strings.Contains(string(text), c.l1) {
		t.Fatalf("%d %v %s", n, err, text)
	}
	if want := "/basicspacedata/query/class/gp_history/EPOCH/2026-09-09--2026-09-13/orderby/NORAD_CAT_ID/format/3le"; query != want {
		t.Fatalf("query %s", query)
	}
	s.Logout(ctx)
}

func TestStoreHistory(t *testing.T) {
	s := Store{Dir: t.TempDir(), Keep: 1}
	c := sgp4Cases[1]
	text := []byte(tle(c.name, c.l1, c.l2))
	night := time.Date(2026, 9, 12, 21, 0, 0, 0, time.UTC)
	s.SaveHistory(text, night)
	s.Save(text, time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC))
	s.Save(text, time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC))
	// the catalogue of the past is kept, the downloads pruned
	cats := s.Catalogs()
	if len(cats) != 2 || !cats[0].History || !cats[0].Time.Equal(night) || cats[1].History {
		t.Fatalf("%+v", cats)
	}
	if d := s.Downloads(); len(d) != 1 || d[0].History {
		t.Fatalf("%+v", d)
	}
	if n, _ := s.Nearest(night.Add(5 * time.Hour)); !n.History {
		t.Fatalf("nearest %+v", n)
	}
}
