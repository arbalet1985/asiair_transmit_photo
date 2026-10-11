package svc

import (
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// The guardian brings a program running in the tray back after a crash.
// A Windows service is restarted by the service manager (recovery set at
// install); a tray program has nobody to do that, so it starts a small
// copy of itself ("guardian") that waits for it to end. A normal exit
// (code 0: "Выход", a restart for an update) ends the guardian too; any
// other end (a panic, a fatal error, the process killed) starts the
// program again, at most guardMax times in guardWindow.

const (
	guardCommand = "guardian"
	guardDelay   = 10 * time.Second
	guardMax     = 5
	guardWindow  = 30 * time.Minute
)

// StartGuardian starts the guardian of this process (tray mode only).
func StartGuardian(app string) {
	if IsService() || os.Getenv("ASIAIR_NO_GUARDIAN") != "" {
		return
	}
	exe, err := update.Executable()
	if err != nil {
		return
	}
	args := append([]string{guardCommand, strconv.Itoa(os.Getpid())}, os.Args[1:]...)
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("Страховка от сбоев не запущена: %v", err)
		return
	}
	cmd.Process.Release()
}

// guardian waits for the process pid and starts the program again (with
// args) when it ended abnormally.
func guardian(app string, pid int, args []string) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	st, err := p.Wait()
	if err != nil || st.ExitCode() == 0 {
		return
	}
	// how often it was brought back lately
	file := filepath.Join(exeDir(), app+".guardian.json")
	var times []time.Time
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &times)
	}
	now := time.Now()
	recent := times[:0]
	for _, t := range times {
		if now.Sub(t) < guardWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= guardMax {
		return // crashes again and again: a restart will not help
	}
	recent = append(recent, now)
	if b, err := json.Marshal(recent); err == nil {
		os.WriteFile(file, b, 0o644)
	}
	time.Sleep(guardDelay)
	exe, err := update.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, args...)
	// started like after an update: waits for the lock, no window shown
	cmd.Env = append(os.Environ(), restartEnv+"=1", "ASIAIR_GUARDIAN_RESTART="+strconv.Itoa(st.ExitCode()))
	hideWindow(cmd)
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

// GuardianRestart reports the exit code of the run before when this
// process was started by the guardian after a crash.
func GuardianRestart() (code int, ok bool) {
	v := os.Getenv("ASIAIR_GUARDIAN_RESTART")
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}
