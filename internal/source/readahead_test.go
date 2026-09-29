package source

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// flaky fails reads that start at or after failAt.
type flaky struct {
	*bytes.Reader
	failAt int64
}

func (f flaky) ReadAt(p []byte, off int64) (int, error) {
	if f.failAt >= 0 && off >= f.failAt {
		return 0, errors.New("network down")
	}
	return f.Reader.ReadAt(p, off)
}

func TestReadAhead(t *testing.T) {
	data := make([]byte, 5*readAheadChunk+12345)
	rand.Read(data)
	src := flaky{bytes.NewReader(data), -1}

	// whole file through small reads, as the upload does
	r := newReadAhead(src, nopCloser{}, int64(len(data)))
	got, err := io.ReadAll(struct{ io.Reader }{r})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read all: err %v, equal %v", err, bytes.Equal(got, data))
	}

	// resume: skip the first part, then read the rest
	r = newReadAhead(src, nopCloser{}, int64(len(data)))
	off := int64(2*readAheadChunk + 777)
	if _, err := io.CopyN(io.Discard, r, off); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(r)
	if !bytes.Equal(rest, data[off:]) {
		t.Fatal("resume: wrong data")
	}

	// seek back and forth
	if _, err := r.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 50)
	io.ReadFull(r, b)
	if !bytes.Equal(b, data[100:150]) {
		t.Fatal("seek: wrong data")
	}

	// the file is shorter than its size said: the data there is, then EOF
	short := data[:readAheadChunk+10]
	r = newReadAhead(flaky{bytes.NewReader(short), -1}, nopCloser{}, int64(len(data)))
	got, err = io.ReadAll(r)
	if err != nil || !bytes.Equal(got, short) {
		t.Fatalf("short file: err %v, got %d bytes", err, len(got))
	}

	// a failing read is reported, not turned into a short file
	r = newReadAhead(flaky{bytes.NewReader(data), 2 * readAheadChunk}, nopCloser{}, int64(len(data)))
	got, err = io.ReadAll(r)
	if err == nil || len(got) != 2*readAheadChunk {
		t.Fatalf("failing read: err %v, got %d bytes", err, len(got))
	}
}

func TestReadBlock(t *testing.T) {
	defer SetReadBlock(0)
	for _, c := range []struct{ set, want int64 }{
		{0, DefaultReadBlock},
		{256 << 10, 256 << 10},
		{1, MinReadBlock},
		{1 << 30, MaxReadBlock},
	} {
		SetReadBlock(c.set)
		if got := ReadBlock(); got != c.want {
			t.Errorf("SetReadBlock(%d): %d, want %d", c.set, got, c.want)
		}
	}

	// a small block reads the file the same
	SetReadBlock(MinReadBlock)
	data := make([]byte, 7*MinReadBlock+99)
	rand.Read(data)
	r := newReadAhead(bytes.NewReader(data), nopCloser{}, int64(len(data)))
	if r.chunk != MinReadBlock {
		t.Fatalf("chunk %d", r.chunk)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("small block: err %v, equal %v", err, bytes.Equal(got, data))
	}
}
