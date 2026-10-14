// Package aircraft records the planes near the observatory from OpenSky
// Network (ADS-B), so the tracks they leave on the frames can be told.
// OpenSky keeps the positions of all planes only for an hour back: they are
// recorded as they fly, while it is dark at the observatory.
package aircraft

import (
	"bufio"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// Client asks OpenSky with an API client (OAuth2 client credentials).
type Client struct {
	ID, Secret string
	UserAgent  string
	TokenURL   string // the OpenSky one when empty
	APIURL     string // https://opensky-network.org/api when empty

	mu      sync.Mutex
	token   string
	expires time.Time
	http    http.Client
}

const tokenURL = "https://auth.opensky-network.org/auth/realms/opensky-network/protocol/openid-connect/token"

// ErrAuth: the API client is not accepted.
var ErrAuth = errors.New("OpenSky: API-клиент не принят — проверьте client id и secret")

// RateLimited: the credits of the day are spent; Retry: when to ask again.
type RateLimited struct{ Retry time.Duration }

func (e RateLimited) Error() string {
	return fmt.Sprintf("OpenSky: кредиты API на сегодня израсходованы, повтор через %s", e.Retry.Round(time.Minute))
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	u := c.TokenURL
	if u == "" {
		u = tokenURL
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.ID}, "client_secret": {c.Secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.UserAgent)
	c.http.Timeout = time.Minute
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenSky: токен: %s", resp.Status)
	}
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.AccessToken == "" {
		return "", fmt.Errorf("OpenSky: токен не получен: %v", err)
	}
	c.token, c.expires = r.AccessToken, time.Now().Add(time.Duration(r.ExpiresIn)*time.Second)
	return c.token, nil
}

// Box is an area: degrees.
type Box struct{ LatMin, LatMax, LonMin, LonMax float64 }

// Around: the box of half sides dLat by the same distance east and west
// (so that its area stays under 25 square degrees: one credit a query).
func Around(lat, lon, dLat float64) Box {
	dLon := dLat / math.Max(0.2, math.Cos(lat*math.Pi/180))
	dLon = math.Min(dLon, 12.4/(2*dLat)) // area = 2dLat * 2dLon <= 24.8
	return Box{lat - dLat, lat + dLat, lon - dLon, lon + dLon}
}

// State: a plane at the time of a query.
type State struct {
	T        time.Time // of the position
	ICAO     string
	Callsign string
	Lat, Lon float64
	AltM     float64 // geometric, else barometric, m
	Speed    float64 // m/s
	Track    float64 // degrees from north
	Vert     float64 // m/s
}

