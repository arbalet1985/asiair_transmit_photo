//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// hideWindow detaches the program from the terminal of its parent.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
