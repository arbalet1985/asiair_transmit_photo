package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

func TestInventoryStatesAndTotals(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "a.fit"), 1000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "b.fit"), 2000)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "c.fit"), 3000)
	writeRandom(t, filepath.Join(asiair, "Thumbnail", "a.jpg"), 10)
	writeRandom(t, filepath.Join(asiair, "Autorun", "log.txt"), 10)

	cfg := &config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "http://127.0.0.1:1", Token: "x", MaxRate: "0",
		StateFile: filepath.Join(tmp, "state.json"), Extensions: []string{".fit", ".jpg"},
		Exclude: []string{"Thumbnail"}, MinFileAge: config.Duration(time.Hour),
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	states := func() map[string]string {
		a.mu.Lock()
		defer a.mu.Unlock()
		m := map[string]string{}
		for _, f := range a.inventoryLocked(nil, time.Now()) {
			m[f.Path] = f.State
		}
		return m
	}

	// first scan: nothing is stable yet
	a.scan(ctx)
	if st := states()["EMMC Images/Autorun/Light/a.fit"]; st != protocol.StateWriting {
		t.Fatalf("new file: %s", st)
	}
	var filtered []protocol.RemoteFile
	a.mu.Lock()
	var wanted, bad []source.File
	a.src.Walk(ctx, nil, func(f source.File) error {
		if a.wanted(f.Path) && !a.excluded(f.Path) {
			wanted = append(wanted, f)
		} else {
			bad = append(bad, f)
		}
		return nil
	})
	filtered = a.inventoryLocked(bad, time.Now())
	a.mu.Unlock()
	if len(wanted) != 3 || len(bad) != 2 {
		t.Fatalf("wanted %d, filtered %d", len(wanted), len(bad))
	}
	nf := 0
	for _, f := range filtered {
		if f.State == protocol.StateFiltered {
			nf++
		}
	}
	if nf != 2 {
		t.Fatalf("filtered states: %d", nf)
	}

	// files become stable: queued
	a.cfg.MinFileAge = 0
	a.scan(ctx)
	st := states()
	for _, p := range []string{"a.fit", "b.fit", "c.fit"} {
		if s := st["EMMC Images/Autorun/Light/"+p]; s != protocol.StateQueued {
			t.Fatalf("%s: %s", p, s)
		}
	}

	// one sent, one failed
	a.mu.Lock()
	fa, fb := a.queue[0], a.queue[1]
	a.mu.Unlock()
	a.markSent(fa)
	a.fail(fb, errors.New("обрыв связи"))
	st = states()
	if st[fa.Path] != protocol.StateSent || st[fb.Path] != protocol.StateRetry {
		t.Fatalf("after send/fail: %v", st)
	}
	s := a.Status()
	if s.TotalFiles != 3 || s.TotalBytes != 6000 || s.DoneFiles != 1 || s.DoneBytes != fa.Size {
		t.Fatalf("totals: %+v", s)
	}
	if len(s.Failed) != 1 || s.Failed[0].Path != fb.Path || s.Failed[0].Error != "обрыв связи" || s.Failed[0].RetryAt.IsZero() {
		t.Fatalf("failed: %+v", s.Failed)
	}

	// deleting a file removes it from the totals
	if err := a.deletePath(fa.Path, false); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.updateTotalsLocked()
	a.mu.Unlock()
	if s := a.Status(); s.TotalFiles != 2 || s.DoneFiles != 0 {
		t.Fatalf("totals after delete: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", filepath.Base(fa.Path))); !os.IsNotExist(err) {
		t.Fatal("file not deleted")
	}
}

func TestExcluded(t *testing.T) {
	a := &Agent{cfg: &config.Agent{Exclude: []string{"Thumbnail", `Autorun\Flat`}}}
	for p, want := range map[string]bool{
		"EMMC Images/Thumbnail/x.jpg":         true,
		"EMMC Images/Plan/Thumbnail/x.jpg":    true,
		"EMMC Images/Autorun/Flat/f.fit":      true,
		"EMMC Images/Autorun/Light/Flat.fit":  false,
		"EMMC Images/Autorun/Light/M31/a.fit": false,
		"EMMC Images/a.fit":                   false,
	} {
		if got := a.excluded(p); got != want {
			t.Errorf("excluded(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDeleteFolder(t *testing.T) {
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "a.fit"), 100)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "2026-09-24", "b.fit"), 100)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31", "log.txt"), 10)
	writeRandom(t, filepath.Join(asiair, "Autorun", "Light", "M31 old", "c.fit"), 100)
	os.MkdirAll(filepath.Join(asiair, "Autorun", "Light", "M31", "empty"), 0o755)

	cfg := &config.Agent{
		ASIAIR: "dir:" + asiair, ReceiverURL: "http://127.0.0.1:1", Token: "x", MaxRate: "0",
		StateFile: filepath.Join(tmp, "state.json"), Extensions: []string{".fit"},
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.scan(context.Background())

	for _, bad := range []string{"EMMC Images", "EMMC Images/", "EMMC Images/Autorun/.."} {
		if err := a.deletePath(bad, true); err == nil {
			t.Fatalf("deleted %q", bad)
		}
	}
	if err := a.deletePath("EMMC Images/Autorun/Light/M31", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31")); !os.IsNotExist(err) {
		t.Fatal("folder not deleted")
	}
	if _, err := os.Stat(filepath.Join(asiair, "Autorun", "Light", "M31 old", "c.fit")); err != nil {
		t.Fatal("a folder with a similar name was deleted:", err)
	}
	a.mu.Lock()
	var left []string
	for _, f := range a.files {
		left = append(left, f.Path)
	}
	queued := len(a.queue)
	a.mu.Unlock()
	if len(left) != 1 || left[0] != "EMMC Images/Autorun/Light/M31 old/c.fit" || queued != 1 {
		t.Fatalf("files after delete: %v, queued %d", left, queued)
	}
	// a folder that is already gone is not an error
	if err := a.deletePath("EMMC Images/Autorun/Light/M31", true); err != nil {
		t.Fatal(err)
	}
}

func TestWantedExtensions(t *testing.T) {
	for _, tc := range []struct {
		ext, excl []string
		want      map[string]bool
	}{
		{[]string{".fit", ".jpg"}, []string{".jpg"}, map[string]bool{"a.fit": true, "a.JPG": false, "a.png": false}},
		{nil, []string{".jpg", ".txt"}, map[string]bool{"a.fit": true, "a.jpg": false, "log.TXT": false, "noext": true}},
		{nil, nil, map[string]bool{"a.txt": true}},
	} {
		a := &Agent{cfg: &config.Agent{Extensions: tc.ext, ExcludeExtensions: tc.excl}}
		for name, want := range tc.want {
			if got := a.wanted("EMMC Images/Light/" + name); got != want {
				t.Errorf("ext %v, excl %v: wanted(%s) = %v", tc.ext, tc.excl, name, got)
			}
		}
	}
}
