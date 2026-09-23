//go:build !windows

package autorun

import "time"

func value(name string) string { return "" }

// Set is not available outside Windows.
func Set(name, command string, on bool) error { return ErrUnsupported }

// RunElevated is not available outside Windows.
func RunElevated(args ...string) error { return ErrUnsupported }

// WaitExit is not needed outside Windows.
func WaitExit(pid int, timeout time.Duration) {}
