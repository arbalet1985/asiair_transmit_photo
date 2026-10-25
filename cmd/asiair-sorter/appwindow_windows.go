package main

import (
	"log"
	"os"
	"os/exec"

	"golang.org/x/sys/windows/registry"

	"github.com/arbalet1985/asiair_transmit_photo/internal/atlas"
)

// openAppWindow opens the page at u as a window of its own (atlas.AppBrowsers:
// Edge, else another browser on Chromium) and tells which browser did it;
// "" when none is there, and then the default browser opens it in a tab.
func openAppWindow(u string) string {
	seen := map[string]bool{}
	for _, b := range atlas.AppBrowsers(os.Getenv) {
		paths := []string{b.Path}
		// where Windows itself says they are (only names that are theirs)
		if b.Exe == "msedge.exe" || b.Exe == "chrome.exe" {
			for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
				if k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+b.Exe, registry.QUERY_VALUE); err == nil {
					if p, _, err := k.GetStringValue(""); err == nil && p != "" {
						paths = append([]string{p}, paths...)
					}
					k.Close()
				}
			}
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			if st, err := os.Stat(p); err != nil || st.IsDir() {
				continue
			}
			cmd := exec.Command(p, atlas.AppArgs(u)...)
			if err := cmd.Start(); err != nil {
				log.Printf("Атлас: %s не запустился: %v", b.Name, err)
				continue
			}
			cmd.Process.Release()
			return b.Name
		}
	}
	openURL(u)
	return ""
}
