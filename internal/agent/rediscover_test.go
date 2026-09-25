package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

// deadSource is an ASIAIR that does not answer.
type deadSource struct{ closed bool }

var errDead = errors.New("dial tcp: i/o timeout")

func (d *deadSource) Walk(context.Context, func(string) bool, func(source.File) error) error {
	return errDead
}
func (d *deadSource) Open(string) (io.ReadSeekCloser, error) { return nil, errDead }
func (d *deadSource) Remove(string) error                    { return errDead }
func (d *deadSource) Close() error                           { d.closed = true; return nil }
func (d *deadSource) Probe(context.Context) (*source.ProbeResult, error) {
	return nil, errDead
}

func writeAgentConfig(t *testing.T, dir, body string) *config.Agent {
	t.Helper()
	p := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestRediscover: an ASIAIR that no longer answers at the saved address is
// found in the network, and the new address is used and saved; when none
// is found the saved path stays as it was.
func TestRediscover(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "a.fit"), 1000)
	cfg := writeAgentConfig(t, tmp, `asiair: '\\192.168.1.50\EMMC Images'
receiver_url: 'https://127.0.0.1:1'
token: 'x'
state_file: state.json
`)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dead := &deadSource{}
	a.src = dead
	if err := a.scan(context.Background()); err == nil || a.Status().ASIAIRError == "" || a.Status().Connected {
		t.Fatalf("failed scan not reported: %+v", a.Status())
	}
	var changed map[string]string
	a.OnConfigChanged = func(v map[string]string) { changed = v }

	// nothing found: the path stays
	a.discover = func(context.Context, func(string)) ([]source.Device, error) {
		return []source.Device{{Kind: source.KindStellaVita, IP: "192.168.1.60"}}, nil
	}
	a.rediscover(context.Background())
	if a.source() != source.Source(dead) || changed != nil {
		t.Fatal("path changed although no ASIAIR was found")
	}

	// found at a new address that answers
	var tried []string
	a.discover = func(context.Context, func(string)) ([]source.Device, error) {
		return []source.Device{{Kind: source.KindASIAIR, IP: "192.168.1.51"}, {Kind: source.KindASIAIR, IP: "192.168.1.77"}}, nil
	}
	a.newSource = func(link, user, pass string) (source.Source, error) {
		tried = append(tried, link)
		if strings.Contains(link, "192.168.1.77") {
			return source.NewDir(asiair), nil
		}
		return &deadSource{}, nil
	}
	a.rediscover(context.Background())
	want := `\\192.168.1.77\EMMC Images`
	if len(tried) != 2 || changed["asiair"] != want || a.Status().ASIAIR != want || !dead.closed {
		t.Fatalf("not switched: tried %v, changed %v, status %q", tried, changed, a.Status().ASIAIR)
	}
	saved, err := config.LoadAgent(cfg.Path)
	if err != nil || saved.ASIAIR != want {
		t.Fatalf("not saved: %+v %v", saved, err)
	}
	if err := a.scan(context.Background()); err != nil || a.Status().TotalFiles != 1 {
		t.Fatalf("scan at the new address: %v %+v", err, a.Status())
	}
	if st := a.Status(); st.ASIAIRError != "" || !st.Connected {
		t.Fatalf("error not cleared: %+v", st)
	}
}

// TestScanFailedTriggersSearch: the first failed scan starts a search, later
// ones only after several failures, and folders are never searched for.
func TestScanFailedTriggersSearch(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeAgentConfig(t, tmp, "asiair: '192.168.1.50'\nreceiver_url: 'https://127.0.0.1:1'\ntoken: 'x'\nstate_file: state.json\n")
	a, _ := New(cfg)
	searched := make(chan struct{}, 10)
	a.discover = func(context.Context, func(string)) ([]source.Device, error) {
		searched <- struct{}{}
		return nil, nil
	}
	a.scanFailed(context.Background())
	select {
	case <-searched:
	case <-time.After(2 * time.Second):
		t.Fatal("no search after the first failed scan")
	}
	for a.discovering.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	a.scanFailed(context.Background()) // too soon after the last search
	select {
	case <-searched:
		t.Fatal("searched again at once")
	case <-time.After(200 * time.Millisecond):
	}

	dir := writeAgentConfig(t, t.TempDir(), "asiair: 'dir:"+tmp+"'\nreceiver_url: 'https://127.0.0.1:1'\ntoken: 'x'\nstate_file: state.json\n")
	b, _ := New(dir)
	b.discover = a.discover
	b.scanFailed(context.Background())
	select {
	case <-searched:
		t.Fatal("a folder was searched for")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestFingerprintOnFirstContact: settings saved without the receiver
// certificate (it was unreachable then) still work: the certificate of the
// first contact is trusted and written to agent.yaml.
func TestFingerprintOnFirstContact(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "a.fit"), 5000)
	rcfg := &config.Receiver{Listen: freeAddr(t), UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data")}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })

	cfg := writeAgentConfig(t, tmp, "asiair: 'dir:"+asiair+"'\nreceiver_url: 'https://"+rcfg.Listen+"'\ntoken: 'secret'\nstate_file: state.json\nscan_interval: 200ms\npoll_interval: 200ms\n")
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	background(t, func() { a.Run(ctx) })
	waitFor(t, "fingerprint saved", 5*time.Second, func() bool {
		c, err := config.LoadAgent(cfg.Path)
		return err == nil && normFP(c.ReceiverFingerprint) == normFP(srv.Fingerprint())
	})
	if a.Status().Connected == false && a.Status().LastScan.IsZero() {
		t.Fatalf("agent did not work: %+v", a.Status())
	}
}

// TestRediscoverSeveral: with several ASIAIRs in the network only the one
// holding images already sent is taken; otherwise the path stays.
func TestRediscoverSeveral(t *testing.T) {
	tmp := t.TempDir()
	mine := filepath.Join(tmp, "mine", "EMMC Images")
	other := filepath.Join(tmp, "other", "EMMC Images")
	writeRandom(t, filepath.Join(mine, "Autorun", "a.fit"), 1000)
	writeRandom(t, filepath.Join(other, "Autorun", "b.fit"), 1000)
	cfg := writeAgentConfig(t, tmp, "asiair: '192.168.1.50'\nreceiver_url: 'https://127.0.0.1:1'\ntoken: 'x'\nstate_file: state.json\n")
	a, _ := New(cfg)
	a.src = &deadSource{}
	a.discover = func(context.Context, func(string)) ([]source.Device, error) {
		return []source.Device{{Kind: source.KindASIAIR, IP: "192.168.1.51"}, {Kind: source.KindASIAIR, IP: "192.168.1.52"}}, nil
	}
	a.newSource = func(link, user, pass string) (source.Source, error) {
		if strings.Contains(link, ".52") {
			return source.NewDir(mine), nil
		}
		return source.NewDir(other), nil
	}
	// no history: which one is ours cannot be told
	a.rediscover(context.Background())
	if a.Status().ASIAIR != "192.168.1.50" {
		t.Fatalf("switched without knowing: %q", a.Status().ASIAIR)
	}
	a.sent["EMMC Images/Autorun/a.fit"] = sentInfo{Size: 1000}
	a.rediscover(context.Background())
	if a.Status().ASIAIR != "192.168.1.52" {
		t.Fatalf("not switched to the ASIAIR with our images: %q", a.Status().ASIAIR)
	}
}
