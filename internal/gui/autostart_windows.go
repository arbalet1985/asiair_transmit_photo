package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// Autostart uses the per-user "Run" key: no administrator rights needed, and
// the program starts in the user's session, so the tray icon is visible.
const (
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runName = "ASIAIR Agent"
)

func autostartCommand(configPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return fmt.Sprintf(`"%s" -config "%s" gui`, exe, configPath), nil
}

// autostartValue returns the registered command ("" when autostart is off).
func autostartValue() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runName)
	if err != nil {
		return ""
	}
	return v
}

// AutostartEnabled reports whether the agent starts at Windows logon.
func AutostartEnabled() bool { return autostartValue() != "" }

// SetAutostart turns starting at Windows logon on or off.
func SetAutostart(on bool, configPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	cmd, err := autostartCommand(configPath)
	if err != nil {
		return err
	}
	return k.SetStringValue(runName, cmd)
}

// refreshAutostart re-registers the command when autostart is on but the
// program or its config has moved.
func refreshAutostart(configPath string) {
	v := autostartValue()
	if v == "" {
		return
	}
	if cmd, err := autostartCommand(configPath); err == nil && cmd != v {
		SetAutostart(true, configPath)
	}
}
