package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// A new version must prove itself: until it has talked to the receiver the
// old program stays next to it, and a watchdog started from the old program
// puts it back if the new one does not start or does not connect in time.

// WatchCommand is the command line word that runs the watchdog.
const WatchCommand = "update-watch"

// RestartEnv is set for a program started again after an update or a
// rollback (svc.Restarted reads it): the window comes back to the tray.
const RestartEnv = "ASIAIR_RESTARTED"

// Pending describes an update in progress; it lives next to the program.
type Pending struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Deadline time.Time `json:"deadline"`
	Service  bool      `json:"service"`        // runs as a service: the service manager starts it again
	Args     []string  `json:"args,omitempty"` // command line to start the old version again
	PID      int       `json:"pid,omitempty"`  // the new version, once it runs
	OK       bool      `json:"ok,omitempty"`   // the new version works
}

// Rollback records a version that was put back.
type Rollback struct {
	Version string    `json:"version"` // the version that failed
	From    string    `json:"from"`    // the version restored
	Reason  string    `json:"reason"`
	Time    time.Time `json:"time"`
}

// Backup is where Install keeps the previous program. It stays an ".exe",
// so the watchdog can be started from it.
func Backup(exe string) string {
	if runtime.GOOS == "windows" || strings.EqualFold(filepath.Ext(exe), ".exe") {
		return strings.TrimSuffix(exe, filepath.Ext(exe)) + ".old" + filepath.Ext(exe)
	}
	return exe + ".old"
}

func pendingFile(exe string) string  { return exe + ".update.json" }

// WatchLog is the log of the watchdog of the updates of exe.
func WatchLog(exe string) string {
	return filepath.Join(filepath.Dir(exe), strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))+"-update.log")
}
func rollbackFile(exe string) string { return exe + ".rollback.json" }

func writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	os.Remove(name)
	return os.Rename(tmp, name)
}

func readJSON(name string, v any) error {
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ReadPending returns the update in progress, or nil.
func ReadPending(exe string) *Pending {
	var p Pending
	if readJSON(pendingFile(exe), &p) != nil {
		return nil
	}
	return &p
}

// ReadRollback returns the last rollback, or nil.
func ReadRollback(exe string) *Rollback {
	var r Rollback
	if readJSON(rollbackFile(exe), &r) != nil {
		return nil
	}
	return &r
}

// StartWatch records the update and starts the watchdog from the backup of
// the old program. Call it after Install and before the restart.
func StartWatch(exe string, p Pending) error {
	if err := writeJSON(pendingFile(exe), p); err != nil {
		return err
	}
	cmd := exec.Command(Backup(exe), WatchCommand, exe)
	cmd.Dir = filepath.Dir(exe)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(pendingFile(exe))
		return err
	}
	cmd.Process.Release()
	return nil
}

// Undo cancels an installed update that could not be started: the watchdog
// stops, the old program goes back in place.
func Undo(exe string) error {
	os.Remove(pendingFile(exe))
	return rollBack(exe, 0)
}

// Started tells the watchdog that the new version runs. It reports whether
// an update of this program to version is being watched.
func Started(exe, version string) bool {
	p := ReadPending(exe)
	if p == nil || p.To != version {
		return false
	}
	p.PID = os.Getpid()
	writeJSON(pendingFile(exe), p)
	return true
}

// Healthy tells the watchdog that the new version works: the old program
// is no longer needed.
func Healthy(exe, version string) {
	p := ReadPending(exe)
	if p == nil || p.To != version {
		return
	}
	p.OK = true
	writeJSON(pendingFile(exe), p)
	os.Remove(rollbackFile(exe))
}

// Watch is the watchdog: it waits for the new version to report that it
// works and otherwise puts the old program back and starts it.
func Watch(exe string) error {
	lf, err := os.OpenFile(WatchLog(exe),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer lf.Close()
		log.SetOutput(lf)
		log.SetFlags(log.LstdFlags)
	}
	p := ReadPending(exe)
	if p == nil {
		return errors.New("нет обновления в процессе")
	}
	log.Printf("Обновление %s → %s: жду, пока новая версия выйдет на связь (до %s)", p.From, p.To, p.Deadline.Format("15:04:05"))
	pid := 0
	for time.Now().Before(p.Deadline) {
		time.Sleep(watchPoll)
		cur := ReadPending(exe)
		if cur == nil {
			return nil // handled elsewhere
		}
		if cur.OK {
			os.Remove(pendingFile(exe))
			log.Printf("Обновление %s → %s: новая версия работает", p.From, p.To)
			return nil
		}
		pid = cur.PID
	}
	reason := "новая версия не запустилась"
	if pid != 0 {
		reason = "новая версия не вышла на связь с приёмником"
	}
	log.Printf("Обновление %s → %s: %s — возвращаю %s", p.From, p.To, reason, p.From)
	if err := rollBack(exe, pid); err != nil {
		log.Printf("Откат не удался: %v", err)
		return err
	}
	writeJSON(rollbackFile(exe), Rollback{Version: p.To, From: p.From, Reason: reason, Time: time.Now()})
	os.Remove(pendingFile(exe))
	if p.Service {
		// the service manager starts the service again after the stop; in
		// case it gave up, ask it once more (fails harmlessly if running)
		time.Sleep(serviceRetry)
		out, err := exec.Command(exe, "start").CombinedOutput()
		log.Printf("Запуск службы: %v %s", err, strings.TrimSpace(string(out)))
		return nil
	}
	cmd := exec.Command(exe, p.Args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(os.Environ(), RestartEnv+"=1")
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить %s: %w", exe, err)
	}
	log.Printf("Запущена версия %s (процесс %d)", p.From, cmd.Process.Pid)
	cmd.Process.Release()
	return nil
}

// timings (tests shorten them)
var (
	watchPoll    = 2 * time.Second
	serviceRetry = 45 * time.Second
	renameWait   = 20 * time.Second
)

// rollBack stops the new version and puts the backup in its place.
func rollBack(exe string, pid int) error {
	if pid != 0 && pid != os.Getpid() {
		if pr, err := os.FindProcess(pid); err == nil {
			pr.Kill()
		}
	}
	// the stopped program may keep its file for a moment
	deadline := time.Now().Add(renameWait)
	for {
		err := os.Rename(exe, exe+".bad")
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := os.Rename(Backup(exe), exe); err != nil {
		os.Rename(exe+".bad", exe)
		return err
	}
	return nil
}
