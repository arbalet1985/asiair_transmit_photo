//go:build !windows

package agent

import (
	"context"
	"os/exec"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func currentWiFi(ctx context.Context) *protocol.WiFiInfo {
	out, err := exec.CommandContext(ctx, "iwgetid", "-r").Output()
	if err != nil {
		return nil
	}
	if ssid := strings.TrimSpace(string(out)); ssid != "" {
		return &protocol.WiFiInfo{SSID: ssid}
	}
	return nil
}
