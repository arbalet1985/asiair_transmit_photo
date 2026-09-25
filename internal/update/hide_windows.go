package update

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow starts a console program without a console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
