// Package autorun starts the programs with Windows: the per-user "Run"
// registry key (no administrator rights, runs at logon in the user's
// session) and relaunching the program with administrator rights (UAC) for
// actions such as installing a service.
package autorun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrUnsupported is returned outside Windows.
var ErrUnsupported = errors.New("доступно только в Windows")

// ErrCancelled is returned when the user declines the administrator prompt.
var ErrCancelled = errors.New("запрос прав администратора отклонён")

// Executable returns the path of the running program.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// Command builds the command line that starts this program with the given
// config file and command (e.g. "gui").
func Command(configPath, command string) (string, error) {
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`"%s" -config "%s" %s`, exe, configPath, command), nil
}

// Refresh re-registers the command when autostart is on but the program or
// its config has moved.
func Refresh(name, command string) {
	if v := value(name); v != "" && v != command {
		Set(name, command, true)
	}
}

// Enabled reports whether an autostart entry with this name exists.
func Enabled(name string) bool { return value(name) != "" }
