package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode/tsnodetest"
)

// TestTSLogin: the agent connected through Tailscale takes a new login from
// the receiver: checked aside, saved, the folders swapped at the next run
// and the login confirmed once the receiver answers; a login through which
// the receiver does not answer changes nothing.
func TestTSLogin(t *testing.T) {
	ctrl := tsnodetest.Control(t, false).HTTPTestServer.URL
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	os.MkdirAll(asiair, 0o755)
	listen := freeAddr(t)
	rcfg := &config.Receiver{
		Listen: listen, UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
		TailscaleOn: true,
		Tailscale:   config.Tailscale{TailscaleHostname: "asiair-receiver", TailscaleControlURL: ctrl},
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })
	_, port, _ := net.SplitHostPort(listen)
	url := "https://asiair-receiver:" + port
	cfg := writeAgentConfig(t, tmp, `asiair: 'dir:`+asiair+`'
receiver_url: '`+url+`'
receiver_url_tailscale: '`+url+`'
connection: tailscale
tailscale_hostname: asiair-agent
tailscale_control_url: '`+ctrl+`'
token: secret
state_file: state.json
poll_interval: 200ms
scan_interval: 300ms
`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restarts := make(chan struct{}, 4)
	a.Restart = func() error { restarts <- struct{}{}; return nil }
	waitFor(t, "the agent in the tailnet", 90*time.Second, func() bool {
		st := a.Tailscale()
		return st != nil && st.State == tsnode.StateRunning
	})
	switchCheckWait = 20 * time.Second
	defer func() { switchCheckWait = 30 * time.Second }()

	// the receiver does not answer through the new login: nothing changes
	bad := &protocol.TSLogin{ID: 1, AuthKey: "tskey-auth-test", ControlURL: ctrl, Hostname: "asiair-agent2"}
	a.cfg.ReceiverURLTailscale = "https://asiair-receiver:1"
	if err := a.tsLogin(ctx, bad); err == nil {
		t.Fatal("a login through which the receiver does not answer was taken")
	}
	a.cfg.ReceiverURLTailscale = url
	if c, _ := config.LoadAgent(cfg.Path); c.TailscaleHostname != "asiair-agent" || exists(tsSwap(cfg)) || exists(tsNew(cfg)) {
		t.Fatalf("a failed login left traces: %+v", c)
	}

	// a login that works
	l := &protocol.TSLogin{ID: 2, AuthKey: "tskey-auth-test", ControlURL: ctrl, Hostname: "asiair-agent2", ReceiverURLs: []string{url}}
	if err := a.tsLogin(ctx, l); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restarts:
	default:
		t.Fatal("not restarted")
	}
	if !exists(tsNew(cfg)) || !exists(tsSwap(cfg)) {
		t.Fatal("the new login not kept for the next run")
	}
	a.Close()

	// the next run takes it
	cfg2, err := config.LoadAgent(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.TailscaleHostname != "asiair-agent2" || cfg2.TailscaleAuthKey != "tskey-auth-test" {
		t.Fatalf("not saved: %+v", cfg2)
	}
	a2, err := New(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if exists(tsNew(cfg2)) || !exists(tsOld(cfg2)) || !exists(tsConfirm(cfg2)) || !a2.Status().TSLoginBack {
		t.Fatal("the folders not swapped")
	}
	a2.Restart = func() error { return nil }
	actx, acancel := context.WithCancel(ctx)
	defer acancel()
	background(t, func() { a2.Run(actx) })
	waitFor(t, "the new login confirmed", 90*time.Second, func() bool {
		return !exists(tsConfirm(cfg2)) && !exists(tsOld(cfg2)) && !a2.Status().TSLoginBack
	})
	if st := a2.Tailscale(); st == nil || st.Name != "asiair-agent2" {
		t.Fatalf("the agent runs with %+v", st)
	}
}

// TestTSGoBack: with no answer through the new login the old settings are
// written back and the old folder comes back at the next run.
func TestTSGoBack(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeAgentConfig(t, tmp, `asiair: 'dir:`+tmp+`'
receiver_url: 'https://new-name:8443'
receiver_url_tailscale: 'https://new-name:8443'
connection: tailscale
tailscale_hostname: new-name
tailscale_authkey: tskey-new
token: x
state_file: state.json
`)
	os.MkdirAll(cfg.TailscaleDir, 0o755)
	os.WriteFile(filepath.Join(cfg.TailscaleDir, "which"), []byte("new"), 0o644)
	os.MkdirAll(tsOld(cfg), 0o755)
	os.WriteFile(filepath.Join(tsOld(cfg), "which"), []byte("old"), 0o644)
	os.WriteFile(tsConfirm(cfg), []byte(`{"auth_key":"tskey-old","hostname":"asiair-agent","url":"https://asiair-receiver:8443","receiver_url":"https://asiair-receiver:8443"}`), 0o600)
	// the agent itself is not started: its Tailscale would need a network
	a := &Agent{cfg: cfg}
	restarts := 0
	a.Restart = func() error { restarts++; return nil }
	a.tsGoBack()
	c, err := config.LoadAgent(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || c.TailscaleHostname != "asiair-agent" || c.TailscaleAuthKey != "tskey-old" || c.ReceiverURL != "https://asiair-receiver:8443" ||
		!exists(tsRestore(cfg)) || exists(tsConfirm(cfg)) {
		t.Fatalf("not gone back (restarts %d): %+v", restarts, c)
	}
	tsStartup(c)
	if b, _ := os.ReadFile(filepath.Join(c.TailscaleDir, "which")); string(b) != "old" || exists(tsOld(c)) || exists(tsRestore(c)) {
		t.Fatalf("the old folder not back: %q", b)
	}
}
