package receiver

import (
	"encoding/base32"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

// RFC 6238, appendix B (SHA-1), the last 6 of the 8 digits.
func TestTOTPVectors(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	for sec, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 20000000000: "353130"} {
		if got := totpCode(secret, time.Unix(sec, 0)); got != want {
			t.Errorf("T=%d: %s, want %s", sec, got, want)
		}
	}
}

func TestTOTPGuard(t *testing.T) {
	secret := newTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	var g totpGuard
	if g.check(secret, "12345", now) || g.check(secret, "abcdef", now) {
		t.Fatal("malformed code accepted")
	}
	prev := totpCode(secret, now.Add(-totpStep))
	if !g.check(secret, prev, now) {
		t.Fatal("code of the previous step (clock drift) refused")
	}
	cur := totpCode(secret, now)
	if !g.check(secret, " "+cur[:3]+" "+cur[3:]+" ", now) {
		t.Fatal("current code with spaces refused")
	}
	if g.check(secret, cur, now) || g.check(secret, prev, now) {
		t.Fatal("a used code accepted again")
	}
	if g.check(secret, totpCode(secret, now.Add(-3*totpStep)), now.Add(time.Hour)) {
		t.Fatal("old code accepted")
	}
}

// Setting up one-time codes on the local page and logging in with them.
func TestRemoteLoginTOTP(t *testing.T) {
	loginDelay = time.Millisecond
	defer func() { loginDelay = time.Second }()
	s, _ := newTestServer(t)
	h := s.agentPortHandler("internet")
	local := s.uiHandler()
	jar := map[string]string{}
	for _, a := range []map[string]string{{"action": "password", "password": "звёздное небо"}, {"action": "internet-on"}} {
		if rec := call(t, local, jar, "POST", "/ui/api/remote", a); rec.Code != 200 {
			t.Fatalf("%v: %d %s", a, rec.Code, rec.Body)
		}
	}
	info := func() (on bool) {
		rec := call(t, h, jar, "GET", "/login/info", nil)
		var v map[string]bool
		json.Unmarshal(rec.Body.Bytes(), &v)
		return v["totp"]
	}
	if info() {
		t.Fatal("codes on before setup")
	}

	rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "totp-begin"})
	var ri remoteInfo
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ri) != nil || ri.TOTPSetup == nil ||
		!strings.HasPrefix(ri.TOTPSetup.QR, "data:image/png;base64,") || !strings.HasPrefix(ri.TOTPSetup.URI, "otpauth://totp/") {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	secret := strings.ReplaceAll(ri.TOTPSetup.Secret, " ", "")
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "totp-confirm", "code": "000000"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong code confirmed: %d", rec.Code)
	}
	now := time.Now()
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "totp-confirm", "code": totpCode(secret, now)}); rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	if cfg, _ := config.LoadReceiver(s.cfg.Path); cfg.RemoteTOTP != secret {
		t.Fatal("secret not saved in receiver.yaml")
	}
	if !info() {
		t.Fatal("login page does not know about the codes")
	}

	login := func(pw, code string) int {
		return call(t, h, jar, "POST", "/login", map[string]string{"password": pw, "code": code}).Code
	}
	next := totpCode(secret, now.Add(totpStep))
	if c := login("звёздное небо", ""); c != http.StatusUnauthorized {
		t.Fatalf("password alone: %d", c)
	}
	if c := login("не тот", next); c != http.StatusUnauthorized {
		t.Fatalf("wrong password with a good code: %d", c)
	}
	if c := login("звёздное небо", totpCode(secret, now)); c != http.StatusUnauthorized {
		t.Fatalf("the code used for the setup logs in: %d", c)
	}
	if c := login("звёздное небо", next); c != http.StatusNoContent {
		t.Fatalf("password and code: %d", c)
	}
	delete(jar, sessionCookie)
	if c := login("звёздное небо", next); c != http.StatusUnauthorized {
		t.Fatalf("code used twice: %d", c)
	}
	// only the computer with the receiver turns them off
	if rec := call(t, h, jar, "POST", "/ui/api/remote", map[string]string{"action": "totp-off"}); rec.Code == 200 {
		t.Fatal("codes turned off remotely")
	}
	if rec := call(t, local, jar, "POST", "/ui/api/remote", map[string]string{"action": "totp-off"}); rec.Code != 200 {
		t.Fatalf("off: %d", rec.Code)
	}
	if c := login("звёздное небо", ""); c != http.StatusNoContent {
		t.Fatalf("password alone after off: %d", c)
	}
}
