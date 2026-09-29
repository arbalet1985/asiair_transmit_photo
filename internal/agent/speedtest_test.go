package agent

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
)

func TestSpeedTests(t *testing.T) {
	oldNet, oldRead := speedNetDuration, speedReadDuration
	speedNetDuration, speedReadDuration = 700*time.Millisecond, 500*time.Millisecond
	defer func() { speedNetDuration, speedReadDuration = oldNet, oldRead }()

	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "a.fit"), 300_000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "b.fit"), 900_000)

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
	background(t, func() { srv.Run(ctx) })
	a, err := New(&config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: strings.ToLower(srv.Fingerprint()), MaxRate: "100KB/s",
		ScanInterval: config.Duration(200 * time.Millisecond), PollInterval: config.Duration(200 * time.Millisecond),
		StateFile: filepath.Join(tmp, "agent-state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })
	waitFor(t, "scan", 5*time.Second, func() bool { return a.Status().QueueFiles == 2 })

	run := func(kind string) *protocol.SpeedResult {
		t.Helper()
		var resp *http.Response
		waitFor(t, "receiver page", 5*time.Second, func() bool {
			var err error
			resp, err = http.Post("http://"+rcfg.UIListen+"/ui/api/speedtest", "application/json", strings.NewReader(`{"kind":"`+kind+`"}`))
			return err == nil
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %d", kind, resp.StatusCode)
		}
		var res *protocol.SpeedResult
		waitFor(t, kind+" result", 15*time.Second, func() bool {
			res = a.Status().SpeedTest
			return res != nil && res.Kind == kind && !res.Running
		})
		return res
	}

	// the network test is not held by the 100KB/s limit of the transfer
	res := run(protocol.SpeedTestNetwork)
	if res.Error != "" || res.Bytes < 1<<20 || res.Speed < 1<<20 {
		t.Fatalf("network: %+v", res)
	}
	// the ASIAIR test reads the largest file of the storage
	res = run(protocol.SpeedTestASIAIR)
	if res.Error != "" || len(res.Shares) != 1 {
		t.Fatalf("asiair: %+v", res)
	}
	if s := res.Shares[0]; s.Share != "EMMC Images" || !strings.HasSuffix(s.File, "b.fit") || s.Bytes != 900_000 || s.Speed <= 0 {
		t.Fatalf("asiair share: %+v", s)
	}
	// the receiver stops asking once the result is in
	waitFor(t, "pending cleared", 5*time.Second, func() bool {
		resp, err := http.Get("http://" + rcfg.UIListen + "/ui/api/state")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return err == nil && !strings.Contains(string(b), "speed_pending")
	})
}

func TestDiagShares(t *testing.T) {
	var d diag
	d.start = time.Now().Add(-diagWindow)
	d.readNs.Add(int64(time.Second))
	d.netNs.Add(int64(time.Second))
	d.bytes.Add(2 << 20)
	d.addShare("TF Images", 1<<20, int64(time.Second)) // waited: 1 MB/s
	d.addShare("EMMC Images", 1<<20, int64(time.Millisecond))
	res := d.result(3)
	if res == nil || len(res.Shares) != 2 {
		t.Fatalf("result: %+v", res)
	}
	if s := res.Shares[0]; s.Share != "EMMC Images" || s.Speed != 0 {
		t.Fatalf("eMMC did not hold the sending back: %+v", s)
	}
	if s := res.Shares[1]; s.Share != "TF Images" || s.Speed < 1<<20-1 || s.Speed > 1<<20+1 {
		t.Fatalf("SD card: %+v", s)
	}
}
