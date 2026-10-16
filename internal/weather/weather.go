// Package weather gives the forecast of the clouds, the wind and the dew
// at the observatory from Open-Meteo (free, no key, for non-commercial use
// within its limits). The forecast is kept on disk and asked for again only
// when it is old: a few requests a day at most.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hour is the forecast of one hour (starting at T).
type Hour struct {
	T        int64   `json:"t"`     // unix milliseconds
	Cloud    int     `json:"cloud"` // total cover, percent
	Low      int     `json:"low"`
	Mid      int     `json:"mid"`
	High     int     `json:"high"`
	Humidity int     `json:"hum"`
	Temp     float64 `json:"temp"` // °C at 2 m
	Dew      float64 `json:"dew"`  // dew point, °C
	Wind     float64 `json:"wind"` // m/s at 10 m
	Gust     float64 `json:"gust"`
	Precip   int     `json:"precip"` // probability, percent
}

// Forecast for a site, as fetched.
type Forecast struct {
	Lat     float64   `json:"lat"`
	Lon     float64   `json:"lon"`
	Fetched time.Time `json:"fetched"`
	Hours   []Hour    `json:"hours"`
}

// Between: the hours that overlap [from, to].
func (f *Forecast) Between(from, to time.Time) []Hour {
	if f == nil {
		return nil
	}
	var out []Hour
	for _, h := range f.Hours {
		t := time.UnixMilli(h.T)
		if t.Add(time.Hour).After(from) && t.Before(to) {
			out = append(out, h)
		}
	}
	return out
}

// Endpoint of the forecast (a variable for the tests).
var Endpoint = "https://api.open-meteo.com/v1/forecast"

// Fetch asks Open-Meteo for the forecast of a site: the day before (a
// night begun yesterday) and the next 7 days, hour by hour.
func Fetch(ctx context.Context, client *http.Client, userAgent string, lat, lon float64) (*Forecast, error) {
	q := url.Values{
		"latitude":        {strconv.FormatFloat(lat, 'f', 3, 64)},
		"longitude":       {strconv.FormatFloat(lon, 'f', 3, 64)},
		"hourly":          {"cloud_cover,cloud_cover_low,cloud_cover_mid,cloud_cover_high,relative_humidity_2m,temperature_2m,dew_point_2m,wind_speed_10m,wind_gusts_10m,precipitation_probability"},
		"wind_speed_unit": {"ms"},
		"timeformat":      {"unixtime"},
		"timezone":        {"GMT"},
		"past_days":       {"1"},
		"forecast_days":   {"7"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Reason string `json:"reason"`
		}
		json.Unmarshal(b, &e)
		if e.Reason == "" {
			e.Reason = resp.Status
		}
		return nil, fmt.Errorf("Open-Meteo: %s", e.Reason)
	}
	return parse(b, lat, lon)
}

