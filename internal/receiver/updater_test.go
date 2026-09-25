package receiver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/agent"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// TestAgentUpdate: the receiver finds a new release on (a fake) GitHub,
// downloads the agent program and the agent replaces itself with it.
func TestAgentUpdate(t *testing.T) {
	// the releases carry the Windows agent; here it is offered to this platform
	defer func(p string) { agentPlatform = p }(agentPlatform)
	agentPlatform = runtime.GOOS + "/" + runtime.GOARCH
	old := protocol.Version
	protocol.Version = "1.1.0"
	defer func() { protocol.Version = old }()

	newExe := []byte("MZ asiair-agent 1.2.0")
	sum := sha256.Sum256(newExe)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v1.2.0","html_url":"https://example/rel","assets":[
				{"name":"asiair-agent.exe","size":%d,"digest":"sha256:%s","url":"%s/a","browser_download_url":"%s/a"}]}`,
				len(newExe), hex.EncodeToString(sum[:]), gh.URL, gh.URL)
		case "/a":
			w.Write(newExe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	tmp := t.TempDir()
	rcfg := &config.Receiver{Listen: freePort(t), UIListen: freePort(t), Token: "secret", DisableTLS: true,
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"), AutoUpdate: true, UpdateRepo: "o/r"}
	srv, err := New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	srv.UpdateAPI = gh.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	srv.updater.trigger(false)
	deadline := time.Now().Add(5 * time.Second)
	for srv.updater.info().Agent != "1.2.0" {
		if time.Now().After(deadline) {
			t.Fatalf("agent not downloaded: %+v", srv.updater.info())
		}
		time.Sleep(50 * time.Millisecond)
	}

	asiair := filepath.Join(tmp, "EMMC Images")
	os.MkdirAll(asiair, 0o755)
	exe := filepath.Join(tmp, "asiair-agent.exe")
	os.WriteFile(exe, []byte("MZ old agent"), 0o755)
	a, err := agent.New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "http://" + rcfg.Listen, Token: "secret", MaxRate: "0",
		ScanInterval: config.Duration(time.Minute), PollInterval: config.Duration(100 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"), AutoUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 1)
	a.Exe = exe
	a.Restart = func() error { restarted <- struct{}{}; return nil }
	go a.Run(ctx)

	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatalf("agent did not update: %+v", a.Status())
	}
	if b, _ := os.ReadFile(exe); string(b) != string(newExe) {
		t.Fatalf("program not replaced: %q", b)
	}
	if b, _ := os.ReadFile(update.Backup(exe)); string(b) != "MZ old agent" {
		t.Fatalf("old copy: %q", b)
	}
}
