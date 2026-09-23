package gui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// detachConsole closes the console window that Windows opens for a console
// program started by double-click or at logon. When started from an existing
// command prompt the console is shared with it and is left alone.
func detachConsole() {
	var ids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), 2)
	if n == 1 {
		procFreeConsole.Call()
	}
}
