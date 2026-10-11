package svc

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
)

var crashCaught atomic.Bool // CatchCrashes is in effect: the log rotation leaves it

// CrashFile is where CatchCrashes keeps the report of the running program;
// PrevCrashFile holds the report of the run before, if it crashed.
func CrashFile(app string) string     { return filepath.Join(exeDir(), app+".crash") }
func PrevCrashFile(app string) string { return CrashFile(app) + ".prev" }

// CatchCrashes makes the Go runtime write the reason of a crash (a panic
// in any goroutine, a fatal error) to CrashFile, in every mode: the next
// run finds it in PrevCrashFile and can report it.
func CatchCrashes(app string) {
	file := CrashFile(app)
	if st, err := os.Stat(file); err == nil && st.Size() > 0 {
		os.Remove(file + ".prev")
		os.Rename(file, file+".prev")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Printf("Файл отчёта о сбое %s: %v", file, err)
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return
	}
	f.Close() // SetCrashOutput keeps its own copy
	crashCaught.Store(true)
}
