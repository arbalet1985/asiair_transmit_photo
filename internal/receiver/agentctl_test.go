package receiver

import (
	"path/filepath"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// TestAgentRestartAsked: the restart is offered to the agent run that was
// asked, and dropped once another run of the agent comes.
func TestAgentRestartAsked(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	old := protocol.SyncRequest{Session: "a", Seq: 1}
	s.Sync(old)
	s.AskRestart()
	old.Seq++
	if s.Sync(old).Restart == 0 {
		t.Fatal("restart not offered")
	}
	old.Seq++
	if s.Sync(old).Restart == 0 {
		t.Fatal("restart not offered again before the agent restarted")
	}
	if r := s.Sync(protocol.SyncRequest{Session: "b", Seq: 1}).Restart; r != 0 {
		t.Fatal("restart offered to the new run")
	}
	if at, _ := s.AgentCtlPending(); !at.IsZero() {
		t.Fatal("restart still pending")
	}
}

// TestAgentSwitchAsked: the switch is offered until the agent connects the
// new way or gives up.
func TestAgentSwitchAsked(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.SyncRequest{Session: "a", Seq: 1, Status: protocol.AgentStatus{Connection: protocol.ConnInternet}}
	s.AskSwitch(protocol.ConnTailscale, []string{"https://asiair-receiver:8443"})
	sw := s.Sync(req).Switch
	if sw == nil || sw.Connection != protocol.ConnTailscale || len(sw.URLs) != 1 {
		t.Fatalf("switch not offered: %+v", sw)
	}
	if _, to := s.AgentCtlPending(); to != protocol.ConnTailscale {
		t.Fatalf("pending %q", to)
	}
	// the agent gave up
	req.Seq++
	req.Status.SwitchDone = sw.ID
	if s.Sync(req).Switch != nil {
		t.Fatal("switch offered after the agent gave up")
	}
	// asked again: done once the agent comes through Tailscale
	s.AskSwitch(protocol.ConnTailscale, nil)
	if s.Sync(req).Switch == nil {
		t.Fatal("new switch not offered")
	}
	if s.Sync(protocol.SyncRequest{Session: "b", Seq: 1, Status: protocol.AgentStatus{Connection: protocol.ConnTailscale}}).Switch != nil {
		t.Fatal("switch offered after it was made")
	}
}

func TestNoteAgentURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, _ := LoadState(file, nil)
	s.NoteAgentURL(protocol.ConnInternet, "https://home.example.ru:8443")
	s2, err := LoadState(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u := s2.AgentURL(protocol.ConnInternet); u != "https://home.example.ru:8443" {
		t.Fatalf("address not kept: %q", u)
	}
}
