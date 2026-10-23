package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// TestSwitchConn: the agent tries the receiver addresses for the new
// connection, saves the one that answers with the old one to go back to,
// and restarts; with no address answering nothing changes.
func TestSwitchConn(t *testing.T) {
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"commands":[]}`))
	}))
	defer recv.Close()
	tmp := t.TempDir()
	cfg := writeAgentConfig(t, tmp, `asiair: 'dir:`+tmp+`'
receiver_url: 'https://asiair-receiver:8443'
connection: internet
token: 'x'
state_file: state.json
`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// pretend the agent runs through Tailscale (no node in the test)
	cfg.Connection = config.ConnTailscale
	restarts := 0
	a.Restart = func() error { restarts++; return nil }
	switchCheckWait = 3 * time.Second

	sw := &protocol.ConnSwitch{ID: 1, Connection: protocol.ConnInternet, URLs: []string{"https://127.0.0.1:1"}}
	if err := a.switchConn(context.Background(), sw); err == nil || restarts != 0 {
		t.Fatalf("switched to an address that does not answer: %v", err)
	}
	if c, _ := config.LoadAgent(cfg.Path); c.Connection != config.ConnInternet || c.ConnectionFallback != "" {
		t.Fatalf("settings changed by a failed switch: %+v", c)
	}

	sw.URLs = append(sw.URLs, recv.URL)
	if err := a.switchConn(context.Background(), sw); err != nil || restarts != 1 {
		t.Fatalf("switch: %v, restarts %d", err, restarts)
	}
	c, err := config.LoadAgent(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Connection != config.ConnInternet || c.ReceiverURL != recv.URL || c.ReceiverURLInternet != recv.URL ||
		c.ReceiverURLTailscale != "https://asiair-receiver:8443" || c.ConnectionFallback != config.ConnTailscale {
		t.Fatalf("saved: %+v", c)
	}
}

// TestSwitchBack: after a switch the agent goes back to the previous
// connection when the receiver does not answer the new way in time, and
// confirms the switch when it does.
func TestSwitchBack(t *testing.T) {
	tmp := t.TempDir()
	body := `asiair: 'dir:` + tmp + `'
receiver_url: 'https://new.example.ru:8443'
receiver_url_tailscale: 'https://asiair-receiver:8443'
connection: internet
connection_fallback: tailscale
token: 'x'
state_file: state.json
`
	cfg := writeAgentConfig(t, tmp, body)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status().SwitchBack != config.ConnTailscale {
		t.Fatal("switch back not reported")
	}
	restarts := 0
	a.Restart = func() error { restarts++; return nil }
	switchConfirm = 100 * time.Millisecond
	defer func() { switchConfirm = 10 * time.Minute }()
	a.connLoop(context.Background())
	c, err := config.LoadAgent(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || c.Connection != config.ConnTailscale || c.ReceiverURL != "https://asiair-receiver:8443" || c.ConnectionFallback != "" {
		t.Fatalf("not switched back (restarts %d): %+v", restarts, c)
	}

	// the receiver answers: the switch is confirmed, the address remembered
	cfg = writeAgentConfig(t, tmp, body)
	a, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Restart = func() error { restarts++; return nil }
	a.synced.Store(true)
	a.connLoop(context.Background())
	c, _ = config.LoadAgent(cfg.Path)
	if restarts != 1 || c.Connection != config.ConnInternet || c.ConnectionFallback != "" || c.ReceiverURLInternet != "https://new.example.ru:8443" {
		t.Fatalf("switch not confirmed: %+v", c)
	}
	if a.Status().SwitchBack != "" {
		t.Fatal("switch back still reported")
	}
}

// TestRestartAsked: the agent restarts once per request.
func TestRestartAsked(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeAgentConfig(t, tmp, `asiair: 'dir:`+tmp+`'
receiver_url: 'https://127.0.0.1:1'
token: 'x'
state_file: state.json
`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status().CanRestart {
		t.Fatal("restart offered without a way to restart")
	}
	done := make(chan struct{}, 2)
	a.Restart = func() error { done <- struct{}{}; return nil }
	if !a.Status().CanRestart {
		t.Fatal("restart not offered")
	}
	a.restartAsked(5)
	a.restartAsked(5)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("not restarted")
	}
	select {
	case <-done:
		t.Fatal("restarted twice")
	case <-time.After(4 * time.Second):
	}
}
