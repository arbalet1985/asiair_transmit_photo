package receiver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// One-time codes (TOTP, RFC 6238) as authenticator apps make them: Google
// Authenticator, Aegis, 2FAS, Microsoft Authenticator, 1Password…
// 6 digits, a new code every 30 seconds, HMAC-SHA1.
const (
	totpStep   = 30 * time.Second
	totpDigits = 6
	totpSkew   = 1 // codes of the neighbouring steps are accepted too (clock drift)
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a random 160-bit secret in base32.
func newTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	return b32.DecodeString(strings.ToUpper(strings.ReplaceAll(strings.TrimRight(secret, "="), " ", "")))
}

func totpAt(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000)
}

// totpCode returns the code for time t (used by the tests).
func totpCode(secret string, t time.Time) string {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return ""
	}
	return totpAt(key, uint64(t.Unix())/uint64(totpStep/time.Second))
}

// totpGuard remembers the last step used, so that a code is accepted once.
type totpGuard struct {
	mu   sync.Mutex
	last uint64
}

// check verifies code for time now; it returns false for a code already used.
func (g *totpGuard) check(secret, code string, now time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return false
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil || len(key) == 0 {
		return false
	}
	cur := uint64(now.Unix()) / uint64(totpStep/time.Second)
	g.mu.Lock()
	defer g.mu.Unlock()
	for d := -totpSkew; d <= totpSkew; d++ {
		c := cur + uint64(d)
		if subtle.ConstantTimeCompare([]byte(totpAt(key, c)), []byte(code)) == 1 {
			if c <= g.last {
				return false // replayed
			}
			g.last = c
			return true
		}
	}
	return false
}

// totpURI is what the authenticator app reads from the QR code.
func totpURI(secret, account string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", "ASIAIR receiver")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(int(totpStep/time.Second)))
	return "otpauth://totp/" + url.PathEscape("ASIAIR receiver:"+account) + "?" + v.Encode()
}

// totpQR returns the QR code of uri as a data: URL of a PNG image.
func totpQR(uri string) (string, error) {
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// groupSecret shows the secret in groups of four for typing by hand.
func groupSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// totpSetup is shown on the page while the authenticator app is set up.
type totpSetup struct {
	Secret string `json:"secret"` // grouped for typing
	URI    string `json:"uri"`
	QR     string `json:"qr"` // data: URL
}

// totpAccount names this receiver in the authenticator app.
func (s *Server) totpAccount() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "receiver"
}
