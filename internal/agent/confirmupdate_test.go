package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// With ConfirmUpdate the offered version is only shown until the user
// presses "Обновить", even with auto_update on.
func TestConfirmUpdateWaitsForUser(t *testing.T) {
	old := protocol.Version
	protocol.Version = "1.0.0"
	defer func() { protocol.Version = old }()

	restarted := false
	a := &Agent{cfg: &config.Agent{AutoUpdate: true}, ConfirmUpdate: true,
		Restart: func() error { restarted = true; return nil }}
	offer := &protocol.UpdateOffer{Version: "1.1.0"}

	a.maybeUpdate(context.Background(), offer)
	if a.updating {
		t.Fatal("installing without the user")
	}
	if v := a.UpdateAvailable(); v != "1.1.0" {
		t.Fatalf("UpdateAvailable = %q", v)
	}
	if u := a.status.Update; !strings.Contains(u, "доступна версия 1.1.0") {
		t.Fatalf("status %q", u)
	}

	// the receiver no longer offers it: the button goes away
	a.maybeUpdate(context.Background(), nil)
	if v := a.UpdateAvailable(); v != "" || a.status.Update != "" {
		t.Fatalf("after withdrawal: %q, %q", v, a.status.Update)
	}
	if err := a.InstallUpdate(context.Background()); err == nil {
		t.Fatal("installed a withdrawn version")
	}
	if restarted {
		t.Fatal("restarted")
	}
}
