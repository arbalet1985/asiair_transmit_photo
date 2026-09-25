//go:build !windows

package update

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInstall lays out an updated program: exe is the new version, the
// backup the old one; the old one, when started, writes its arguments and
// RestartEnv to marker.
func fakeInstall(t *testing.T) (exe, marker string) {
	t.Helper()
	watchPoll, renameWait = 20*time.Millisecond, 2*time.Second
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	exe = filepath.Join(dir, "asiair-agent")
	marker = filepath.Join(dir, "marker")
	os.WriteFile(exe, []byte("#!/bin/sh\necho new\n"), 0o755)
	os.WriteFile(Backup(exe), []byte("#!/bin/sh\necho \"old $* $"+RestartEnv+"\" > "+marker+"\n"), 0o755)
	return exe, marker
}

func pending(exe string, d time.Duration) {
	writeJSON(pendingFile(exe), Pending{From: "1.0.0", To: "1.1.0", Deadline: time.Now().Add(d), Args: []string{"gui"}})
}

func waitFile(t *testing.T, name string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(name); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s not written", name)
	return ""
}

func TestWatchNewVersionWorks(t *testing.T) {
	exe, marker := fakeInstall(t)
	pending(exe, 5*time.Second)
	go func() {
		time.Sleep(100 * time.Millisecond)
		if !Started(exe, "1.1.0") {
			t.Error("Started: update not found")
		}
		Healthy(exe, "1.1.0")
	}()
	if err := Watch(exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "new") {
		t.Fatal("new version replaced")
	}
	if ReadPending(exe) != nil || ReadRollback(exe) != nil {
		t.Fatal("pending or rollback left")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("old version started")
	}
	// the old copy is removed only now
	Cleanup(exe)
	if _, err := os.Stat(Backup(exe)); err == nil {
		t.Fatal("backup not cleaned up")
	}
}

func TestWatchNewVersionDoesNotStart(t *testing.T) {
	exe, marker := fakeInstall(t)
	pending(exe, 200*time.Millisecond)
	if err := Watch(exe); err != nil {
		t.Fatal(err)
	}
	if got := waitFile(t, marker); strings.TrimSpace(got) != "old gui 1" {
		t.Fatalf("old version started with %q", got)
	}
	if b, _ := os.ReadFile(exe + ".bad"); !strings.Contains(string(b), "new") {
		t.Fatal("new version not kept as .bad")
	}
	rb := ReadRollback(exe)
	if rb == nil || rb.Version != "1.1.0" || rb.From != "1.0.0" || !strings.Contains(rb.Reason, "не запустилась") {
		t.Fatalf("rollback %+v", rb)
	}
	if ReadPending(exe) != nil {
		t.Fatal("pending left")
	}
}

func TestWatchNewVersionHangs(t *testing.T) {
	exe, marker := fakeInstall(t)
	pending(exe, 300*time.Millisecond)
	hung := exec.Command("sleep", "60") // runs but never reports to the receiver
	if err := hung.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- hung.Wait() }()
	p := ReadPending(exe)
	p.PID = hung.Process.Pid
	writeJSON(pendingFile(exe), p)

	if err := Watch(exe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		hung.Process.Kill()
		t.Fatal("hung version not stopped")
	}
	waitFile(t, marker)
	if rb := ReadRollback(exe); rb == nil || !strings.Contains(rb.Reason, "не вышла на связь") {
		t.Fatalf("rollback %+v", rb)
	}
}

// A restart that fails at once puts the old program back without waiting.
func TestUndo(t *testing.T) {
	exe, _ := fakeInstall(t)
	pending(exe, time.Hour)
	if err := Undo(exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "old") {
		t.Fatal("old program not back")
	}
	if ReadPending(exe) != nil {
		t.Fatal("watchdog not stopped")
	}
}
