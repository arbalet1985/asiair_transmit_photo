package svc

import (
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"

	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// restartEnv marks a process started by Restart: it waits for the old one
// to let go of the single-instance lock and starts without showing a window.
const restartEnv = update.RestartEnv

var (
	quitMu   sync.Mutex
	quitHook func()
)

// SetQuit registers how the windowed program closes itself (tray icon
// removed, window closed), used by Restart.
func SetQuit(f func()) {
	quitMu.Lock()
	quitHook = f
	quitMu.Unlock()
}

// Restarted reports whether this process was started by Restart.
func Restarted() bool { return os.Getenv(restartEnv) != "" }

// SingleInstance takes a machine-wide lock so that the service and the tray
// application never run at the same time. It returns false when another
// instance holds the lock. After Restart the lock is awaited while the old
// process exits.
func SingleInstance(name string) (release func(), ok bool) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		release, ok = singleInstance(name)
		if ok || !Restarted() || time.Now().After(deadline) {
			return release, ok
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Restart runs the (already replaced) program again. A service exits with
// an error and is started again by the service manager (recovery "restart"
// set at install); a console or tray program starts its new copy with the
// same arguments and closes.
func Restart() error {
	if IsService() {
		log.Printf("Перезапуск службы: Windows запустит её снова через 30 секунд")
		go func() {
			time.Sleep(time.Second) // let the log and the web answer go out
			os.Exit(1)
		}()
		return nil
	}
	exe, err := update.Executable() // where the new version now is
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), restartEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Printf("Перезапуск: запущена новая версия (процесс %d)", cmd.Process.Pid)
	quitMu.Lock()
	quit := quitHook
	quitMu.Unlock()
	go func() {
		time.Sleep(time.Second)
		if quit != nil {
			quit()
			time.Sleep(15 * time.Second) // the window did not close: stop anyway
		}
		os.Exit(0)
	}()
	return nil
}
