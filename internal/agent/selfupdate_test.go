//go:build !windows

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// buildAgent builds the agent program with the given version.
func buildAgent(t *testing.T, version, out string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cmd := exec.Command("go", "build", "-o", out,
		"-ldflags", "-X github.com/arbalet1985/asiair_transmit_photo/internal/protocol.Version="+version, "./cmd/asiair-agent")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
}

// fakeReceiver offers newVersion (payload) to agents of other versions and
// records what they report.
type fakeReceiver struct {
	mu        sync.Mutex
	versions  []string // versions seen in syncs, in order
	updates   []string // status.update texts
	downloads int
}

func (f *fakeReceiver) last() (version, upd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.versions) == 0 {
		return "", ""
	}
	return f.versions[len(f.versions)-1], f.updates[len(f.updates)-1]
}

func startFakeReceiver(t *testing.T, newVersion string, payload []byte) (*fakeReceiver, string) {
	f := &fakeReceiver{}
	sum := sha256.Sum256(payload)
	offer := &protocol.UpdateOffer{Version: newVersion, Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SyncRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.versions = append(f.versions, req.Status.Version)
		f.updates = append(f.updates, req.Status.Update)
		f.mu.Unlock()
		resp := protocol.SyncResponse{Commands: []protocol.Command{}}
		if req.Status.Version != newVersion {
			resp.Update = offer
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/update/agent", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.downloads++
		f.mu.Unlock()
		w.Write(payload)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// runOldAgent starts agent 1.0.0 from dir with a config for url.
func runOldAgent(t *testing.T, dir, url string) string {
	exe := filepath.Join(dir, "asiair-agent")
	buildAgent(t, "1.0.0", exe)
	os.MkdirAll(filepath.Join(dir, "EMMC Images"), 0o755)
	cfg := filepath.Join(dir, "agent.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("asiair: 'dir:%s'\nreceiver_url: '%s'\ntoken: t\npoll_interval: 300ms\nscan_interval: 1h\nlog_file: agent.log\n",
		filepath.Join(dir, "EMMC Images"), url)), 0o644)
	cmd := exec.Command(exe, "-config", cfg, "run")
	cmd.Env = append(os.Environ(), "ASIAIR_UPDATE_TIMEOUT=4s")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	t.Cleanup(func() {
		// stop whatever runs from this folder now (the restarted copies too)
		exec.Command("pkill", "-f", exe).Run()
		if t.Failed() {
			for _, l := range []string{"agent.log", "asiair-agent-update.log"} {
				b, _ := os.ReadFile(filepath.Join(dir, l))
				t.Logf("--- %s:\n%s", l, b)
			}
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				t.Logf("file %s", e.Name())
			}
		}
	})
	return exe
}

func waitCond(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

// A new version that does not start is rolled back; the old version comes
// back, reports it and does not take the same version again.
func TestSelfUpdateRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent")
	}
	broken := []byte("#!/bin/sh\nexit 1\n")
	f, url := startFakeReceiver(t, "1.1.0", broken)
	dir := t.TempDir()
	exe := runOldAgent(t, dir, url)

	waitCond(t, "old version back and reporting the rollback", 40*time.Second, func() bool {
		v, u := f.last()
		return v == "1.0.0" && strings.Contains(u, "возвращена версия 1.0.0")
	})
	if b, _ := os.ReadFile(exe); strings.HasPrefix(string(b), "#!") {
		t.Fatal("broken version still in place")
	}
	if rb := update.ReadRollback(exe); rb == nil || rb.Version != "1.1.0" {
		t.Fatalf("rollback %+v", rb)
	}
	// the same version is not downloaded again
	n := f.downloads
	time.Sleep(2 * time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downloads != n || n != 1 {
		t.Fatalf("downloads: %d, then %d", n, f.downloads)
	}
}

// A working new version stays, and the old copy is removed.
func TestSelfUpdateKeepsWorkingVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent")
	}
	tmp := t.TempDir()
	newExe := filepath.Join(tmp, "new")
	buildAgent(t, "1.1.0", newExe)
	payload, _ := os.ReadFile(newExe)
	f, url := startFakeReceiver(t, "1.1.0", payload)
	dir := t.TempDir()
	exe := runOldAgent(t, dir, url)

	waitCond(t, "new version reporting", 40*time.Second, func() bool {
		v, _ := f.last()
		return v == "1.1.0"
	})
	// well past the check deadline: no rollback, old copy gone
	time.Sleep(6 * time.Second)
	if v, _ := f.last(); v != "1.1.0" {
		t.Fatalf("rolled back to %s", v)
	}
	if update.ReadRollback(exe) != nil || update.ReadPending(exe) != nil {
		t.Fatal("rollback or pending left")
	}
	waitCond(t, "old copy removed", 20*time.Second, func() bool {
		_, err := os.Stat(update.Backup(exe))
		return os.IsNotExist(err)
	})
}
