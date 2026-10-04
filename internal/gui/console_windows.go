package gui

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// freedStdio keeps the standard files of the freed console reachable. Their
// handles are closed by FreeConsole and Windows hands the same numbers to
// the next objects (the single-instance lock among them); if the files were
// garbage collected, their finalizers would close those objects instead.
var freedStdio []*os.File

// DetachConsole closes the console window that Windows opens for a console
// program started by double-click or at logon. When started from an existing
// command prompt the console is shared with it and is left alone.
func DetachConsole() {
	var ids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), 2)
	if n == 1 {
		procFreeConsole.Call()
		// the standard handles now point at the freed console: a program
		// started from here (restart after an update) must not inherit them
		if f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
			freedStdio = []*os.File{os.Stdin, os.Stdout, os.Stderr}
			os.Stdin, os.Stdout, os.Stderr = f, f, f
		}
	}
}

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

// openURL opens a web address in the default browser.
func openURL(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	u, _ := windows.UTF16PtrFromString(url)
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(u)), 0, 0, 1)
}
