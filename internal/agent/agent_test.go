package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func writeRandom(t *testing.T, name string, size int) []byte {
	b := make([]byte, size)
	rand.Read(b)
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEndToEnd runs a receiver and an agent reading a folder that looks like
// the ASIAIR "EMMC Images" share.
func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	light := writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0001.fit"), 300_000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0002.fit"), 100_000)
	writeRandom(t, filepath.Join(asiair, "Thumbnail", "skip.jpg"), 1000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "log.txt"), 10)

	uiAddr := freeAddr(t)
	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: uiAddr, Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	acfg := &config.Agent{
		ASIAIR:              "dir:" + asiair,
		ReceiverURL:         "https://" + rcfg.Listen,
		Token:               "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()),
		MaxRate:             "400KB/s",
		ScanInterval:        config.Duration(300 * time.Millisecond),
		PollInterval:        config.Duration(200 * time.Millisecond),
		StateFile:           filepath.Join(tmp, "agent-state.json"),
		Extensions:          []string{".fit", ".jpg"},
		Exclude:             []string{"Thumbnail"},
	}
	a, err := New(acfg)
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)

	dest := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "M31")
	waitFor(t, "files to arrive", 10*time.Second, func() bool {
		_, e1 := os.Stat(filepath.Join(dest, "Light_M31_0001.fit"))
		_, e2 := os.Stat(filepath.Join(dest, "Light_M31_0002.fit"))
		return e1 == nil && e2 == nil
	})
	got, _ := os.ReadFile(filepath.Join(dest, "Light_M31_0001.fit"))
	if !bytes.Equal(got, light) {
		t.Fatal("received file differs")
	}
	if _, err := os.Stat(filepath.Join(rcfg.DestDir, "EMMC Images", "Thumbnail")); err == nil {
		t.Fatal("excluded folder was transferred")
	}
	if _, err := os.Stat(filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "log.txt")); err == nil {
		t.Fatal("filtered extension was transferred")
	}

	ui := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		resp, err := http.Post("http://"+uiAddr+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// delete on the ASIAIR from the web page, also locally
	p := "EMMC Images/Autorun/Light/M31/Light_M31_0002.fit"
	if r := ui("/ui/api/delete", map[string]any{"paths": []string{p}, "also_local": true}); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	waitFor(t, "delete on ASIAIR", 5*time.Second, func() bool {
		_, e1 := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0002.fit"))
		_, e2 := os.Stat(filepath.Join(dest, "Light_M31_0002.fit"))
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
	// the other file is untouched
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0001.fit")); err != nil {
		t.Fatal(err)
	}

	// path traversal is refused
	if r := ui("/ui/api/delete", map[string]any{"paths": []string{"EMMC Images/../../etc/passwd"}}); r.StatusCode != 400 {
		t.Fatalf("traversal accepted: %d", r.StatusCode)
	}

	// rate set on the web page reaches the agent
	ui("/ui/api/rate", map[string]any{"rate": "3MB/s"})
	waitFor(t, "rate change", 5*time.Second, func() bool { return a.lim.Rate() == 3<<20 })
	ui("/ui/api/rate", map[string]any{"rate": nil})
	waitFor(t, "rate reset", 5*time.Second, func() bool { return a.lim.Rate() == 400*1024 })

	// a new frame is picked up
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0003.fit"), 50_000)
	waitFor(t, "new file", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dest, "Light_M31_0003.fit"))
		return err == nil
	})

	// wrong token is rejected
	c, _ := newClient(acfg.ReceiverURL, "wrong", acfg.ReceiverFingerprint)
	if err := c.postJSON(context.Background(), "/api/sync", map[string]any{}, nil); err == nil {
		t.Fatal("wrong token accepted")
	}
	// wrong certificate is rejected
	c, _ = newClient(acfg.ReceiverURL, "secret", strings.Repeat("00", 32))
	if err := c.postJSON(context.Background(), "/api/sync", map[string]any{}, nil); err == nil ||
		!strings.Contains(err.Error(), "отпечаток") {
		t.Fatalf("wrong fingerprint accepted: %v", err)
	}

	// helpers used by the settings form
	fp, err := FetchFingerprint(context.Background(), acfg.ReceiverURL)
	if err != nil || fp != srv.Fingerprint() {
		t.Fatalf("FetchFingerprint = %q, %v; want %q", fp, err, srv.Fingerprint())
	}
	if err := CheckReceiver(context.Background(), acfg.ReceiverURL, "secret", fp); err != nil {
		t.Fatalf("CheckReceiver: %v", err)
	}
	if err := CheckReceiver(context.Background(), acfg.ReceiverURL, "bad", fp); err == nil || !strings.Contains(err.Error(), "токен") {
		t.Fatalf("CheckReceiver with bad token: %v", err)
	}
	if res, err := a.src.Probe(context.Background()); err != nil || len(res.Shares) != 1 || res.Shares[0].Entries != 2 {
		t.Fatalf("Probe: %+v %v", res, err)
	}
}
