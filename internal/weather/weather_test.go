package weather

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// answer: 12 hours from 18:00 UTC: clear until 22:00, cloudy 22–01, clear
// after; humid at the end.
func answer(t0 time.Time) string {
	var ts, cl, hum []string
	for i := 0; i < 12; i++ {
		ts = append(ts, fmt.Sprint(t0.Add(time.Duration(i)*time.Hour).Unix()))
		c, h := 5, 70
		if i >= 4 && i < 7 {
			c = 90
		}
		if i >= 9 {
			h = 97
		}
		cl = append(cl, fmt.Sprint(c))
		hum = append(hum, fmt.Sprint(h))
	}
	j := strings.Join
	n := func(v string) string { return j(strings.Split(strings.Repeat(v+",", 12), ",")[:12], ",") }
	return `{"hourly":{"time":[` + j(ts, ",") + `],"cloud_cover":[` + j(cl, ",") + `],"cloud_cover_low":[` + n("1") +
		`],"cloud_cover_mid":[` + n("2") + `],"cloud_cover_high":[` + n("3") + `],"relative_humidity_2m":[` + j(hum, ",") +
		`],"temperature_2m":[` + n("8") + `],"dew_point_2m":[` + n("2") + `],"wind_speed_10m":[` + n("3.2") +
		`],"wind_gusts_10m":[` + n("7.6") + `],"precipitation_probability":[` + n("10") + `]}}`
}

func TestCache(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("hourly") == "" || r.Header.Get("User-Agent") != "test/1" {
			t.Errorf("request %s", r.URL)
		}
		if fail {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":true,"reason":"Too many requests"}`)
			return
		}
		fmt.Fprint(w, answer(t0))
	}))
	defer srv.Close()
	Endpoint = srv.URL
	file := filepath.Join(t.TempDir(), "weather.json")
	c := &Cache{File: file, UserAgent: "test/1"}
	ctx := context.Background()
	f, err := c.Get(ctx, 56.8, 60.3)
	if err != nil || len(f.Hours) != 12 || f.Hours[4].Cloud != 90 || f.Hours[0].Wind != 3.2 || f.Hours[0].T != t0.UnixMilli() {
		t.Fatalf("%v %+v", err, f)
	}
	// fresh: not asked again, nor by a program started anew
	c.Get(ctx, 56.81, 60.3)
	(&Cache{File: file, UserAgent: "test/1"}).Get(ctx, 56.8, 60.3)
	if calls.Load() != 1 {
		t.Fatalf("asked %d times", calls.Load())
	}
	// old: asked again; refused: the old one with the error, and no new
	// request until the retry time
	c.saved.Forecast.Fetched = time.Now().Add(-4 * time.Hour)
	c.saved.Tried = c.saved.Forecast.Fetched
	fail = true
	f, err = c.Get(ctx, 56.8, 60.3)
	if err == nil || !strings.Contains(err.Error(), "Too many requests") || f == nil || calls.Load() != 2 {
		t.Fatalf("%v %v %d", err, f, calls.Load())
	}
	if _, err = c.Get(ctx, 56.8, 60.3); err == nil || calls.Load() != 2 {
		t.Fatalf("asked again at once: %d", calls.Load())
	}
	// another place: nothing kept for it, and the last try was a failure
	// a moment ago: the error, no request
	if f, err = c.Get(ctx, 45, 10); f != nil || err == nil || calls.Load() != 2 {
		t.Fatalf("%v %v %d", f, err, calls.Load())
	}
	// nothing to show there: asked again after 10 minutes, not an hour
	c.saved.Tried = time.Now().Add(-11 * time.Minute)
	fail = false
	if f, err = c.Get(ctx, 45, 10); f == nil || err != nil || calls.Load() != 3 {
		t.Fatalf("not asked again after 11 minutes: %v %v %d", f, err, calls.Load())
	}
}

func TestSummary(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)
	f, err := parse([]byte(answer(t0)), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("UTC+5", 5*3600)
	from, to := t0.Add(90*time.Minute), t0.Add(11*time.Hour) // 00:30–10:00 there
	s := Summary(f.Between(from, to), from, to, zone)
	want := "Ясно 00:30–03:00, облачно 03:00–06:00, ясно 06:00–10:00; ветер до 3 м/с, порывы до 8; влажность до 97% — роса или туман, нужен обогрев."
	if s != want {
		t.Fatalf("%s\nwant %s", s, want)
	}
	if c := Clear(f.Hours, from, to); c < 6.4 || c > 6.6 {
		t.Fatalf("clear %.2f h", c)
	}
	if Summary(nil, from, to, zone) != "" {
		t.Fatal("no forecast: no text")
	}
}
