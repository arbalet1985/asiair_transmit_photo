//go:build !windows

package firewall

const supported = false

func allowed(name, exe string) bool { return false }
func allow(name, exe string) error  { return ErrUnsupported }
