package receiver

import (
	"os"
	"path/filepath"
	"strings"
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

// TestAgentTSLoginAsked: new Tailscale credentials are offered to the agent
// run that was asked until it takes them (another run) or gives up, and
// never written to the state file.
func TestAgentTSLoginAsked(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.SyncRequest{Session: "a", Seq: 1}
	s.Sync(req)
	s.AskTSLogin(protocol.TSLogin{AuthKey: "tskey-auth-secret", Hostname: "asiair-agent"})
	req.Seq++
	l := s.Sync(req).TSLogin
	if l == nil || l.ID == 0 || l.AuthKey != "tskey-auth-secret" || !s.TSLoginPending() {
		t.Fatalf("login not offered: %+v", l)
	}
	s.mu.Lock()
	s.save()
	s.mu.Unlock()
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "tskey") {
		t.Fatal("the key was written to the state file")
	}
	// the agent gave up
	req.Seq++
	req.Status.TSLoginDone = l.ID
	if s.Sync(req).TSLogin != nil || s.TSLoginPending() {
		t.Fatal("login offered after the agent gave up")
	}
	// asked again: done once another run of the agent comes
	s.AskTSLogin(protocol.TSLogin{AuthKey: "tskey-auth-2"})
	req.Seq++
	if s.Sync(req).TSLogin == nil {
		t.Fatal("new login not offered")
	}
	if s.Sync(protocol.SyncRequest{Session: "b", Seq: 1}).TSLogin != nil {
		t.Fatal("login offered to the new run")
	}
}

// TestUITSLogin: the page cannot send a login an agent cannot take, nor
// without Tailscale on the receiver to check it through.
func TestUITSLogin(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	s.state.Sync(protocol.SyncRequest{Session: "a", Seq: 1, Status: protocol.AgentStatus{CanRestart: true}})
	if code := uiCall(t, ui, "POST", "/ui/api/agentctl", map[string]string{"action": "tslogin", "auth_key": "tskey-auth-x"}, nil); code != 409 {
		t.Fatalf("an old agent: %d", code)
	}
	s.state.Sync(protocol.SyncRequest{Session: "a", Seq: 2, Status: protocol.AgentStatus{CanRestart: true, CanTSLogin: true}})
	if code := uiCall(t, ui, "POST", "/ui/api/agentctl", map[string]string{"action": "tslogin", "auth_key": "tskey-auth-x"}, nil); code != 409 || s.state.TSLoginPending() {
		t.Fatalf("no Tailscale on the receiver: %d", code)
	}
	if !validHostname("asiair-agent2") || validHostname("asiair agent") || validHostname("-x") {
		t.Fatal("hostname check")
	}
}
