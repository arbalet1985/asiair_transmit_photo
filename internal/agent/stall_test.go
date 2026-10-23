package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

// stallSource stops the first file opened after stallAt bytes, until it is
// closed, and counts what each opened file gave.
type stallSource struct {
	source.Source
	stallAt int64
	mu      sync.Mutex
	opened  []*stallFile
}

type stallFile struct {
	io.ReadSeekCloser
	stallAt int64 // 0: never stops
	n       atomic.Int64
	closed  chan struct{}
	once    sync.Once
}

func (s *stallSource) Open(p string) (io.ReadSeekCloser, error) {
	f, err := s.Source.Open(p)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sf := &stallFile{ReadSeekCloser: f, closed: make(chan struct{})}
	if len(s.opened) == 0 {
		sf.stallAt = s.stallAt
	}
	s.opened = append(s.opened, sf)
	return sf, nil
}

func (f *stallFile) Read(p []byte) (int, error) {
	if f.stallAt > 0 {
		left := f.stallAt - f.n.Load()
		if left <= 0 {
			<-f.closed
			return 0, io.ErrClosedPipe
		}
		p = p[:min(int64(len(p)), left)]
	}
	n, err := f.ReadSeekCloser.Read(p)
	f.n.Add(int64(n))
	return n, err
}

func (f *stallFile) Close() error {
	f.once.Do(func() { close(f.closed) })
	return f.ReadSeekCloser.Close()
}

// TestStallResume: an upload stuck on the way is ended by the watchdog and
// resumed at once where the receiver stopped, without reading the start of
// the file again; the file arrives whole.
func TestStallResume(t *testing.T) {
	stallWindow = 600 * time.Millisecond
	defer func() { stallWindow = time.Minute }()
	tmp := t.TempDir()
	asiair := filepath.Join(tmp, "EMMC Images")
	data := writeRandom(t, filepath.Join(asiair, "Light", "a.fit"), 3*markStep+12345)

	rcfg := &config.Receiver{Listen: freeAddr(t), UIListen: freeAddr(t), Token: "secret",
		DestDir: filepath.Join(tmp, "dest"), DataDir: filepath.Join(tmp, "data")}
	srv, err := receiver.New(rcfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background(t, func() { srv.Run(ctx) })

	a, err := New(&config.Agent{ASIAIR: "dir:" + asiair, ReceiverURL: "https://" + rcfg.Listen, Token: "secret",
		ReceiverFingerprint: srv.Fingerprint(), StateFile: filepath.Join(tmp, "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	var files []source.File
	a.source().Walk(ctx, nil, func(f source.File) error { files = append(files, f); return nil })
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	f := files[0]
	src := &stallSource{Source: a.source(), stallAt: markStep + markStep/2}
	a.src = src

	logs := &syncBuffer{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	defer log.SetOutput(os.Stderr)

	uctx, ucancel := context.WithCancel(ctx)
	defer ucancel()
	u := &upload{f: f, ctx: uctx, cancel: ucancel}
	waitFor(t, "receiver", 5*time.Second, func() bool { return CheckReceiver(ctx, a.cfg.ReceiverURL, "secret", srv.Fingerprint(), nil) == nil })
	if err := a.upload(u); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(rcfg.DestDir, "EMMC Images", "Light", "a.fit"))
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("received file differs")
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.opened) != 2 {
		t.Fatalf("opened %d times, want 2", len(src.opened))
	}
	if n := src.opened[1].n.Load(); n != int64(len(data))-markStep {
		t.Fatalf("the second try read %d bytes, want %d (from the mark at %d)", n, int64(len(data))-markStep, markStep)
	}
	if l := logs.String(); !strings.Contains(l, "передача зависла") || !strings.Contains(l, "без перечитывания начала") {
		t.Fatalf("log:\n%s", l)
	}
}

// TestResumeHashMarks: the checksum restored from a mark goes on to the
// checksum of the whole file.
func TestResumeHashMarks(t *testing.T) {
	a := &Agent{marks: map[string]*hashMarks{}}
	f := source.File{Path: "EMMC Images/a.fit", Size: 5*markStep + 7, ModTime: time.Unix(1, 0)}
	data := bytes.Repeat([]byte("asiair"), int(f.Size)/6+1)[:f.Size]
	w := newMarkHash(sha256.New(), 0, func(pos int64, hh hash.Hash) { a.saveMark(f, pos, hh) })
	w.Write(data[:3*markStep+100])
	for _, off := range []int64{3*markStep + 100, 2 * markStep, markStep + 1} {
		h2 := sha256.New()
		pos := a.resumeHash(f, off, h2)
		if pos != off/markStep*markStep {
			t.Fatalf("offset %d: mark %d", off, pos)
		}
		h2.Write(data[pos:])
		if !bytes.Equal(h2.Sum(nil), sha256Sum(data)) {
			t.Fatalf("offset %d: checksum differs", off)
		}
	}
	// another version of the file: no mark
	f2 := f
	f2.Size++
	if pos := a.resumeHash(f2, 2*markStep, sha256.New()); pos != 0 {
		t.Fatal("mark of another version used")
	}
}

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// syncBuffer collects the log, written from many goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
