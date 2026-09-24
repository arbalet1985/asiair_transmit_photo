package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// startTransfer presses "Старт" on the receiver page.
func startTransfer(t *testing.T, uiAddr string) {
	t.Helper()
	var resp *http.Response
	waitFor(t, "receiver page", 5*time.Second, func() bool {
		var err error
		resp, err = http.Post("http://"+uiAddr+"/ui/api/pause", "application/json", strings.NewReader(`{"paused":false}`))
		return err == nil
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start: %d", resp.StatusCode)
	}
}

func writeRandom(t *testing.T, name string, size int) []byte {
	b := make([]byte, size)
	rand.Read(b)
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEndToEnd runs a receiver and an agent reading a folder that looks like
// the ASIAIR "EMMC Images" share.
func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	light := writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0001.fit"), 300_000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0002.fit"), 100_000)
	writeRandom(t, filepath.Join(asiair, "Thumbnail", "skip.jpg"), 1000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "log.txt"), 10)

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
	go srv.Run(ctx)
	startTransfer(t, uiAddr)

	acfg := &config.Agent{
		ASIAIR:              "dir:" + asiair,
		ReceiverURL:         "https://" + rcfg.Listen,
		Token:               "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()),
		MaxRate:             "400KB/s",
		ScanInterval:        config.Duration(300 * time.Millisecond),
		PollInterval:        config.Duration(200 * time.Millisecond),
		StateFile:           filepath.Join(tmp, "agent-state.json"),
		Extensions:          []string{".fit", ".jpg"},
		Exclude:             []string{"Thumbnail"},
	}
	a, err := New(acfg)
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)

	dest := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "M31")
	waitFor(t, "files to arrive", 10*time.Second, func() bool {
		_, e1 := os.Stat(filepath.Join(dest, "Light_M31_0001.fit"))
		_, e2 := os.Stat(filepath.Join(dest, "Light_M31_0002.fit"))
		return e1 == nil && e2 == nil
	})
	got, _ := os.ReadFile(filepath.Join(dest, "Light_M31_0001.fit"))
	if !bytes.Equal(got, light) {
		t.Fatal("received file differs")
	}
	if _, err := os.Stat(filepath.Join(rcfg.DestDir, "EMMC Images", "Thumbnail")); err == nil {
		t.Fatal("excluded folder was transferred")
	}
	if _, err := os.Stat(filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "log.txt")); err == nil {
		t.Fatal("filtered extension was transferred")
	}

	ui := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		resp, err := http.Post("http://"+uiAddr+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// delete on the ASIAIR from the web page, also locally
	p := "EMMC Images/Autorun/Light/M31/Light_M31_0002.fit"
	if r := ui("/ui/api/delete", map[string]any{"paths": []string{p}, "also_local": true}); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	waitFor(t, "delete on ASIAIR", 5*time.Second, func() bool {
		_, e1 := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0002.fit"))
		_, e2 := os.Stat(filepath.Join(dest, "Light_M31_0002.fit"))
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	})
	// the other file is untouched
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0001.fit")); err != nil {
		t.Fatal(err)
	}

	// path traversal is refused
	if r := ui("/ui/api/delete", map[string]any{"paths": []string{"EMMC Images/../../etc/passwd"}}); r.StatusCode != 400 {
		t.Fatalf("traversal accepted: %d", r.StatusCode)
	}

	// rate set on the web page reaches the agent
	ui("/ui/api/rate", map[string]any{"rate": "3MB/s"})
	waitFor(t, "rate change", 5*time.Second, func() bool { return a.lim.Rate() == 3<<20 })
	ui("/ui/api/rate", map[string]any{"rate": nil})
	waitFor(t, "rate reset", 5*time.Second, func() bool { return a.lim.Rate() == 400*1024 })

	// a new frame is picked up
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "Light_M31_0003.fit"), 50_000)
	waitFor(t, "new file", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dest, "Light_M31_0003.fit"))
		return err == nil
	})

	// wrong token is rejected
	c, _ := newClient(acfg.ReceiverURL, "wrong", acfg.ReceiverFingerprint)
	if err := c.postJSON(context.Background(), "/api/sync", map[string]any{}, nil); err == nil {
		t.Fatal("wrong token accepted")
	}
	// wrong certificate is rejected
	c, _ = newClient(acfg.ReceiverURL, "secret", strings.Repeat("00", 32))
	if err := c.postJSON(context.Background(), "/api/sync", map[string]any{}, nil); err == nil ||
		!strings.Contains(err.Error(), "отпечаток") {
		t.Fatalf("wrong fingerprint accepted: %v", err)
	}

	// helpers used by the settings form
	fp, err := FetchFingerprint(context.Background(), acfg.ReceiverURL)
	if err != nil || fp != srv.Fingerprint() {
		t.Fatalf("FetchFingerprint = %q, %v; want %q", fp, err, srv.Fingerprint())
	}
	if err := CheckReceiver(context.Background(), acfg.ReceiverURL, "secret", fp); err != nil {
		t.Fatalf("CheckReceiver: %v", err)
	}
	if err := CheckReceiver(context.Background(), acfg.ReceiverURL, "bad", fp); err == nil || !strings.Contains(err.Error(), "токен") {
		t.Fatalf("CheckReceiver with bad token: %v", err)
	}
	if res, err := a.src.Probe(context.Background()); err != nil || len(res.Shares) != 1 || res.Shares[0].Entries != 2 {
		t.Fatalf("Probe: %+v %v", res, err)
	}
}

