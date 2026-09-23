package receiver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCleanPath(t *testing.T) {
	good := map[string]string{
		"EMMC Images/Autorun/a.fit": "EMMC Images/Autorun/a.fit",
		`EMMC Images\Autorun\a.fit`: "EMMC Images/Autorun/a.fit",
		"/EMMC Images/a.fit/":       "EMMC Images/a.fit",
	}
	for in, want := range good {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a", "a/../b", "a/./b", "C:/x", "a//b", "a/b:c", ".partial/x", "a/\x00b"} {
		if _, err := CleanPath(bad); err == nil {
			t.Errorf("CleanPath(%q): expected error", bad)
		}
	}
}

func TestStoreResumeAndCommit(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("0123456789"), 1000)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	p := "EMMC Images/Light/a.fit"
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	size := int64(len(data))

	// interrupted upload: only the first 3000 bytes arrive
	got, _, err := s.Write(p, size, mt, 0, bytes.NewReader(data[:3000]))
	if got != 3000 || err == nil {
		t.Fatalf("partial write: %d %v", got, err)
	}
	if off, done := s.Offset(p, size, mt); off != 3000 || done {
		t.Fatalf("offset %d %v", off, done)
	}
	// a wrong offset is refused
	var eo *ErrOffset
	if _, _, err := s.Write(p, size, mt, 100, bytes.NewReader(data[100:])); !errors.As(err, &eo) || eo.Have != 3000 {
		t.Fatalf("expected offset error, got %v", err)
	}
	// resume
	got, gotSum, err := s.Write(p, size, mt, 3000, bytes.NewReader(data[3000:]))
	if err != nil || got != size || gotSum != hexSum {
		t.Fatalf("resume: %d %q %v", got, gotSum, err)
	}
	if err := s.Commit(p, size, mt, "00"+hexSum[2:]); !errors.Is(err, ErrChecksum) {
		t.Fatalf("bad checksum accepted: %v", err)
	}
	// after a checksum failure the upload starts over
	if off, _ := s.Offset(p, size, mt); off != 0 {
		t.Fatalf("offset after checksum failure: %d", off)
	}
	s.Write(p, size, mt, 0, bytes.NewReader(data))
	if err := s.Commit(p, size, mt, hexSum); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Local(p))
	if err != nil || !bytes.Equal(b, data) {
		t.Fatalf("final file differs: %v", err)
	}
	if off, done := s.Offset(p, size, mt); off != size || !done {
		t.Fatalf("complete not detected: %d %v", off, done)
	}
	// a different version of the file (new mtime) is not "complete"
	if _, done := s.Offset(p, size, mt.Add(time.Hour)); done {
		t.Fatal("new version reported complete")
	}
	files, _ := s.List()
	if len(files) != 1 || files[0].Path != p {
		t.Fatalf("list: %+v", files)
	}
	if err := s.Remove(p); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.List(); len(files) != 0 {
		t.Fatalf("not removed: %+v", files)
	}
}
