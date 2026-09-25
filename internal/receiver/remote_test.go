package receiver

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("секретный пароль")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(h, "секретный пароль") || checkPassword(h, "другой") || checkPassword("", "") || checkPassword("plain", "plain") {
		t.Fatal("password check")
	}
	if h2, _ := HashPassword("секретный пароль"); h2 == h {
		t.Fatal("no salt")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < loginMaxFails; i++ {
		if l.wait("1.2.3.4") != 0 {
			t.Fatalf("locked after %d", i)
		}
		l.fail("1.2.3.4")
	}
	if l.wait("1.2.3.4") == 0 || l.wait("5.6.7.8") != 0 {
		t.Fatal("lockout by address")
	}
	now = now.Add(loginWindow + time.Second)
	if l.wait("1.2.3.4") != 0 {
		t.Fatal("still locked after the window")
	}
}

// call sends a request to h like a browser: with the cookies in jar.
func call(t *testing.T, h http.Handler, jar map[string]string, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "203.0.113.7:5555"
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range jar {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		jar[c.Name] = c.Value
	}
	return rec
}

func TestRemoteAccess(t *testing.T) {
	loginDelay = time.Millisecond
	defer func() { loginDelay = time.Second }()
	s, _ := newTestServer(t)
	h := s.agentPortHandler("internet")
	local := s.uiHandler()
	jar := map[string]string{}

	// off: only the agent API answers on the agent port
	if rec := call(t, h, jar, "GET", "/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("page while off: %d", rec.Code)
	}
	if rec := call(t, h, jar, "POST", "/api/sync", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("agent API without token: %d", rec.Code)
	}

	// turned on without a password: nobody can log in
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "internet-on"}); rec.Code != 200 {
		t.Fatalf("internet-on: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, jar, "GET", "/", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("page before login: %d", rec.Code)
	}
	if rec := call(t, h, jar, "GET", "/login", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Пароль") {
		t.Fatalf("login page: %d", rec.Code)
	}
	if rec := call(t, h, jar, "POST", "/login", map[string]string{"password": ""}); rec.Code != http.StatusForbidden {
		t.Fatalf("login without a password set: %d", rec.Code)
	}
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "password", "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password accepted: %d", rec.Code)
	}
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "password", "password": "звёздное небо"}); rec.Code != 200 {
		t.Fatalf("password: %d %s", rec.Code, rec.Body)
	}
	// the setting is saved in receiver.yaml as a hash
	cfg, err := config.LoadReceiver(s.cfg.Path)
	if err != nil || !bool(cfg.RemoteInternet) || !checkPassword(cfg.RemotePassword, "звёздное небо") {
		t.Fatalf("saved settings: %+v %v", cfg, err)
	}

	if rec := call(t, h, jar, "POST", "/login", map[string]string{"password": "не тот"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec := call(t, h, jar, "POST", "/login", map[string]string{"password": "звёздное небо"}); rec.Code != http.StatusNoContent || jar[sessionCookie] == "" {
		t.Fatalf("login: %d", rec.Code)
	}
	rec := call(t, h, jar, "GET", "/ui/api/state", nil)
	var st uiState
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &st) != nil || !st.RemoteAccess.Remote {
		t.Fatalf("state after login: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/ui/api/connect", "/ui/api/token", "/ui/api/destdir", "/ui/api/remote"} {
		if rec := call(t, h, jar, "POST", p, map[string]string{}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s remotely: %d", p, rec.Code)
		}
	}
	if rec := call(t, h, jar, "POST", "/ui/api/update", map[string]string{"action": "settings"}); rec.Code != http.StatusForbidden {
		t.Fatalf("update settings remotely: %d", rec.Code)
	}
	if rec := call(t, h, jar, "POST", "/ui/api/pause", map[string]bool{"paused": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("pause remotely: %d", rec.Code)
	}
	// the files for installing the page open without a login
	if rec := call(t, h, map[string]string{}, "GET", "/manifest.webmanifest", nil); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "manifest") {
		t.Fatalf("manifest: %d", rec.Code)
	}

	// a session survives a restart of the receiver
	s2, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s2.agentPortHandler("internet"), jar, "GET", "/ui/api/state", nil); rec.Code != 200 {
		t.Fatalf("state after restart: %d", rec.Code)
	}

	// "logout everywhere" ends it
	call(t, local, map[string]string{}, "POST", "/ui/api/remote", map[string]string{"action": "logout-all"})
	if rec := call(t, h, jar, "GET", "/ui/api/state", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("state after logout-all: %d", rec.Code)
	}

	// lockout after too many wrong passwords
	for i := 0; i < loginMaxFails; i++ {
		call(t, h, jar, "POST", "/login", map[string]string{"password": "x"})
	}
	if rec := call(t, h, jar, "POST", "/login", map[string]string{"password": "звёздное небо"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", loginMaxFails, rec.Code)
	}

	// turned off again: the page is gone from the agent port
	call(t, local, map[string]string{}, "POST", "/ui/api/remote", map[string]string{"action": "internet-off"})
	if rec := call(t, h, jar, "GET", "/login", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("login page while off: %d", rec.Code)
	}
}

func TestStateGzipped(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/ui/api/state", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	s.uiHandler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("not compressed")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var st uiState
	if err := json.NewDecoder(zr).Decode(&st); err != nil || st.RemoteAccess.Remote {
		t.Fatalf("state: %v %+v", err, st.RemoteAccess)
	}
}
