package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

func TestAgentLogs(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "a.fit"), 1000)
	logs := filepath.Join(tmp, "agentdir")
	os.MkdirAll(logs, 0o755)
	write := func(name, text string, age time.Duration) {
		p := filepath.Join(logs, name)
		os.WriteFile(p, []byte(text), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
	}
	write("asiair-agent.log", "today\n", 0)
	write("asiair-agent-2026-01-01.log", "yesterday\n", 30*time.Hour)
	write("asiair-agent-2025-01-01.log.gz", "old", 10*24*time.Hour)
	write("agent.yaml", "token: secret", 0)
	exe := filepath.Join(logs, "asiair-agent.exe")
	write("asiair-agent-update.log", "watchdog\n", time.Hour)

	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
		LogFile: filepath.Join(tmp, "asiair-receiver.log"),
	}
	os.WriteFile(rcfg.LogFile, []byte("receiver\n"), 0o644)
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })
	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()),
		ScanInterval:        config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"), LogFile: filepath.Join(logs, "asiair-agent.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Exe = exe
	background(t, func() { a.Run(ctx) })

	var resp *http.Response
	waitFor(t, "receiver page", 5*time.Second, func() bool {
		resp, err = http.Post("http://"+rcfg.UIListen+"/ui/api/agentlogs", "application/json", strings.NewReader(`{"days":3}`))
		return err == nil
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ask: %d", resp.StatusCode)
	}
	var name string
	waitFor(t, "logs sent", 10*time.Second, func() bool {
		st := a.Status().Logs
		if st != nil && !st.Running {
			if st.Error != "" {
				t.Fatalf("logs: %s", st.Error)
			}
			name = st.Name
			return true
		}
		return false
	})
	// the page lists it and downloads it with the receiver logs
	var ui struct {
		AgentLogs []struct{ Name string } `json:"agent_logs"`
	}
	r2, err := http.Get("http://" + rcfg.UIListen + "/ui/api/state")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(r2.Body).Decode(&ui)
	r2.Body.Close()
	if len(ui.AgentLogs) != 1 || ui.AgentLogs[0].Name != name {
		t.Fatalf("listed: %+v, sent %q", ui.AgentLogs, name)
	}
	r3, err := http.Get("http://" + rcfg.UIListen + "/ui/logs.zip?name=" + name)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range zr.File {
		got = append(got, f.Name)
	}
	sort.Strings(got)
	want := []string{"agent/asiair-agent-2026-01-01.log", "agent/asiair-agent-update.log", "agent/asiair-agent.log", "receiver/asiair-receiver.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("zip: %v, want %v (no old days, no settings)", got, want)
	}
	// a bad name is refused
	r4, _ := http.Get("http://" + rcfg.UIListen + "/ui/logs.zip?name=../receiver.yaml")
	r4.Body.Close()
	if r4.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad name: %d", r4.StatusCode)
	}
}
