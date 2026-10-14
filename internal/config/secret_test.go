package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI")
	}
	p := filepath.Join(t.TempDir(), "receiver.yaml")
	os.WriteFile(p, []byte("token: tok\nui_password: plain\nspacetrack_password: ''\n"), 0o644)
	if err := SealReceiverFile(p); err != nil {
		t.Fatal(err)
	}
	if err := UpdateReceiverFile(p, map[string]string{"opensky_secret": "s3cret", "opensky_id": "me-api-client"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "plain") || strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), "me-api-client") {
		t.Fatalf("not sealed:\n%s", b)
	}
	c, err := LoadReceiver(p)
	if err != nil || c.UIPassword != "plain" || c.OpenSkySecret != "s3cret" || c.Token != "tok" || c.SpaceTrackPassword != "" {
		t.Fatalf("%v %+v", err, c)
	}
	// from another computer: empty, not an error
	os.WriteFile(p, []byte("token: tok\nui_password: 'dpapi:AAAA'\n"), 0o644)
	if c, err := LoadReceiver(p); err != nil || c.UIPassword != "" {
		t.Fatalf("%v %+v", err, c)
	}
}
