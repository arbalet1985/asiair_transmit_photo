package atlas

import "path/filepath"

// The atlas of the sorter in a window of its own: a browser built on
// Chromium opens a page with --app as a window without tabs and the address
// line. Edge comes with Windows; the others are tried when it is not there,
// and only then the default browser opens the page in a tab.

// AppBrowser is a browser that opens a page as a window of its own.
type AppBrowser struct {
	Name string
	Exe  string // its file in the App Paths of Windows
	Path string // where it lies
}

// AppBrowsers are the places of such browsers on Windows, Edge first;
// env reads the environment (ProgramFiles, LOCALAPPDATA…).
func AppBrowsers(env func(string) string) []AppBrowser {
	var out []AppBrowser
	add := func(name, exe string, dirs []string, rel ...string) {
		for _, d := range dirs {
			if v := env(d); v != "" {
				out = append(out, AppBrowser{name, exe, filepath.Join(append([]string{v}, rel...)...)})
			}
		}
	}
	all := []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"}
	add("Edge", "msedge.exe", all, "Microsoft", "Edge", "Application", "msedge.exe")
	add("Chrome", "chrome.exe", all, "Google", "Chrome", "Application", "chrome.exe")
	add("Яндекс Браузер", "browser.exe", []string{"LOCALAPPDATA", "ProgramFiles"}, "Yandex", "YandexBrowser", "Application", "browser.exe")
	add("Brave", "brave.exe", all, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")
	add("Vivaldi", "vivaldi.exe", []string{"LOCALAPPDATA", "ProgramFiles"}, "Vivaldi", "Application", "vivaldi.exe")
	return out
}

// AppArgs are the arguments that open the page at u as a window.
func AppArgs(u string) []string {
	return []string{"--app=" + u, "--window-size=1400,900", "--new-window"}
}