func parse(b []byte, lat, lon float64) (*Forecast, error) {
	var r struct {
		Hourly struct {
			Time     []int64    `json:"time"`
			Cloud    []*float64 `json:"cloud_cover"`
			Low      []*float64 `json:"cloud_cover_low"`
			Mid      []*float64 `json:"cloud_cover_mid"`
			High     []*float64 `json:"cloud_cover_high"`
			Humidity []*float64 `json:"relative_humidity_2m"`
			Temp     []*float64 `json:"temperature_2m"`
			Dew      []*float64 `json:"dew_point_2m"`
			Wind     []*float64 `json:"wind_speed_10m"`
			Gust     []*float64 `json:"wind_gusts_10m"`
			Precip   []*float64 `json:"precipitation_probability"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("Open-Meteo: ответ не прочитан: %w", err)
	}
	h := r.Hourly
	if len(h.Time) == 0 || len(h.Cloud) != len(h.Time) {
		return nil, errors.New("Open-Meteo: в ответе нет облачности")
	}
	at := func(a []*float64, i int) float64 {
		if i < len(a) && a[i] != nil {
			return *a[i]
		}
		return 0
	}
	f := &Forecast{Lat: lat, Lon: lon, Fetched: time.Now()}
	for i, t := range h.Time {
		if h.Cloud[i] == nil {
			continue // beyond the models
		}
		f.Hours = append(f.Hours, Hour{T: t * 1000, Cloud: int(at(h.Cloud, i)), Low: int(at(h.Low, i)), Mid: int(at(h.Mid, i)),
			High: int(at(h.High, i)), Humidity: int(at(h.Humidity, i)), Temp: at(h.Temp, i), Dew: at(h.Dew, i),
			Wind: at(h.Wind, i), Gust: at(h.Gust, i), Precip: int(at(h.Precip, i))})
	}
	return f, nil
}

// Cache keeps the forecast of one site in a file and asks for a new one
// only when it is older than MinAge (and no sooner than Retry after a
// failed request): the programs are started and the planner opened often,
// Open-Meteo is asked a few times a day.
type Cache struct {
	File      string
	UserAgent string
	MinAge    time.Duration // 0: 3 hours
	Retry     time.Duration // 0: 1 hour; 10 minutes while there is no forecast to show
	Client    *http.Client  // nil: one with a timeout

	mu     sync.Mutex
	loaded bool
	saved  cacheFile
}

type cacheFile struct {
	Forecast *Forecast `json:"forecast,omitempty"`
	Tried    time.Time `json:"tried"` // the last request, done or not
	Err      string    `json:"err,omitempty"`
}

// near: the forecast is of the same place (within a few km).
func near(f *Forecast, lat, lon float64) bool {
	return f != nil && math.Abs(f.Lat-lat) < 0.05 && math.Abs(f.Lon-lon) < 0.05
}

// Get returns the forecast of the site, asking Open-Meteo only when the one
// kept is old or of another place. When asking fails the one kept (if of
// this place) comes back with the error.
func (c *Cache) Get(ctx context.Context, lat, lon float64) (*Forecast, error) {
	c.mu.Lock() // one request at a time; the others wait for it
	defer c.mu.Unlock()
	if !c.loaded {
		c.loaded = true
		if b, err := os.ReadFile(c.File); err == nil {
			json.Unmarshal(b, &c.saved)
		}
	}
	minAge, retry := c.MinAge, c.Retry
	if minAge <= 0 {
		minAge = 3 * time.Hour
	}
	if retry <= 0 {
		retry = time.Hour
	}
	kept := c.saved.Forecast
	if !near(kept, lat, lon) {
		kept = nil
	}
	if kept == nil && c.Retry <= 0 {
		retry = 10 * time.Minute // nothing to show: Open-Meteo is asked again sooner
	}
	now := time.Now()
	if kept != nil && now.Sub(kept.Fetched) < minAge {
		return kept, nil
	}
	if now.Sub(c.saved.Tried) < retry && (kept != nil || c.saved.Err != "") {
		if c.saved.Err != "" {
			return kept, errors.New(c.saved.Err)
		}
		return kept, nil
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second} // Open-Meteo is slow to answer at times
	}
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	f, err := Fetch(fctx, client, c.UserAgent, lat, lon)
	cancel()
	if ctx.Err() != nil {
		return kept, ctx.Err() // the asker left: not counted as a try
	}
	c.saved.Tried, c.saved.Err = now, ""
	if err != nil {
		c.saved.Err = err.Error()
	} else {
		c.saved.Forecast, kept = f, f
	}
	if b, merr := json.Marshal(c.saved); merr == nil && c.File != "" {
		os.MkdirAll(filepath.Dir(c.File), 0o755)
		tmp := c.File + ".new"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, c.File)
		}
	}
	return kept, err
}

// sky classes of an hour by its total cloud cover
func skyClass(cloud int) int {
	switch {
	case cloud <= 20:
		return 0
	case cloud <= 60:
		return 1
	}
	return 2
}

var skyName = [3]string{"ясно", "переменная облачность", "облачно"}

// Dewy: the air is near its dew point (optics dew up, fog may come).
func (h Hour) Dewy() bool { return h.Humidity >= 95 || h.Humidity > 0 && h.Temp-h.Dew <= 1.5 }

// Summary tells the night [from, to] in words, the times on the clock of
// zone: the clear and cloudy spells, the wind, the dew, the rain. "" when
// the forecast does not reach the night.
func Summary(hours []Hour, from, to time.Time, zone *time.Location) string {
	if len(hours) == 0 {
		return ""
	}
	hm := func(ms int64) string { return time.UnixMilli(ms).In(zone).Format("15:04") }
	type spell struct {
		class    int
		from, to int64
		max      int
	}
	var sp []spell
	var wind, gust float64
	var precip, humid int
	dew := false
	for _, h := range hours {
		end := h.T + 3600e3
		t0, t1 := max(h.T, from.UnixMilli()), min(end, to.UnixMilli())
		if t1 <= t0 {
			continue
		}
		c := skyClass(h.Cloud)
		if n := len(sp); n > 0 && sp[n-1].class == c {
			sp[n-1].to = t1
			sp[n-1].max = max(sp[n-1].max, h.Cloud)
		} else {
			sp = append(sp, spell{c, t0, t1, h.Cloud})
		}
		wind, gust = math.Max(wind, h.Wind), math.Max(gust, h.Gust)
		precip, humid = max(precip, h.Precip), max(humid, h.Humidity)
		dew = dew || h.Dewy()
	}
	if len(sp) == 0 {
		return ""
	}
	var parts []string
	if len(sp) == 1 {
		parts = append(parts, fmt.Sprintf("%s всю ночь (облачность до %d%%)", skyName[sp[0].class], sp[0].max))
	} else {
		for _, s := range sp {
			parts = append(parts, fmt.Sprintf("%s %s–%s", skyName[s.class], hm(s.from), hm(s.to)))
		}
	}
	r := []rune(strings.Join(parts, ", ")) // Cyrillic: the first letter is a rune
	t := strings.ToUpper(string(r[0])) + string(r[1:])
	t += fmt.Sprintf("; ветер до %.0f м/с, порывы до %.0f", wind, gust)
	if dew {
		t += fmt.Sprintf("; влажность до %d%% — роса или туман, нужен обогрев", humid)
	}
	if precip >= 30 {
		t += fmt.Sprintf("; осадки с вероятностью до %d%%", precip)
	}
	return t + "."
}

// Clear: the hours of the night [from, to] with a clear sky (cloud cover
// up to 20%), in hours.
func Clear(hours []Hour, from, to time.Time) float64 {
	var ms int64
	for _, h := range hours {
		t0, t1 := max(h.T, from.UnixMilli()), min(h.T+3600e3, to.UnixMilli())
		if t1 > t0 && skyClass(h.Cloud) == 0 {
			ms += t1 - t0
		}
	}
	return float64(ms) / 3600e3
}
