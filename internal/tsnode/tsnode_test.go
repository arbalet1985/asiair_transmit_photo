package tsnode

import (
	"context"
	"io"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"

	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode/tsnodetest"
)

func waitRunning(t *testing.T, n *Node) Status {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if st := n.Status(); st.State == StateRunning {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("node not running: %+v", n.Status())
	return Status{}
}

func TestNodesTalkByName(t *testing.T) {
	ctrl := tsnodetest.Control(t, false).HTTPTestServer.URL
	dir := t.TempDir()
	start := func(name string) *Node {
		n, err := Start(Config{Dir: filepath.Join(dir, name), Hostname: name, ControlURL: ctrl, Logf: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.Close() })
		return n
	}
	recv, agent := start("asiair-receiver"), start("asiair-agent")
	rs := waitRunning(t, recv)
	waitRunning(t, agent)
	if rs.Name != "asiair-receiver" || rs.IP == "" {
		t.Fatalf("status %+v", rs)
	}

	ln, err := recv.Listen(":8443")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("hello"))
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, addr := range []string{"asiair-receiver:8443", rs.IP + ":8443"} {
		c, err := agent.Dial(ctx, "tcp", addr)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		b, _ := io.ReadAll(c)
		c.Close()
		if string(b) != "hello" {
			t.Fatalf("dial %s: got %q", addr, b)
		}
	}
}

func TestInteractiveLogin(t *testing.T) {
	ctrl := tsnodetest.Control(t, true)
	n, err := Start(Config{Dir: t.TempDir(), Hostname: "asiair-agent", ControlURL: ctrl.HTTPTestServer.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	var st Status
	for i := 0; i < 300; i++ {
		if st = n.Status(); st.State == StateLogin && st.AuthURL != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.State != StateLogin || st.AuthURL == "" {
		t.Fatalf("expected a login link, got %+v", st)
	}
	if !ctrl.CompleteAuth(st.AuthURL) {
		t.Fatal("CompleteAuth failed")
	}
	waitRunning(t, n)
}

func TestPeerPath(t *testing.T) {
	s := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
		key.NewNode().Public(): {DNSName: "asiair-receiver.tail1.ts.net.", HostName: "asiair-receiver", Relay: "fra"},
		key.NewNode().Public(): {DNSName: "other.tail1.ts.net.", CurAddr: "1.2.3.4:41641", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}},
	}}
	if p := peerPath(s, "asiair-receiver"); !strings.Contains(p, "ретранслятор Tailscale «fra»") {
		t.Fatalf("relay: %q", p)
	}
	if p := peerPath(s, "100.64.0.9"); p != "напрямую (1.2.3.4:41641)" {
		t.Fatalf("direct: %q", p)
	}
	if p := peerPath(s, "nobody"); p != "" {
		t.Fatalf("unknown: %q", p)
	}
}
