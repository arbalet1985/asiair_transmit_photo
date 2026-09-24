//go:build !windows

package svc

// SingleInstance is a no-op outside Windows.
func singleInstance(name string) (release func(), ok bool) { return func() {}, true }