// TestPause: a pause interrupts the file being sent and stops the transfer;
// after "continue" the file is resumed and arrives intact.
func TestPause(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	big := writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "big.fit"), 600_000)

	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()), MaxRate: "200KB/s",
		ScanInterval: config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	a.SetPaused(true)
	go a.Run(ctx)

	// paused from the start: nothing is sent
	waitFor(t, "scan", 5*time.Second, func() bool { return a.Status().QueueFiles == 1 })
	time.Sleep(500 * time.Millisecond)
	if st := a.Status(); st.Uploading != "" || st.SentFiles != 0 || !st.Paused {
		t.Fatalf("transfer while paused: %+v", st)
	}

	// a new receiver waits for "Старт": the ASIAIR tree is shown, nothing is sent
	a.SetPaused(false)
	waitFor(t, "wait for start", 5*time.Second, func() bool { st := a.Status(); return st.WaitStart && st.Paused })
	waitFor(t, "inventory at the receiver", 5*time.Second, func() bool {
		resp, err := http.Get("http://" + rcfg.UIListen + "/ui/api/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var ui struct {
			Files     []struct{ Status string } `json:"files"`
			WaitStart bool                      `json:"wait_start"`
		}
		json.NewDecoder(resp.Body).Decode(&ui)
		return ui.WaitStart && len(ui.Files) == 1
	})
	time.Sleep(500 * time.Millisecond)
	if st := a.Status(); st.Uploading != "" || st.SentFiles != 0 {
		t.Fatalf("transfer before start: %+v", st)
	}

	// start, then pause in the middle of the file
	startTransfer(t, rcfg.UIListen)
	waitFor(t, "upload to start", 5*time.Second, func() bool { return a.Status().UploadDone > 100_000 })
	a.SetPaused(true)
	waitFor(t, "upload to stop", 5*time.Second, func() bool { return a.Status().Uploading == "" })
	time.Sleep(500 * time.Millisecond)
	if st := a.Status(); st.Uploading != "" || st.SentFiles != 0 || len(st.Failed) != 0 {
		t.Fatalf("paused upload: %+v", st)
	}
	// the part already at the receiver still counts in the overall progress
	if st := a.Status(); st.DoneBytes < 100_000 || st.DoneBytes >= int64(len(big)) {
		t.Fatalf("overall progress while paused: %d of %d", st.DoneBytes, st.TotalBytes)
	}

	a.SetPaused(false)
	dest := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "big.fit")
	waitFor(t, "file to arrive", 10*time.Second, func() bool {
		_, err := os.Stat(dest)
		return err == nil
	})
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, big) {
		t.Fatal("received file differs")
	}
	// resumed, not sent again from the start
	if st := a.Status(); st.SentBytes >= int64(len(big)) {
		t.Fatalf("sent %d bytes of %d: not resumed", st.SentBytes, len(big))
	}

	// pause from the receiver page
	pause := func(on bool) {
		b, _ := json.Marshal(map[string]bool{"paused": on})
		resp, err := http.Post("http://"+rcfg.UIListen+"/ui/api/pause", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("pause: %d", resp.StatusCode)
		}
	}
	pause(true)
	waitFor(t, "remote pause", 5*time.Second, func() bool { st := a.Status(); return st.Paused && st.RemotePause && !st.LocalPause })
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "next.fit"), 10_000)
	next := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light", "next.fit")
	waitFor(t, "scan", 5*time.Second, func() bool { return a.Status().QueueFiles == 1 })
	a.SetPaused(false) // the window cannot lift the receiver's pause
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(next); err == nil {
		t.Fatal("file sent during the receiver's pause")
	}
	pause(false)
	waitFor(t, "file after remote resume", 10*time.Second, func() bool {
		_, err := os.Stat(next)
		return err == nil
	})
	if a.Status().Paused {
		t.Fatal("still paused")
	}
}

