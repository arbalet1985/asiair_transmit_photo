package receiver

import (
	"strings"
	"testing"
)

func TestTrayInfoAndPause(t *testing.T) {
	s, _ := newTestServer(t)
	info := s.TrayInfo()
	if !strings.HasPrefix(info, "агент не на связи") || !strings.Contains(info, "свободно на диске") {
		t.Fatalf("tray info: %q", info)
	}
	if len([]rune("ASIAIR Receiver\n"+info)) > 120 {
		t.Fatalf("too long for the tooltip: %q", info)
	}
	// a new receiver waits for "Старт": the tray starts it and pauses it
	if !s.TransferPaused() {
		t.Fatal("a new receiver should wait for the start")
	}
	if err := s.SetTransferPaused(false); err != nil || s.TransferPaused() {
		t.Fatalf("start: %v", err)
	}
	if err := s.SetTransferPaused(true); err != nil || !s.TransferPaused() {
		t.Fatalf("pause: %v", err)
	}
}
