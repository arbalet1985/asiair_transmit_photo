package receiver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func TestBuildFiles(t *testing.T) {
	const d = "EMMC Images/Light/"
	local := []LocalFile{
		{Path: d + "received.fit", Size: 100},
		{Path: d + "changed.fit", Size: 50},
		{Path: d + "gone.fit", Size: 70},
		{Path: d + "deleting.fit", Size: 100},
	}
	inv := []protocol.RemoteFile{
		{Path: d + "received.fit", Size: 100, State: protocol.StateSent},
		{Path: d + "changed.fit", Size: 100, State: protocol.StateQueued},
		{Path: d + "uploading.fit", Size: 200, State: protocol.StateQueued},
		{Path: d + "retry.fit", Size: 100, State: protocol.StateRetry},
		{Path: d + "partial.fit", Size: 400, State: protocol.StateQueued},
		{Path: d + "writing.fit", Size: 100, State: protocol.StateWriting},
		{Path: d + "queued.fit", Size: 100, State: protocol.StateQueued},
		{Path: d + "deleting.fit", Size: 100, State: protocol.StateSent},
		{Path: d + "old-agent.fit", Size: 100}, // no State: older agent
		{Path: d + "cut.fit", Size: 100, State: protocol.StateRetry},
		{Path: "EMMC Images/Thumbnail/t.jpg", Size: 5, State: protocol.StateFiltered},
	}
	cmds := []*Command{{Command: protocol.Command{Path: d + "deleting.fit"}, Status: StatusPending}}
	agent := protocol.AgentStatus{
		Uploading: d + "uploading.fit", UploadDone: 50, UploadSize: 200, Speed: 100,
		Failed: []protocol.FailedFile{
			{Path: d + "retry.fit", Error: "обрыв", RetryAt: time.Now().Add(time.Minute)},
			{Path: d + "cut.fit", Error: "обрыв", RetryAt: time.Now().Add(time.Minute)},
		},
	}
	partials := map[string]int64{d + "partial.fit": 100, d + "cut.fit": 60}

	files, tot := buildFiles(local, inv, cmds, agent, true, partials)
	got := map[string]uiFile{}
	for _, f := range files {
		got[strings.TrimPrefix(f.Path, d)] = f
	}
	want := map[string]string{
		"received.fit": FileReceived, "changed.fit": FileChanged, "gone.fit": FileLocalOnly,
		"uploading.fit": FileUploading, "retry.fit": FileRetry, "partial.fit": FilePartial,
		"writing.fit": FileWriting, "queued.fit": FileQueued, "deleting.fit": FileDeleting,
		"old-agent.fit": FileQueued, "EMMC Images/Thumbnail/t.jpg": FileFiltered,
		"cut.fit": FilePartial,
	}
	for name, st := range want {
		if got[name].Status != st {
			t.Errorf("%s: status %q, want %q", name, got[name].Status, st)
		}
	}
	if p := got["uploading.fit"].Progress; p != 0.25 {
		t.Errorf("uploading progress %v", p)
	}
	if p := got["partial.fit"].Progress; p != 0.25 {
		t.Errorf("partial progress %v", p)
	}
	if got["retry.fit"].Error != "обрыв" || got["retry.fit"].RetryAt.IsZero() {
		t.Errorf("retry details: %+v", got["retry.fit"])
	}
	if c := got["cut.fit"]; c.Progress != 0.6 || c.RetryAt.IsZero() || c.Error != "обрыв" {
		t.Errorf("interrupted file: %+v", c)
	}

	// totals: 10 transferable files on the ASIAIR (filtered and local-only excluded)
	// bytes: 100+100+200+100+400+100+100+100+100+100 = 1400
	// done: received 100 + deleting 100 (here already) + uploading 50 + partial 100 + cut 60 = 410
	if tot.Files != 10 || tot.Bytes != 1400 || tot.DoneFiles != 2 || tot.DoneBytes != 410 ||
		tot.RemainingBytes != 990 || tot.Failed != 2 || tot.ETASeconds != 9.9 {
		t.Fatalf("totals: %+v", tot)
	}

	// agent offline: nothing is shown as uploading, no ETA
	files, tot = buildFiles(local, inv, cmds, agent, false, partials)
	for _, f := range files {
		if f.Status == FileUploading {
			t.Fatal("uploading shown while agent is offline")
		}
	}
	if tot.ETASeconds != 0 {
		t.Fatal("ETA while offline")
	}
}

func TestStorePartials(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := "EMMC Images/Light/a.fit"
	s.Write(p, 1000, time.Now(), 0, strings.NewReader(strings.Repeat("x", 300)))
	got := s.Partials()
	if got[p] != 300 || len(got) != 1 {
		t.Fatalf("partials: %v", got)
	}
	// a stray file in .partial is ignored
	os.WriteFile(filepath.Join(s.root, partialDir, "junk.json"), []byte("{"), 0o644)
	if len(s.Partials()) != 1 {
		t.Fatal("junk counted")
	}
}
