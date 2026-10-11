package agent

import (
	"context"
	"os/exec"
	"syscall"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func currentWiFi(ctx context.Context) *protocol.WiFiInfo {
	cmd := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // no console window
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseNetsh(string(out))
}
