//go:build !windows

package agent

import (
	"syscall"
	"time"
)

// processCPU is the processor time used by the agent so far (0 when unknown).
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
