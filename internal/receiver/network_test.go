package receiver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

type fakeFirewall struct{ allowed, asked atomic.Bool }

func (f *fakeFirewall) Supported() bool { return true }
func (f *fakeFirewall) Allowed() bool   { return f.allowed.Load() }
func (f *fakeFirewall) Allow() error    { f.asked.Store(true); f.allowed.Store(true); return nil }

func freeNetPort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// The agent port opens and closes from the page, and the choice is saved.
func TestInternetToggleAndFirewall(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "receiver.yaml")
	listen, ui := freeNetPort(t), freeNetPort(t)
	os.WriteFile(cfgPath, []byte("listen: '"+listen+"'\nui_listen: '"+ui+"'\ntoken: t\ninternet: false\ndest_dir: d\ndata_dir: data\n"), 0o644)
	cfg, err := config.LoadReceiver(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fw := &fakeFirewall{}
	s.Firewall = fw
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	open := func() bool {
		c, err := net.DialTimeout("tcp", listen, time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	post := func(action string) netInfo {
		t.Helper()
		var r *http.Response
		for i := 0; i < 50; i++ {
			if r, err = http.Post("http://"+ui+"/ui/api/network", "application/json", strings.NewReader(`{"action":"`+action+`"}`)); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var n netInfo
		json.NewDecoder(r.Body).Decode(&n)
		return n
	}
	time.Sleep(200 * time.Millisecond)
	if open() {
		t.Fatal("port open although internet: false")
	}
	if n := post("firewall-allow"); !fw.asked.Load() || !n.FWSupport {
		t.Fatalf("firewall not asked: %+v", n)
	}
	if n := post("internet-on"); !n.Internet || !n.Listening || !open() {
		t.Fatalf("internet on: %+v, open %v", n, open())
	}
	if c, _ := config.LoadReceiver(cfgPath); !c.InternetOn() {
		t.Fatal("setting not saved")
	}
	if n := post("internet-off"); n.Internet || n.Listening {
		t.Fatalf("internet off: %+v", n)
	}
	time.Sleep(100 * time.Millisecond)
	if open() {
		t.Fatal("port still open")
	}
	if c, _ := config.LoadReceiver(cfgPath); c.InternetOn() {
		t.Fatal("setting not saved")
	}
}

// After a restart a port still held by the previous copy is tried again.
func TestListenWaitsForPortAfterRestart(t *testing.T) {
	oldRetry, oldStart := listenRetry, processStart
	listenRetry = 20 * time.Millisecond
	t.Cleanup(func() { listenRetry, processStart = oldRetry, oldStart })
	t.Setenv(update.RestartEnv, "")
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.Addr().String()

	// a normal start reports the busy port at once
	if ln, err := listen(addr); err == nil {
		ln.Close()
		t.Fatal("busy port opened")
	}

	t.Setenv(update.RestartEnv, "1")
	processStart = time.Now()
	go func() {
		time.Sleep(200 * time.Millisecond)
		busy.Close() // the previous copy exits
	}()
	ln, err := listen(addr)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	ln.Close()
}
