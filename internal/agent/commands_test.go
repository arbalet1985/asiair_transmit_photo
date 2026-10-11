package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

// TestManyDeletes: thousands of delete commands are carried out in
// batches while the agent keeps reporting to the receiver (it stays "on
// line"), the results arrive in batches, and the agent's events reach the
// receiver page.
func TestManyDeletes(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	const n = 1500
	var paths []string
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("Autorun/Light/M31/f%04d.jpg", i)
		os.MkdirAll(filepath.Dir(filepath.Join(asiair, rel)), 0o755)
		os.WriteFile(filepath.Join(asiair, rel), []byte("x"), 0o644)
		paths = append(paths, "EMMC Images/"+rel)
	}
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M42", "a.fit"), 1000)

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

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: srv.Fingerprint(),
		ScanInterval:        config.Duration(time.Hour),
		PollInterval:        config.Duration(time.Second),
		StateFile:           filepath.Join(tmp, "agent-state.json"),
		Extensions:          []string{".fit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })
	type state struct {
		Commands []struct {
			Status string `json:"status"`
		} `json:"commands"`
		AgentOnline bool `json:"agent_online"`
		Events      []struct {
			Text string `json:"text"`
		} `json:"agent_events"`
	}
	get := func() (st state) {
		r, err := http.Get("http://" + uiAddr + "/ui/api/state")
		if err != nil {
			return st // not up yet
		}
		defer r.Body.Close()
		json.NewDecoder(r.Body).Decode(&st)
		return st
	}
	waitFor(t, "agent on line", 10*time.Second, func() bool { return get().AgentOnline })

	b, _ := json.Marshal(map[string]any{"paths": paths, "dirs": []string{"EMMC Images/Autorun/Light/M42"}})
	r, err := http.Post("http://"+uiAddr+"/ui/api/delete", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	waitFor(t, "all deleted", 60*time.Second, func() bool {
		st := get()
		if len(st.Commands) != n+1 {
			return false
		}
		for _, c := range st.Commands {
			if c.Status != "done" {
				return false
			}
		}
		return true
	})
	for _, p := range paths[:10] {
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Fatalf("%s still there", p)
		}
	}
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M42")); !os.IsNotExist(err) {
		t.Fatal("folder still there")
	}
	waitFor(t, "events on the page", 5*time.Second, func() bool {
		for _, e := range get().Events {
			if bytes.Contains([]byte(e.Text), []byte("удаление на ASIAIR закончено")) {
				return true
			}
		}
		return false
	})
	if !get().AgentOnline {
		t.Fatal("agent off line")
	}
}

// TestDeletePause: while deleting is paused on the page the commands stay
// pending and the files stay on the ASIAIR; after the pause they go.
func TestDeletePause(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	const n = 300
	var paths []string
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("Autorun/Light/M31/f%04d.jpg", i)
		os.MkdirAll(filepath.Dir(filepath.Join(asiair, rel)), 0o755)
		os.WriteFile(filepath.Join(asiair, rel), []byte("x"), 0o644)
		paths = append(paths, "EMMC Images/"+rel)
	}
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
	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: srv.Fingerprint(),
		ScanInterval:        config.Duration(time.Hour),
		PollInterval:        config.Duration(time.Second),
		StateFile:           filepath.Join(tmp, "agent-state.json"),
		Extensions:          []string{".fit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })
	type state struct {
		Commands []struct {
			Status string `json:"status"`
		} `json:"commands"`
		AgentOnline  bool `json:"agent_online"`
		DeletePaused bool `json:"delete_paused"`
	}
	get := func() (st state) {
		r, err := http.Get("http://" + uiAddr + "/ui/api/state")
		if err != nil {
			return st
		}
		defer r.Body.Close()
		json.NewDecoder(r.Body).Decode(&st)
		return st
	}
	post := func(url string, v any) {
		b, _ := json.Marshal(v)
		r, err := http.Post("http://"+uiAddr+url, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	count := func(status string) (k int) {
		for _, c := range get().Commands {
			if c.Status == status {
				k++
			}
		}
		return
	}
	waitFor(t, "agent on line", 10*time.Second, func() bool { return get().AgentOnline })

	post("/ui/api/delpause", map[string]bool{"paused": true})
	if !get().DeletePaused {
		t.Fatal("pause not shown on the page")
	}
	post("/ui/api/delete", map[string]any{"paths": paths})
	time.Sleep(3 * time.Second) // a few syncs of the agent
	if k := count("pending"); k != n {
		t.Fatalf("paused: %d of %d pending", k, n)
	}
	if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(paths[0]))); err != nil {
		t.Fatal("deleted while paused")
	}

	post("/ui/api/delpause", map[string]bool{"paused": false})
	waitFor(t, "all deleted", 30*time.Second, func() bool { return count("done") == n })
}

func TestParseNetsh(t *testing.T) {
	out := "\r\nНа этой системе обнаружено интерфейсов: 1.\r\n\r\n    Имя                    : Беспроводная сеть\r\n" +
		"    Состояние              : подключено\r\n    SSID                   : Observatory\r\n    BSSID                  : aa:bb:cc:dd:ee:ff\r\n" +
		"    Сигнал                 : 87%\r\n"
	w := parseNetsh(out)
	if w == nil || w.SSID != "Observatory" || w.Signal != 87 || w.Iface != "Беспроводная сеть" {
		t.Fatalf("%+v", w)
	}
	if parseNetsh("    Name : Wi-Fi\r\n    State : disconnected\r\n") != nil {
		t.Fatal("no SSID: not connected")
	}
}
