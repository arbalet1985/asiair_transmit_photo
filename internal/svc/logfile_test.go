package svc

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRotatingLog(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.log")
	SetLogPolicy(LogPolicy{Keep: 7 * 24 * time.Hour, CompressAfter: 3 * 24 * time.Hour})
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.Local) }
	now := day(1)
	r, err := openLog(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	names := func() []string {
		r.cleaning.Lock() // wait for a cleanup in progress
		r.cleaning.Unlock()
		es, _ := os.ReadDir(dir)
		var out []string
		for _, e := range es {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out
	}
	// one line a day for 12 days; each rotated day gets the time of its line
	for d := 1; d <= 12; d++ {
		now = day(d)
		r.Write([]byte("день\n"))
		if d > 1 {
			prev := filepath.Join(dir, "app-"+day(d-1).Format(dayLayout)+".log")
			os.Chtimes(prev, day(d-1), day(d-1))
		}
		r.cleanup(now)
	}
	got := strings.Join(names(), " ")
	// on day 12: days 10..11 are younger than 3 days: plain; 5..9 compressed;
	// older than 7 days: deleted
	want := "app-2026-09-05.log.gz app-2026-09-06.log.gz app-2026-09-07.log.gz app-2026-09-08.log.gz app-2026-09-09.log.gz " +
		"app-2026-09-10.log app-2026-09-11.log app.log"
	if got != want {
		t.Fatalf("files:\n%s\nwant:\n%s", got, want)
	}
	zf, _ := os.Open(filepath.Join(dir, "app-2026-09-05.log.gz"))
	zr, err := gzip.NewReader(zf)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	zf.Close()
	if string(b) != "день\n" {
		t.Fatalf("compressed content %q", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "app-2026-09-05.log.gz")); !st.ModTime().Equal(day(5)) {
		t.Fatalf("compressed file time %v", st.ModTime())
	}
	if b, _ := os.ReadFile(file); string(b) != "день\n" {
		t.Fatalf("current log %q", b)
	}
}

// A log left from an earlier day is rotated at the first line; a day too
// big for one file continues in the next one.
func TestRotatingLogReopenAndSize(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.log")
	SetLogPolicy(LogPolicy{CompressAfter: 365 * 24 * time.Hour}) // nothing compressed or deleted
	os.WriteFile(file, []byte("вчера\n"), 0o644)
	old := time.Date(2026, 9, 1, 23, 0, 0, 0, time.Local)
	os.Chtimes(file, old, old)
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.Local)
	r, err := openLog(file, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Write([]byte("сегодня\n"))
	r.cleaning.Lock() // let the cleanup started on open finish
	r.cleaning.Unlock()
	if b, _ := os.ReadFile(filepath.Join(dir, "app-2026-09-01.log")); string(b) != "вчера\n" {
		t.Fatalf("yesterday: %q", b)
	}
	r.mu.Lock()
	r.size = logMaxSize
	r.mu.Unlock()
	r.Write([]byte("ещё\n"))
	if b, _ := os.ReadFile(filepath.Join(dir, "app-2026-09-02.log")); string(b) != "сегодня\n" {
		t.Fatalf("first part of the day: %q", b)
	}
	if b, _ := os.ReadFile(file); string(b) != "ещё\n" {
		t.Fatalf("current: %q", b)
	}
	r.size = logMaxSize
	r.Write([]byte("и ещё\n"))
	if _, err := os.Stat(filepath.Join(dir, "app-2026-09-02-2.log")); err != nil {
		t.Fatal(err)
	}
}
