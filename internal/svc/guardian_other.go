//go:build !windows

package svc

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
