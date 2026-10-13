package receiver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/weather"
)

// handleUIWeather gives the forecast of the night [from, to] (unix ms) for
// the planner, with its summary on the clock tz (seconds east of UTC).
// Open-Meteo is asked only when the forecast kept is old (weather.Cache).
func (s *Server) handleUIWeather(w http.ResponseWriter, r *http.Request) {
	site := s.state.Snapshot().Site
	if !site.Set {
		http.Error(w, "задайте координаты обсерватории", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	ms := func(k string) time.Time { v, _ := strconv.ParseInt(q.Get(k), 10, 64); return time.UnixMilli(v) }
	from, to := ms("from"), ms("to")
	tz, _ := strconv.Atoi(q.Get("tz"))
	out := struct {
		Fetched time.Time      `json:"fetched,omitempty"`
		Error   string         `json:"error,omitempty"`
		Hours   []weather.Hour `json:"hours"`
		Text    string         `json:"text"`
		Clear   float64        `json:"clear"` // clear hours of the night
	}{Hours: []weather.Hour{}}
	f, err := s.weather.Get(r.Context(), site.Lat, site.Lon)
	if err != nil {
		out.Error = err.Error()
	}
	if f != nil {
		out.Fetched = f.Fetched
		if h := f.Between(from, to); len(h) > 0 {
			out.Hours = h
			out.Text = weather.Summary(h, from, to, time.FixedZone("", tz))
			out.Clear = weather.Clear(h, from, to)
		}
	}
	writeJSON(w, out)
}
