package receiver

import (
	"path/filepath"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// Changes for the agent wake the held sync request; results do not.
func TestStateChangedWakesSync(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rev := s.Sync(protocol.SyncRequest{}).Rev
	if rev == 0 {
		t.Fatal("no rev in the answer")
	}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	for name, change := range map[string]func(){
		"delete": func() { s.QueueDelete([]string{"EMMC Images/a.fit"}, protocol.CmdDelete, false) },
		"pause":  func() { s.SetPaused(true) },
		"rate":   func() { s.SetRate(nil) },
		"fetch":  func() { s.RequestFetch([]string{"EMMC Images/b.fit"}, true) },
	} {
		ch := s.Changed(rev)
		if ch == nil {
			t.Fatalf("%s: state already changed", name)
		}
		change()
		if !closed(ch) {
			t.Fatalf("%s did not wake the agent", name)
		}
		if s.Changed(rev) != nil {
			t.Fatalf("%s: old rev still current", name)
		}
		rev = s.Sync(protocol.SyncRequest{}).Rev
	}
	// the same delete again, or a result, changes nothing for the agent
	ch := s.Changed(rev)
	s.QueueDelete([]string{"EMMC Images/a.fit"}, protocol.CmdDelete, false)
	if closed(ch) {
		t.Fatal("a duplicate delete woke the agent")
	}
}

// A status that arrives after a newer one of the same agent run is ignored.
func TestSyncKeepsNewestStatus(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Sync(protocol.SyncRequest{Session: "a", Seq: 2, Status: protocol.AgentStatus{LocalPause: true}})
	s.Sync(protocol.SyncRequest{Session: "a", Seq: 1, Status: protocol.AgentStatus{LocalPause: false}}) // late
	if !s.Snapshot().Status.LocalPause {
		t.Fatal("a late status replaced a newer one")
	}
	// a restarted agent starts counting again
	s.Sync(protocol.SyncRequest{Session: "b", Seq: 1, Status: protocol.AgentStatus{LocalPause: false}})
	if s.Snapshot().Status.LocalPause {
		t.Fatal("status of a new agent run ignored")
	}
}

// Deleting a folder drops everything under it, and only that.
func TestDeleteDirResult(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := []protocol.RemoteFile{
		{Path: "EMMC Images/Light/M31/a.fit"},
		{Path: "EMMC Images/Light/M31/night/b.fit"},
		{Path: "EMMC Images/Light/M31 old/c.fit"},
	}
	s.SetInventory(inv)
	s.RequestFetch([]string{"EMMC Images/Light/M31/a.fit"}, true)
	// a file and a folder with the same path are different commands
	s.QueueDelete([]string{"EMMC Images/Light/M31"}, protocol.CmdDelete, false)
	if n, _ := s.QueueDelete([]string{"EMMC Images/Light/M31"}, protocol.CmdDeleteDir, true); n != 1 {
		t.Fatalf("queued %d", n)
	}
	snap := s.Snapshot()
	files, _ := buildFiles(nil, snap.Inventory, snap.Commands, protocol.AgentStatus{}, true, nil)
	for _, f := range files {
		if want := f.Path != "EMMC Images/Light/M31 old/c.fit"; f.Pending != want {
			t.Fatalf("%s: pending %v", f.Path, f.Pending)
		}
	}
	var id string
	for _, c := range snap.Commands {
		if c.Type == protocol.CmdDeleteDir {
			id = c.ID
		}
	}
	c, err := s.Result(protocol.CommandResult{ID: id, OK: true})
	if err != nil || c == nil || !c.IsDir() || !c.AlsoLocal {
		t.Fatalf("result: %+v, %v", c, err)
	}
	snap = s.Snapshot()
	if len(snap.Inventory) != 1 || snap.Inventory[0].Path != "EMMC Images/Light/M31 old/c.fit" || len(snap.Fetch) != 0 {
		t.Fatalf("after delete: %+v, fetch %v", snap.Inventory, snap.Fetch)
	}
}

// A synchronised folder requests its files, including ones found later,
// until the synchronisation is turned off.
func TestSyncDir(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetInventory([]protocol.RemoteFile{
		{Path: "EMMC Images/Light/M31/a.fit", State: protocol.StateQueued},
		{Path: "EMMC Images/Light/M31/b.fit", State: protocol.StateSent},
		{Path: "EMMC Images/Light/M31/t.txt", State: protocol.StateFiltered},
		{Path: "EMMC Images/Light/M31 old/c.fit", State: protocol.StateQueued},
	})
	rev := s.Sync(protocol.SyncRequest{}).Rev
	if err := s.SetSyncDir("EMMC Images/Light/M31", true); err != nil {
		t.Fatal(err)
	}
	if s.Changed(rev) != nil {
		t.Fatal("the agent was not woken")
	}
	fetch := func() []string { return s.Sync(protocol.SyncRequest{}).Fetch }
	if f := fetch(); len(f) != 1 || f[0] != "EMMC Images/Light/M31/a.fit" {
		t.Fatalf("fetch: %v", f)
	}
	// a new frame in a subfolder is requested as soon as the agent lists it
	s.SetInventory([]protocol.RemoteFile{
		{Path: "EMMC Images/Light/M31/a.fit", State: protocol.StateSent},
		{Path: "EMMC Images/Light/M31/n/d.fit", State: protocol.StateWriting},
		{Path: "EMMC Images/Light/M31 old/c.fit", State: protocol.StateQueued},
	})
	if f := fetch(); len(f) != 1 || f[0] != "EMMC Images/Light/M31/n/d.fit" {
		t.Fatalf("fetch after new frame: %v", f)
	}
	// a file requested by hand elsewhere stays when the folder is turned off
	s.RequestFetch([]string{"EMMC Images/Light/M31 old/c.fit"}, true)
	if err := s.SetSyncDir("EMMC Images/Light/M31", false); err != nil {
		t.Fatal(err)
	}
	if f := fetch(); len(f) != 1 || f[0] != "EMMC Images/Light/M31 old/c.fit" || len(s.Snapshot().SyncDirs) != 0 {
		t.Fatalf("fetch after off: %v", f)
	}
}
