package autorun

import (
	"errors"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// value returns the registered command ("" when absent).
func value(name string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

// Set adds (on) or removes the autostart entry.
func Set(name, command string, on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(name, command)
}

// quote joins arguments into a Windows command line.
func quote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = syscall.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

// RunElevated starts this program again with administrator rights; Windows
// shows its UAC prompt. It returns once the new process has started.
func RunElevated(args ...string) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(quote(args))
	err = windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE)
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return ErrCancelled
	}
	return err
}

// WaitExit waits until the process pid has exited or the timeout passes.
func WaitExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
}
