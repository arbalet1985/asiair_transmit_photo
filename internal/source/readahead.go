package source

import (
	"errors"
	"fmt"
	"io"
)

// Reading a file over SMB one small request at a time is limited by the
// network round trip: with 32 KB per request and the 30–60 ms of a Wi-Fi
// link that is well under 1 MB/s. readAhead keeps several large reads in
// flight and hands their data out in order.
const (
	readAheadChunk = 1 << 20 // bytes per SMB read
	readAheadDepth = 6       // reads in flight
)

type chunkResult struct {
	b   []byte
	err error
}

type readAhead struct {
	f     io.ReaderAt
	c     io.Closer
	size  int64
	pos   int64              // offset of the next byte handed out
	next  int64              // offset of the next read to start
	queue []chan chunkResult // reads in flight, in order
	cur   []byte             // data handed out next
	err   error              // sticky error
}

func newReadAhead(f io.ReaderAt, c io.Closer, size int64) *readAhead {
	return &readAhead{f: f, c: c, size: size}
}

func (r *readAhead) fill() {
	for len(r.queue) < readAheadDepth && r.next < r.size {
		off, n := r.next, min(int64(readAheadChunk), r.size-r.next)
		ch := make(chan chunkResult, 1)
		go func() {
			b := make([]byte, n)
			k, err := r.f.ReadAt(b, off)
			if errors.Is(err, io.EOF) {
				err = nil // a short read ends the file below
			}
			ch <- chunkResult{b[:k], err}
		}()
		r.queue = append(r.queue, ch)
		r.next += n
	}
}

func (r *readAhead) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.cur) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.pos >= r.size {
			return 0, io.EOF
		}
		r.fill()
		res := <-r.queue[0]
		r.queue = r.queue[1:]
		want := min(int64(readAheadChunk), r.size-r.pos)
		switch {
		case res.err != nil:
			r.err = res.err
		case int64(len(res.b)) < want:
			// the file is shorter than it was: stop after this data
			r.size = r.pos + int64(len(res.b))
			r.drop()
		}
		r.cur = res.b
		r.fill()
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	r.pos += int64(n)
	return n, nil
}

// drop forgets the reads in flight (they finish into their buffered channels).
func (r *readAhead) drop() {
	r.queue = nil
	r.next = r.pos + int64(len(r.cur))
}

func (r *readAhead) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("seek: неверный whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("seek: отрицательная позиция")
	}
	if abs != r.pos {
		r.cur, r.err = nil, nil
		r.pos, r.next, r.queue = abs, abs, nil
	}
	return abs, nil
}

func (r *readAhead) Close() error {
	r.queue, r.cur = nil, nil
	return r.c.Close()
}
