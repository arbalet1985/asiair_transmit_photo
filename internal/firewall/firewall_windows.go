package firewall

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

const supported = true

func netsh(args ...string) (string, error) {
	cmd := exec.Command("netsh", append([]string{"advfirewall", "firewall"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func allowed(name, exe string) bool {
	if _, err := netsh("show", "rule", "name="+name); err == nil {
		return true
	}
	// a rule Windows added itself names the program path
	out, err := netsh("show", "rule", "name=all", "dir=in", "verbose")
	return err == nil && strings.Contains(strings.ToLower(out), strings.ToLower(exe))
}

func allow(name, exe string) error {
	netsh("delete", "rule", "name="+name)
	out, err := netsh("add", "rule", "name="+name, "dir=in", "action=allow", "program="+exe, "enable=yes", "profile=any")
	if err != nil {
		return fmt.Errorf("правило брандмауэра не добавлено (нужны права администратора): %s", strings.TrimSpace(out))
	}
	return nil
}
