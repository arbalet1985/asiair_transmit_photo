package svc

import (
	"golang.org/x/sys/windows"
)

// SingleInstance takes a machine-wide lock so that the service and the tray
// application never run the agent at the same time. It returns false when
// another instance holds the lock.
func SingleInstance(name string) (release func(), ok bool) {
	n, err := windows.UTF16PtrFromString(`Global\` + name)
	if err != nil {
		return func() {}, true
	}
	h, err := windows.CreateMutex(nil, false, n)
	if err != nil {
		// ERROR_ALREADY_EXISTS, or ERROR_ACCESS_DENIED when the mutex
		// belongs to the service running as another user
		if h != 0 {
			windows.CloseHandle(h)
		}
		return func() {}, false
	}
	return func() { windows.CloseHandle(h) }, true
}
