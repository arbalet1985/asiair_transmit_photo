// Package firewall adds an inbound rule for the program to the Windows
// firewall, so Windows does not ask "allow network access?" and a service
// (for which Windows cannot ask) is not blocked silently. Adding a rule
// needs administrator rights; checking does not.
package firewall

import "errors"

// ErrUnsupported: there is no Windows firewall on this system.
var ErrUnsupported = errors.New("брандмауэр Windows есть только в Windows")

// Supported reports whether rules can be managed here.
func Supported() bool { return supported }

// Allowed reports whether an inbound allow rule for exe exists: the one
// added by Allow, or one Windows created when the user answered its question.
func Allowed(name, exe string) bool { return allowed(name, exe) }

// Allow adds (or replaces) the inbound rule "name" for the program exe.
func Allow(name, exe string) error { return allow(name, exe) }
