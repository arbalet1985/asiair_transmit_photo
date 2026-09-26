package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

// Several files go at once, as many as set on the receiver page; the
// agent reports them and the diagnostics.
func TestStreams(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	var data [][]byte
	names := []string{"a.fit", "b.fit", "c.fit", "d.fit", "e.fit"}
	for _, n := range names {
		data = append(data, writeRandom(t, filepath.Join(asiair, "Autorun", "Light", n), 250_000))
	}
	uiAddr := freeAddr(t)
	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: uiAddr, Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })
	post := func(path string, body any) {
		b, _ := json.Marshal(body)
		r, err := http.Post("http://"+uiAddr+path, "application/json", bytes.NewReader(b))
		if err != nil || r.StatusCode >= 300 {
			t.Fatalf("%s: %v %v", path, err, r)
		}
		r.Body.Close()
	}
	startTransfer(t, uiAddr) // waits for the page
	post("/ui/api/streams", map[string]int{"streams": 4})

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()), MaxRate: "150KB/s",
		ScanInterval: config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })

	most := 0
	var diag *protocol.Diag
	waitFor(t, "all files", 20*time.Second, func() bool {
		st := a.Status()
		most = max(most, len(st.Uploads))
		if st.Diag != nil && st.Diag.Text != "" {
			diag = st.Diag
		}
		return st.SentFiles == len(names)
	})
	if most < 2 || most > 4 {
		t.Fatalf("files at once: %d, want 2..4", most)
	}
	if a.Status().Streams != 4 {
		t.Fatalf("streams %d", a.Status().Streams)
	}
	for i, n := range names {
		got, _ := os.ReadFile(filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", n))
		if !bytes.Equal(got, data[i]) {
			t.Fatalf("%s differs", n)
		}
	}
	// the shared limit held the streams back
	if diag == nil || diag.Bottleneck != "limit" {
		t.Fatalf("diag %+v", diag)
	}
	if a.Status().Speed > 250*1024 {
		t.Fatalf("speed %.0f over the shared limit", a.Status().Speed)
	}
}

func TestDiagText(t *testing.T) {
	d := protocol.Diag{Read: 0.1, Network: 0.8, Limit: 0.1}
	d.Bottleneck = bottleneck(d)
	if d.Bottleneck != "network" {
		t.Fatal(d.Bottleneck)
	}
	if s := diagText(d, 1); !strings.Contains(s, "сеть до приёмника") || !strings.Contains(s, "сеть 80%") || !strings.Contains(s, "больше потоков") {
		t.Fatal(s)
	}
	if s := diagText(d, protocol.MaxStreams); strings.Contains(s, "больше потоков") {
		t.Fatal(s)
	}
	d = protocol.Diag{Read: 0.7, Network: 0.2, Limit: 0.1, ReadSpeed: 600 * 1024}
	d.Bottleneck = bottleneck(d)
	if s := diagText(d, 3); !strings.Contains(s, "чтение с ASIAIR") || !strings.Contains(s, "600.0KB/s") {
		t.Fatal(s)
	}
	if diagText(protocol.Diag{}, 1) != "" {
		t.Fatal("text without data")
	}
}
