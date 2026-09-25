package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.1.0", true},
		{"v1.10.0", "1.9.9", true},
		{"1.1.0", "1.1.0", false},
		{"1.0.9", "1.1.0", false},
		{"2.0.0", "1.99.99", true},
		{"1.2.0", "1.2.0-beta.1", true},
		{"1.2.0-beta.1", "1.2.0", false},
		{"1.2.0-beta.2", "1.2.0-beta.1", true},
		{"1.2.0", "dev", false},
		{"dev", "1.0.0", false},
		{"1.2", "1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestLatestAndDownload(t *testing.T) {
	exe := []byte("MZ new program")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v1.3.0","html_url":"https://x/rel","assets":[
				{"name":"asiair-agent.exe","size":%d,"digest":"sha256:%s","url":"%s/assets/1","browser_download_url":"https://nowhere/a.exe"}]}`,
				len(exe), sum(exe), srv.URL)
		case "/assets/1":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("accept %q", r.Header.Get("Accept"))
			}
			w.Write(exe)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{Repo: "o/r", Token: "tok", API: srv.URL}
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.3.0" || rel.Assets[AgentAsset].SHA256 != sum(exe) {
		t.Fatalf("release: %+v", rel)
	}
	dst := filepath.Join(t.TempDir(), "a.exe")
	got, err := c.Download(context.Background(), rel.Assets[AgentAsset], dst)
	if err != nil || got != sum(exe) {
		t.Fatalf("download: %q %v", got, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(exe) {
		t.Fatal("content")
	}

	// a wrong checksum is refused and leaves nothing behind
	bad := rel.Assets[AgentAsset]
	bad.SHA256 = strings.Repeat("0", 64)
	dst2 := filepath.Join(t.TempDir(), "b.exe")
	if _, err := c.Download(context.Background(), bad, dst2); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := os.Stat(dst2); err == nil {
		t.Fatal("bad file kept")
	}

	// no token: a private repository answers 404
	if _, err := (&Client{Repo: "o/r", API: srv.URL}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "токен") {
		t.Fatalf("no token: %v", err)
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "prog.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	nw := exe + ".new"
	os.WriteFile(nw, []byte("new"), 0o755)
	if err := Install(exe, nw); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatal("not replaced")
	}
	if b, _ := os.ReadFile(Backup(exe)); string(b) != "old" {
		t.Fatal("old copy missing")
	}
	Cleanup(exe)
	if _, err := os.Stat(Backup(exe)); err == nil {
		t.Fatal("old copy not removed")
	}
	// a missing new file: the program stays in place
	if err := Install(exe, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatal("rollback failed")
	}
}
