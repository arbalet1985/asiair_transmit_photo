package agent

import (
	"time"

	"golang.org/x/sys/windows"
)

// processCPU is the processor time used by the agent so far (0 when unknown).
func processCPU() time.Duration {
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u) != nil {
		return 0
	}
	ft := func(t windows.Filetime) int64 { return int64(t.HighDateTime)<<32 | int64(t.LowDateTime) }
	return time.Duration(ft(k)+ft(u)) * 100 // 100 ns units
}
