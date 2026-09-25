package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

// TestCommandsArriveAtOnce: with a long poll interval, commands from the
// receiver page still reach the agent within a moment (the sync request is
// held open by the receiver), and a pause in the agent window reaches the
// page at once.
func TestCommandsArriveAtOnce(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	f1 := filepath.Join(asiair, "Autorun", "Light", "a.fit")
	writeRandom(t, f1, 1000)

	uiAddr := freeAddr(t)
	rcfg := &config.Receiver{Listen: freeAddr(t), UIListen: uiAddr, Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data")}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })
	startTransfer(t, uiAddr)

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: srv.Fingerprint(),
		ScanInterval:        config.Duration(200 * time.Millisecond),
		PollInterval:        config.Duration(30 * time.Second), // long: commands must not wait for it
		StateFile:           filepath.Join(tmp, "agent-state.json"),
		Extensions:          []string{".fit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })
	ui := func(path string, body any) {
		b, _ := json.Marshal(body)
		r, err := http.Post("http://"+uiAddr+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	state := func() (st struct {
		Agent struct {
			LocalPause bool `json:"local_pause"`
		} `json:"agent"`
	}) {
		r, err := http.Get("http://" + uiAddr + "/ui/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		json.NewDecoder(r.Body).Decode(&st)
		return st
	}
	// the first exchanges happen; afterwards the agent waits in a held request
	waitFor(t, "file received", 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "a.fit"))
		return err == nil
	})
	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	ui("/ui/api/delete", map[string]any{"paths": []string{"EMMC Images/Autorun/Light/a.fit"}})
	waitFor(t, "delete on ASIAIR", 3*time.Second, func() bool {
		_, err := os.Stat(f1)
		return os.IsNotExist(err)
	})
	t.Logf("delete took %v", time.Since(start))

	ui("/ui/api/rate", map[string]any{"rate": "3MB/s"})
	waitFor(t, "rate change", 3*time.Second, func() bool { return a.lim.Rate() == 3<<20 })

	ui("/ui/api/pause", map[string]any{"paused": true})
	waitFor(t, "pause from the page", 3*time.Second, func() bool { return a.Paused() })

	a.SetPaused(true) // pause in the agent window: the page shows it at once
	waitFor(t, "local pause on the page", 3*time.Second, func() bool { return state().Agent.LocalPause })
}

// TestOldReceiverWithoutLongPoll: a receiver that answers without Rev is
// polled every poll_interval as before (no busy loop).
func TestOldReceiverWithoutLongPoll(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"commands":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	ts := newTestHTTP(t, mux)
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "EMMC Images"), 0o755)
	a, err := New(&config.Agent{
		ASIAIR: "dir:" + filepath.Join(tmp, "EMMC Images"), ReceiverURL: ts, Token: "x",
		ScanInterval: config.Duration(time.Hour), PollInterval: config.Duration(300 * time.Millisecond),
		StateFile: filepath.Join(tmp, "s.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	a.Run(ctx)
	if n := calls.Load(); n < 2 || n > 8 {
		t.Fatalf("%d sync calls in 1.5 s with poll_interval 300ms", n)
	}
}

func newTestHTTP(t *testing.T, h http.Handler) string {
	t.Helper()
	addr := freeAddr(t)
	srv := &http.Server{Addr: addr, Handler: h}
	go srv.ListenAndServe()
	t.Cleanup(func() { srv.Close() })
	waitFor(t, "test server", 2*time.Second, func() bool {
		r, err := http.Get("http://" + addr + "/")
		if err == nil {
			r.Body.Close()
		}
		return err == nil
	})
	return "http://" + strings.TrimPrefix(addr, "http://")
}
