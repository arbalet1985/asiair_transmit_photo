package receiver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/weather"
)

// The planner gets the forecast of the night with its summary; Open-Meteo
// is asked once however often the page asks.
func TestUIWeather(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	om := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var ts, cl []string
		for i := 0; i < 24; i++ {
			ts = append(ts, fmt.Sprint(t0.Add(time.Duration(i)*time.Hour).Unix()))
			cl = append(cl, fmt.Sprint(map[bool]int{true: 80, false: 0}[i >= 6]))
		}
		fmt.Fprintf(w, `{"hourly":{"time":[%s],"cloud_cover":[%s]}}`, strings.Join(ts, ","), strings.Join(cl, ","))
	}))
	defer om.Close()
	old := weather.Endpoint
	weather.Endpoint = om.URL
	defer func() { weather.Endpoint = old }()

	s, _ := newTestServer(t)
	ui := s.uiHandler()
	path := fmt.Sprintf("/ui/api/weather?from=%d&to=%d&tz=18000", t0.Add(time.Hour).UnixMilli(), t0.Add(11*time.Hour).UnixMilli())
	if code := uiCall(t, ui, "GET", path, nil, nil); code != http.StatusBadRequest {
		t.Fatalf("without the site: %d", code)
	}
	if err := s.state.SetSite(Site{Lat: 56.8, Lon: 60.3, Set: true}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hours []weather.Hour `json:"hours"`
		Text  string         `json:"text"`
		Clear float64        `json:"clear"`
		Error string         `json:"error"`
	}
	for i := 0; i < 3; i++ {
		if code := uiCall(t, ui, "GET", path, nil, &got); code != 200 {
			t.Fatalf("code %d", code)
		}
	}
	if calls.Load() != 1 || len(got.Hours) != 10 || got.Clear != 5 || got.Error != "" ||
		!strings.HasPrefix(got.Text, "Ясно 21:00–02:00, облачно 02:00–07:00") {
		t.Fatalf("asked %d: %+v", calls.Load(), got)
	}
}
