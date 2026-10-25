package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// TestFailover: the receiver not answering through Tailscale while it
// answers through the internet, the agent switches there by itself and
// tells why at the next run; with no answer either way nothing changes.
func TestFailover(t *testing.T) {
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"commands":[]}`))
	}))
	defer recv.Close()
	tmp := t.TempDir()
	cfg := writeAgentConfig(t, tmp, `asiair: 'dir:`+tmp+`'
receiver_url: 'https://127.0.0.1:1'
receiver_url_internet: 'https://127.0.0.1:1'
connection: internet
token: 'x'
state_file: state.json
`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// pretend the agent runs through Tailscale (no node in the test) and
	// the receiver has not answered
	cfg.Connection = config.ConnTailscale
	a.syncErr = "нет связи"
	restarts := 0
	a.Restart = func() error { restarts++; return nil }
	switchCheckWait = 3 * time.Second
	defer func() { switchCheckWait = 30 * time.Second }()
	start := time.Now().Add(-time.Hour)
	var since time.Time
	why, to := a.failoverNeeded(start, &since)
	if why == "" || to != protocol.ConnInternet {
		t.Fatalf("no failover: %q %q", why, to)
	}

	// the internet address does not answer either: nothing changes
	if err := a.failover(context.Background(), to, why); err == nil || restarts != 0 || exists(a.switchedNote()) {
		t.Fatalf("switched to a way that does not work: %v", err)
	}

	// it answers: switched, the reason kept for the next run
	a.cfg.ReceiverURLInternet = recv.URL
	if err := a.failover(context.Background(), to, "Tailscale на агенте требует входа (тест)"); err != nil || restarts != 1 {
		t.Fatalf("failover: %v, restarts %d", err, restarts)
	}
	c, err := config.LoadAgent(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Connection != config.ConnInternet || c.ReceiverURL != recv.URL || c.ConnectionFallback != config.ConnTailscale {
		t.Fatalf("saved: %+v", c)
	}
	if !exists(a.switchedNote()) || !a.Status().TSNeedLogin {
		t.Fatal("the reason or the login asked for not kept")
	}
	a.failoverNoted()
	if exists(a.switchedNote()) {
		t.Fatal("told twice")
	}
	found := false
	for _, e := range a.pendingEvents() {
		found = found || strings.Contains(e.Text, "новый вход Tailscale с приёмника")
	}
	if !found {
		t.Fatal("the reason not told to the receiver")
	}

	// a switch not yet confirmed: no failover over it
	a.cfg.ConnectionFallback = config.ConnTailscale
	if why, _ := a.failoverNeeded(start, &since); why != "" {
		t.Fatalf("failover over an unconfirmed switch: %q", why)
	}
	os.Remove(tsNeedLogin(a.cfg.TailscaleDir))
}
