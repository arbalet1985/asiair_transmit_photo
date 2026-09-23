package gui

import "github.com/arbalet1985/asiair_transmit_photo/internal/autorun"

// Autostart uses the per-user "Run" key: no administrator rights needed, and
// the program starts in the user's session, so the tray icon is visible.
const runName = "ASIAIR Agent"

// AutostartEnabled reports whether the agent starts at Windows logon.
func AutostartEnabled() bool { return autorun.Enabled(runName) }

// SetAutostart turns starting at Windows logon on or off.
func SetAutostart(on bool, configPath string) error {
	cmd, err := autorun.Command(configPath, "gui")
	if err != nil {
		return err
	}
	return autorun.Set(runName, cmd, on)
}

// refreshAutostart re-registers the command when the program or its config
// has moved.
func refreshAutostart(configPath string) {
	if cmd, err := autorun.Command(configPath, "gui"); err == nil {
		autorun.Refresh(runName, cmd)
	}
}
