package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func TestPerfRecord(t *testing.T) {
	p := &perf{lines: make(chan string, 4)}
	next := func() protocol.PerfRecord {
		var r protocol.PerfRecord
		if err := json.Unmarshal([]byte(<-p.lines), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	fp := p.file()
	fp.add(actCheck, 0, 120*time.Millisecond)
	fp.add(actRead, 8<<20, time.Second)
	fp.add(actNet, 4<<20, 4*time.Second)
	fp.done("EMMC Images/a.fit", 8<<20, 5*time.Second, 2, nil)
	r := next()
	if r.Kind != "file" || r.Path != "EMMC Images/a.fit" || r.Bytes != 8<<20 || r.MS != 5000 || r.Streams != 2 || r.Error != "" {
		t.Fatalf("record: %+v", r)
	}
	if a := r.Acts["read"]; a.MS != 1000 || a.Bytes != 8<<20 || a.N != 0 {
		t.Fatalf("read: %+v", a)
	}
	if a := r.Acts["check"]; a.MS != 120 || a.N != 1 || a.MaxMS != 120 {
		t.Fatalf("check: %+v", a)
	}
	if _, ok := r.Acts["zstd"]; ok || len(r.Acts) != 3 {
		t.Fatalf("an action not done is written: %+v", r.Acts)
	}
	// the minute has the counts of the file, a failure is told
	if ns, _, _, _ := p.minute.take(); ns[actRead] != int64(time.Second) || p.files.Load() != 1 {
		t.Fatalf("minute: %v, files %d", ns[actRead], p.files.Load())
	}
	fp = p.file()
	fp.add(actCommit, 0, time.Second)
	fp.add(actCommit, 0, 3*time.Second)
	fp.done("b.fit", 0, time.Second, 1, errors.New("обрыв"))
	if r := next(); r.Acts["commit"] != (protocol.PerfAct{MS: 4000, N: 2, MaxMS: 3000}) || r.Error != "обрыв" || p.files.Load() != 1 {
		t.Fatalf("failed: %+v", r)
	}
}

// TestPerfLimit: the time inside a body read that is not reading or hashing
// is the rate limit.
func TestPerfLimit(t *testing.T) {
	p := &perf{lines: make(chan string, 1)}
	fp := p.file()
	fp.last.Store(time.Now().UnixNano())
	b := &timedBody{d: &diag{}, fp: fp, r: readerFunc(func(q []byte) (int, error) {
		fp.add(actRead, int64(len(q)), 30*time.Millisecond) // as if timed inside
		time.Sleep(50 * time.Millisecond)
		return len(q), nil
	})}
	b.Read(make([]byte, 100))
	ns, bytes, _, _ := fp.c.take()
	if l := time.Duration(ns[actLimit]); l < 15*time.Millisecond || l > 40*time.Millisecond {
		t.Fatalf("limit %v", l)
	}
	if bytes[actNet] != 100 {
		t.Fatalf("net bytes %d", bytes[actNet])
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
