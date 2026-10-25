package atlas

import (
	"path/filepath"
	"testing"
)

func TestAppBrowsers(t *testing.T) {
	env := map[string]string{"ProgramFiles(x86)": `C:\PF86`, "LOCALAPPDATA": `C:\U\AppData\Local`}
	l := AppBrowsers(func(k string) string { return env[k] })
	if len(l) == 0 || l[0].Name != "Edge" || l[0].Path != filepath.Join(`C:\PF86`, "Microsoft", "Edge", "Application", "msedge.exe") {
		t.Fatalf("Edge first: %+v", l)
	}
	for _, b := range l {
		if b.Exe == "" || filepath.Base(b.Path) != b.Exe {
			t.Errorf("%+v", b)
		}
	}
	if a := AppArgs("http://127.0.0.1:5/?t=x"); a[0] != "--app=http://127.0.0.1:5/?t=x" {
		t.Fatal(a)
	}
}
