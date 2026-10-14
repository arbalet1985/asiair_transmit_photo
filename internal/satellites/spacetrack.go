package satellites

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SpaceTrack is a session with space-track.org: unlike CelesTrak it keeps
// the orbits of the past (class gp_history). Each piece of the history is
// to be fetched once and kept. Limits: 30 queries a minute, 300 an hour.
type SpaceTrack struct {
	User, Password string
	UserAgent      string
	Base           string // https://www.space-track.org when empty
	client         *http.Client
}

func (s *SpaceTrack) base() string {
	if s.Base != "" {
		return strings.TrimRight(s.Base, "/")
	}
	return "https://www.space-track.org"
}

// Login opens the session (a cookie).
func (s *SpaceTrack) Login(ctx context.Context) error {
	jar, _ := cookiejar.New(nil)
	s.client = &http.Client{Jar: jar, Timeout: 10 * time.Minute}
	form := url.Values{"identity": {s.User}, "password": {s.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base()+"/ajaxauth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", s.UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Space-Track: вход: %s", resp.Status)
	}
	// a failed login answers 200 with {"Login":"Failed"}
	var r struct{ Login string }
	if json.Unmarshal(b, &r) == nil && strings.EqualFold(r.Login, "Failed") {
		return errors.New("Space-Track: неверный логин или пароль")
	}
	return nil
}

// Logout closes the session.
func (s *SpaceTrack) Logout(ctx context.Context) {
	if s.client == nil {
		return
	}
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+"/ajaxauth/logout", nil); err == nil {
		req.Header.Set("User-Agent", s.UserAgent)
		if resp, err := s.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	s.client = nil
}

// History fetches the elements of the whole catalogue with epochs from the
// day of from to the day of to and keeps for each object those nearest t:
// TLE text as Merge gives it, and the number of objects.
func (s *SpaceTrack) History(ctx context.Context, from, to, t time.Time) ([]byte, int, error) {
	if s.client == nil {
		return nil, 0, errors.New("Space-Track: нет входа")
	}
	const day = "2006-01-02"
	u := s.base() + "/basicspacedata/query/class/gp_history/EPOCH/" +
		from.UTC().Format(day) + "--" + to.UTC().Format(day) + "/orderby/NORAD_CAT_ID/format/3le"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, 0, fmt.Errorf("Space-Track: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return NearestElements(resp.Body, t)
}

// NearestElements reads TLE (or 3LE) text with many elements per object and
// keeps for each object those with the epoch nearest t.
func NearestElements(r io.Reader, t time.Time) ([]byte, int, error) {
	type entry struct {
		lines [3]string
		off   float64 // seconds between the epoch and t
	}
	m := map[string]entry{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 4096)
	var name, l1 string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \r")
		switch {
		case strings.HasPrefix(line, "1 ") && len(line) >= 69:
			l1 = line
		case strings.HasPrefix(line, "2 ") && len(line) >= 69 && l1 != "":
			if ep, err := tleEpoch(l1[18:32]); err == nil {
				id := strings.TrimSpace(l1[2:7])
				off := math.Abs(ep.Sub(t).Seconds())
				if old, ok := m[id]; !ok || off < old.off {
					m[id] = entry{[3]string{name, l1, line}, off}
				}
			}
			name, l1 = "", ""
		default:
			name, l1 = strings.TrimSpace(strings.TrimPrefix(line, "0 ")), ""
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b bytes.Buffer
	for _, id := range ids {
		e := m[id]
		b.WriteString(e.lines[0] + "\n" + e.lines[1] + "\n" + e.lines[2] + "\n")
	}
	return b.Bytes(), len(m), nil
}

// tleEpoch reads the epoch of line 1: yyddd.dddddddd.
func tleEpoch(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return time.Time{}, errors.New("эпоха TLE")
	}
	yy, err := strconv.Atoi(s[:2])
	if err != nil {
		return time.Time{}, err
	}
	d, err := strconv.ParseFloat(s[2:], 64)
	if err != nil {
		return time.Time{}, err
	}
	y := 2000 + yy
	if yy >= 57 {
		y = 1900 + yy
	}
	return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration((d - 1) * 864e11)), nil
}