// States: the planes in the box now (those in the air with a position).
func (c *Client) States(ctx context.Context, b Box) ([]State, time.Time, error) {
	tok, err := c.getToken(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	base := c.APIURL
	if base == "" {
		base = "https://opensky-network.org/api"
	}
	q := url.Values{}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	q.Set("lamin", f(b.LatMin))
	q.Set("lamax", f(b.LatMax))
	q.Set("lomin", f(b.LonMin))
	q.Set("lomax", f(b.LonMax))
	q.Set("extended", "0")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/states/all?"+q.Encode(), nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", c.UserAgent)
	c.http.Timeout = time.Minute
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		return nil, time.Time{}, ErrAuth
	case http.StatusTooManyRequests:
		s, _ := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Retry-After-Seconds"))
		return nil, time.Time{}, RateLimited{time.Duration(max(s, 600)) * time.Second}
	default:
		return nil, time.Time{}, fmt.Errorf("OpenSky: %s", resp.Status)
	}
	var r struct {
		Time   int64   `json:"time"`
		States [][]any `json:"states"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return nil, time.Time{}, err
	}
	var out []State
	for _, v := range r.States {
		if len(v) < 14 {
			continue
		}
		num := func(i int) (float64, bool) { x, ok := v[i].(float64); return x, ok }
		str := func(i int) string { s, _ := v[i].(string); return strings.TrimSpace(s) }
		lon, ok1 := num(5)
		lat, ok2 := num(6)
		tp, ok3 := num(3)
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		if g, _ := v[8].(bool); g {
			continue // on the ground
		}
		alt, ok := num(13)
		if !ok {
			if alt, ok = num(7); !ok {
				continue
			}
		}
		s := State{T: time.Unix(int64(tp), 0), ICAO: str(0), Callsign: str(1), Lat: lat, Lon: lon, AltM: alt}
		s.Speed, _ = num(9)
		s.Track, _ = num(10)
		s.Vert, _ = num(11)
		out = append(out, s)
	}
	return out, time.Unix(r.Time, 0), nil
}

// Log keeps what was recorded: a text file a day (UTC), lines
// "P <unix>" for each query made and "S <unix> <icao> <callsign> <lat>
// <lon> <alt m> <speed> <track> <vert>" for each plane in its answer.
type Log struct {
	Dir  string
	Keep int // days kept
	mu   sync.Mutex
}

func (l *Log) file(t time.Time) string {
	return filepath.Join(l.Dir, "planes-"+t.UTC().Format("20060102")+".txt")
}

// Add writes the answer of a query made at t.
func (l *Log) Add(t time.Time, states []State) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.file(t), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "P %d\n", t.Unix())
	for _, s := range states {
		cs := s.Callsign
		if cs == "" {
			cs = "-"
		}
		fmt.Fprintf(w, "S %d %s %s %.5f %.5f %.0f %.1f %.1f %.1f\n", s.T.Unix(), s.ICAO, cs, s.Lat, s.Lon, s.AltM, s.Speed, s.Track, s.Vert)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Prune deletes the days older than Keep.
func (l *Log) Prune(now time.Time) {
	ents, _ := os.ReadDir(l.Dir)
	cut := now.UTC().AddDate(0, 0, -max(l.Keep, 1)).Format("20060102")
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "planes-") && strings.HasSuffix(n, ".txt") && strings.TrimSuffix(strings.TrimPrefix(n, "planes-"), ".txt") < cut {
			os.Remove(filepath.Join(l.Dir, n))
		}
	}
}

// Window: the planes recorded from..to, and whether the recording covers
// that time (queries at most gap apart from before from to after to).
func (l *Log) Window(from, to time.Time, gap time.Duration) ([]*satellites.Plane, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lo, hi := from.Add(-2*time.Minute), to.Add(2*time.Minute)
	byID := map[string]*satellites.Plane{}
	var polls []time.Time
	for d := lo.UTC().Truncate(24 * time.Hour); !d.After(hi); d = d.Add(24 * time.Hour) {
		f, err := os.Open(l.file(d))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			x := strings.Fields(sc.Text())
			if len(x) < 2 {
				continue
			}
			u, err := strconv.ParseInt(x[1], 10, 64)
			if err != nil {
				continue
			}
			t := time.Unix(u, 0)
			if t.Before(lo) || t.After(hi) {
				continue
			}
			switch {
			case x[0] == "P":
				polls = append(polls, t)
			case x[0] == "S" && len(x) >= 10:
				v := make([]float64, 6)
				ok := true
				for i := range v {
					if v[i], err = strconv.ParseFloat(x[4+i], 64); err != nil {
						ok = false
					}
				}
				if !ok {
					continue
				}
				p := byID[x[2]]
				if p == nil {
					p = &satellites.Plane{ICAO: x[2]}
					byID[x[2]] = p
				}
				if x[3] != "-" {
					p.Callsign = x[3]
				}
				p.Points = append(p.Points, satellites.PlanePoint{T: t, Lat: v[0], Lon: v[1], AltM: v[2], Speed: v[3], Track: v[4], Vert: v[5]})
			}
		}
		f.Close()
	}
	sort.Slice(polls, func(i, j int) bool { return polls[i].Before(polls[j]) })
	covered := len(polls) > 0 && !polls[0].After(from) && !polls[len(polls)-1].Before(to)
	for i := 1; covered && i < len(polls); i++ {
		if polls[i].Sub(polls[i-1]) > gap {
			covered = false
		}
	}
	var out []*satellites.Plane
	for _, p := range byID {
		sort.Slice(p.Points, func(i, j int) bool { return p.Points[i].T.Before(p.Points[j].T) })
		// the same position answered twice: once
		pts := p.Points[:0]
		for _, q := range p.Points {
			if n := len(pts); n > 0 && pts[n-1].T.Equal(q.T) {
				continue
			}
			pts = append(pts, q)
		}
		p.Points = pts
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ICAO < out[j].ICAO })
	return out, covered, nil
}
