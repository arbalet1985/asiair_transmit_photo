package receiver

import (
	"path/filepath"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

func TestDiskReserve(t *testing.T) {
	tmp := t.TempDir()
	s, err := New(&config.Receiver{Token: "x", DisableTLS: true, MinFree: "0",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data")})
	if err != nil {
		t.Fatal(err)
	}
	d := s.disk()
	if d.Error != "" || d.Total == 0 || d.Full != "" || d.Reserve != 0 {
		t.Fatalf("disk: %+v", d)
	}
	if msg := s.checkSpace(1000); msg != "" {
		t.Fatalf("small file refused: %s", msg)
	}
	// a file larger than the free space: refused, and the page and the
	// agent see the disk as full until enough is freed
	if msg := s.checkSpace(int64(d.Free) + 1<<40); msg == "" {
		t.Fatal("huge file accepted")
	}
	if s.disk().Full == "" {
		t.Fatal("refused size not remembered")
	}
	if msg := s.checkSpace(1000); msg != "" || s.disk().Full != "" {
		t.Fatalf("accepted file did not clear the state: %q", msg)
	}

	if err := s.SetMinFree("abc"); err == nil {
		t.Fatal("bad value accepted")
	}
	if err := s.SetMinFree("99.99%"); err != nil {
		t.Fatal(err)
	}
	if d := s.disk(); d.Full == "" || d.MinFree != "99.99%" {
		t.Fatalf("reserve not applied: %+v", d)
	}
}
