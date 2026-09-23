//go:build !windows

package svc

// SingleInstance is a no-op outside Windows.
func SingleInstance(name string) (release func(), ok bool) { return func() {}, true }
