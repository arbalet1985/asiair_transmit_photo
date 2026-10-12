package receiver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// TestSorterUpdate: the receiver downloads the sorter of a new release and
// a sorter connected to it finds it newer and downloads it.
func TestSorterUpdate(t *testing.T) {
	oldV, oldB := protocol.Version, protocol.Build
	defer func() { protocol.Version, protocol.Build = oldV, oldB }()
	protocol.Version, protocol.Build = "1.1.0", "aaaa"

	newExe := []byte("MZ asiair-sorter 1.2.0")
	sum := sha256.Sum256(newExe)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v1.2.0","html_url":"https://example/rel","assets":[
				{"name":"asiair-sorter.exe","size":%d,"digest":"sha256:%s","url":"%s/s","browser_download_url":"%s/s"},
				{"name":"builds.json","size":2,"url":"%s/b","browser_download_url":"%s/b"}]}`,
				len(newExe), hex.EncodeToString(sum[:]), gh.URL, gh.URL, gh.URL, gh.URL)
		case "/s":
			w.Write(newExe)
		case "/b":
			fmt.Fprint(w, `{"asiair-sorter.exe":"bbbb"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	s, _ := newTestServer(t)
	s.UpdateAPI = gh.URL
	s.updater.repo = "o/r"
	ui := httptest.NewServer(s.uiHandler())
	defer ui.Close()
	ctx := context.Background()
	r, err := sorter.NewReceiver(ui.URL+"/", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// nothing kept yet
	if o, err := r.SorterUpdate(ctx); err != nil || o.Version != "" || sorter.NewSorter(o) {
		t.Fatalf("before: %+v %v", o, err)
	}

	s.updater.check(ctx, false, false)
	if in := s.updater.info(); in.Sorter != "1.2.0" {
		t.Fatalf("sorter not downloaded: %+v", in)
	}
	o, err := r.SorterUpdate(ctx)
	if err != nil || o.Version != "1.2.0" || o.Build != "bbbb" || o.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("offer: %+v %v", o, err)
	}
	if !sorter.NewSorter(o) {
		t.Fatal("newer sorter not offered")
	}
	dst := filepath.Join(t.TempDir(), "asiair-sorter.exe.new")
	var got int64
	if err := r.DownloadSorter(ctx, o, dst, func(done, total int64) { got = done }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(newExe) || got != int64(len(newExe)) {
		t.Fatalf("downloaded %q (%d bytes counted)", b, got)
	}
	// a wrong checksum is refused
	bad := o
	bad.SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := r.DownloadSorter(ctx, bad, dst+"2", nil); err == nil {
		t.Fatal("wrong checksum accepted")
	}

	// the same code under the new number, an older version, a dev build: no update
	same := o
	same.Build = protocol.Build
	older := o
	older.Version = "1.0.9"
	for _, c := range []protocol.UpdateOffer{same, older, {}} {
		if sorter.NewSorter(c) {
			t.Fatalf("offered: %+v", c)
		}
	}
	protocol.Version = "dev"
	if sorter.NewSorter(o) {
		t.Fatal("a dev build offered an update")
	}
}