// TestFetchWhilePaused: before "Старт" only the files requested on the
// receiver page are sent.
func TestFetchWhilePaused(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	want := writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "a.fit"), 50_000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "b.fit"), 50_000)

	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"),
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()), MaxRate: "0",
		ScanInterval: config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)
	waitFor(t, "queue", 5*time.Second, func() bool { st := a.Status(); return st.WaitStart && st.QueueFiles == 2 })

	resp, err := http.Post("http://"+rcfg.UIListen+"/ui/api/fetch", "application/json",
		strings.NewReader(`{"paths":["EMMC Images/Autorun/Light/a.fit"]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch: %d", resp.StatusCode)
	}
	dest := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "Light")
	waitFor(t, "requested file", 5*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(dest, "a.fit"))
		return err == nil && bytes.Equal(b, want)
	})
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dest, "b.fit")); err == nil {
		t.Fatal("not requested file sent before start")
	}
	if st := a.Status(); !st.WaitStart || st.SentFiles != 1 {
		t.Fatalf("status: %+v", st)
	}
	// the request is dropped once the file is received
	waitFor(t, "request cleared", 5*time.Second, func() bool {
		resp, err := http.Get("http://" + rcfg.UIListen + "/ui/api/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var ui struct {
			Fetch []string `json:"fetch"`
		}
		json.NewDecoder(resp.Body).Decode(&ui)
		return len(ui.Fetch) == 0
	})

	startTransfer(t, rcfg.UIListen)
	waitFor(t, "the rest after start", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dest, "b.fit"))
		return err == nil
	})
}

// TestDiskFull: with a reserve larger than the free space the receiver
// refuses files and the agent pauses; lowering the reserve on the page
// lets the transfer go on.
func TestDiskFull(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "a.fit"), 50_000)

	uiAddr := freeAddr(t)
	rcfg := &config.Receiver{
		Listen: freeAddr(t), UIListen: uiAddr, Token: "secret", DisableTLS: true,
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data"), MinFree: "99.99%",
	}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)
	startTransfer(t, uiAddr)

	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "http://" + rcfg.Listen, Token: "secret", MaxRate: "0",
		ScanInterval: config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"), Extensions: []string{".fit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	go a.Run(ctx)

	waitFor(t, "agent paused for disk space", 5*time.Second, func() bool {
		st := a.Status()
		return st.Paused && st.DiskFull != ""
	})
	got := filepath.Join(rcfg.DestDir, "EMMC Images", "Autorun", "a.fit")
	if _, err := os.Stat(got); err == nil {
		t.Fatal("file received despite the reserve")
	}
	if st := a.Status(); len(st.Failed) != 0 {
		t.Fatalf("disk full counted as a file failure: %+v", st.Failed)
	}

	resp, err := http.Post("http://"+uiAddr+"/ui/api/minfree", "application/json", strings.NewReader(`{"min_free":"0"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("minfree: %d", resp.StatusCode)
	}
	waitFor(t, "file after lowering the reserve", 5*time.Second, func() bool {
		_, err := os.Stat(got)
		return err == nil
	})
	if st := a.Status(); st.DiskFull != "" || st.Paused {
		t.Fatalf("still paused: %+v", st)
	}
}
