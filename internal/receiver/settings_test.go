package receiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

func TestEnsureConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "receiver.yaml")

	created, err := EnsureConfig(p)
	if err != nil || !created {
		t.Fatalf("first start: %v %v", created, err)
	}
	c, err := config.LoadReceiver(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Token) < 30 || c.Token == placeholderToken {
		t.Fatalf("token not generated: %q", c.Token)
	}
	if c.DestDir != filepath.Join(dir, "photos") {
		t.Fatalf("dest dir: %s", c.DestDir)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# ") {
		t.Fatal("comments of the example were lost")
	}

	// second start: nothing changes
	if created, err := EnsureConfig(p); err != nil || created {
		t.Fatalf("second start: %v %v", created, err)
	}
	if c2, _ := config.LoadReceiver(p); c2.Token != c.Token {
		t.Fatal("token changed on second start")
	}

	// a copied example with the placeholder token gets a real token
	os.WriteFile(p, bytes.Replace(b, []byte(c.Token), []byte(placeholderToken), 1), 0o644)
	if created, err := EnsureConfig(p); err != nil || !created {
		t.Fatalf("placeholder: %v %v", created, err)
	}
	if c3, _ := config.LoadReceiver(p); c3.Token == placeholderToken || c3.Token == c.Token {
		t.Fatalf("placeholder not replaced: %q", c3.Token)
	}
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "receiver.yaml")
	if _, err := EnsureConfig(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadReceiver(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func uiCall(t *testing.T, h http.Handler, method, path string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == 200 {
		json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code
}

func TestUIConnectAndToken(t *testing.T) {
	s, _ := newTestServer(t)
	ui, api := s.uiHandler(), s.apiHandler()

	var ci connectInfo
	if uiCall(t, ui, "GET", "/ui/api/connect", nil, &ci) != 200 {
		t.Fatal("connect")
	}
	if ci.Token != s.getToken() || ci.Port != "8443" || !ci.TLS || ci.Fingerprint == "" {
		t.Fatalf("connect info: %+v", ci)
	}

	sync := func(token string) int {
		req := httptest.NewRequest("POST", "/api/sync", strings.NewReader(`{"status":{}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec.Code
	}
	old := ci.Token
	if sync(old) != 200 {
		t.Fatal("current token rejected")
	}
	var nt map[string]string
	if uiCall(t, ui, "POST", "/ui/api/token", struct{}{}, &nt) != 200 || nt["token"] == old {
		t.Fatalf("new token: %v", nt)
	}
	if sync(nt["token"]) != 200 {
		t.Fatal("new token rejected")
	}
	if sync(old) != http.StatusUnauthorized {
		t.Fatal("old token still accepted")
	}
	if c, _ := config.LoadReceiver(s.cfg.Path); c.Token != nt["token"] {
		t.Fatal("new token not saved in receiver.yaml")
	}
}

func TestUIFolders(t *testing.T) {
	s, dir := newTestServer(t)
	ui := s.uiHandler()
	base := filepath.Join(dir, "disk")
	os.MkdirAll(filepath.Join(base, "Astro"), 0o755)
	os.MkdirAll(filepath.Join(base, ".hidden"), 0o755)
	os.MkdirAll(filepath.Join(base, "$RECYCLE.BIN"), 0o755)

	var dl dirList
	if code := uiCall(t, ui, "GET", "/ui/api/dirs?path="+url.QueryEscape(base), nil, &dl); code != 200 {
		t.Fatalf("dirs: %d", code)
	}
	if len(dl.Dirs) != 1 || dl.Dirs[0].Name != "Astro" || dl.Dirs[0].Path != filepath.Join(base, "Astro") || dl.Parent != dir {
		t.Fatalf("dirs: %+v", dl)
	}
	// a folder that does not exist shows its nearest existing parent
	uiCall(t, ui, "GET", "/ui/api/dirs?path="+url.QueryEscape(filepath.Join(base, "no", "such")), nil, &dl)
	if dl.Path != base {
		t.Fatalf("missing folder resolved to %s", dl.Path)
	}
	if uiCall(t, ui, "GET", "/ui/api/dirs?path=relative", nil, nil) != 400 {
		t.Fatal("relative path accepted")
	}

	var mk map[string]string
	if uiCall(t, ui, "POST", "/ui/api/mkdir", map[string]string{"parent": base, "name": "ASIAIR"}, &mk) != 200 {
		t.Fatal("mkdir")
	}
	if st, err := os.Stat(mk["path"]); err != nil || !st.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	if uiCall(t, ui, "POST", "/ui/api/mkdir", map[string]string{"parent": base, "name": "../x"}, nil) != 400 {
		t.Fatal("bad folder name accepted")
	}

	// switch the destination and receive a file into the new folder
	newDest := filepath.Join(base, "ASIAIR", "Photos")
	if code := uiCall(t, ui, "POST", "/ui/api/destdir", map[string]string{"path": newDest}, nil); code != 200 {
		t.Fatalf("destdir: %d", code)
	}
	if c, _ := config.LoadReceiver(s.cfg.Path); c.DestDir != newDest {
		t.Fatalf("dest_dir not saved: %s", c.DestDir)
	}
	st := s.getStore()
	if st.root != newDest {
		t.Fatalf("store not switched: %s", st.root)
	}
	if err := os.MkdirAll(filepath.Join(newDest, "EMMC Images", "Light"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(newDest, "EMMC Images", "Light", "a.fit"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(newDest, "Other stuff"), 0o755)
	os.WriteFile(filepath.Join(newDest, "Other stuff", "big.iso"), []byte("x"), 0o644)
	var ust uiState
	uiCall(t, ui, "GET", "/ui/api/state", nil, &ust)
	if len(ust.Files) != 1 || ust.Files[0].Path != "EMMC Images/Light/a.fit" || ust.DestDir != newDest {
		t.Fatalf("state after switch: %+v", ust)
	}
	if uiCall(t, ui, "POST", "/ui/api/destdir", map[string]string{"path": "relative/dir"}, nil) != 400 {
		t.Fatal("relative destination accepted")
	}
}

type fakeStartup struct {
	info    StartupInfo
	actions []string
}

func (f *fakeStartup) Info() StartupInfo { return f.info }
func (f *fakeStartup) SetAutostart(on bool) error {
	f.info.Autostart = on
	f.actions = append(f.actions, "autostart")
	return nil
}
func (f *fakeStartup) InstallService() error {
	f.actions = append(f.actions, "install")
	return nil
}
func (f *fakeStartup) RemoveService() error {
	f.actions = append(f.actions, "remove")
	return nil
}

func TestUIStartup(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	var info StartupInfo
	// no control: the block is hidden (supported=false)
	if uiCall(t, ui, "GET", "/ui/api/startup", nil, &info) != 200 || info.Supported {
		t.Fatalf("without control: %+v", info)
	}
	f := &fakeStartup{info: StartupInfo{Supported: true, Mode: ModeTray, Service: "not-installed"}}
	s.Startup = f
	uiCall(t, ui, "POST", "/ui/api/startup", map[string]string{"action": "autostart-on"}, &info)
	if !info.Autostart {
		t.Fatal("autostart not switched on")
	}
	for _, a := range []string{"install-service", "remove-service", "autostart-off"} {
		if code := uiCall(t, ui, "POST", "/ui/api/startup", map[string]string{"action": a}, nil); code != 200 {
			t.Fatalf("%s: %d", a, code)
		}
	}
	if strings.Join(f.actions, ",") != "autostart,install,remove,autostart" {
		t.Fatalf("actions: %v", f.actions)
	}
	if uiCall(t, ui, "POST", "/ui/api/startup", map[string]string{"action": "format-c"}, nil) != 400 {
		t.Fatal("unknown action accepted")
	}
}
