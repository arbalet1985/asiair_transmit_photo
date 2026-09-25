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
	rev := s.Sync(protocol.AgentStatus{}).Rev
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
		"delete": func() { s.QueueDelete([]string{"EMMC Images/a.fit"}, false) },
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
		rev = s.Sync(protocol.AgentStatus{}).Rev
	}
	// the same delete again, or a result, changes nothing for the agent
	ch := s.Changed(rev)
	s.QueueDelete([]string{"EMMC Images/a.fit"}, false)
	if closed(ch) {
		t.Fatal("a duplicate delete woke the agent")
	}
}
