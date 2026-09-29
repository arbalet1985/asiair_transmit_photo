package source

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

// Reading a file over SMB one small request at a time is limited by the
// network round trip: with 32 KB per request and the 30–60 ms of a Wi-Fi
// link that is well under 1 MB/s. readAhead keeps several large reads in
// flight and hands their data out in order.
//
// The block size of a read and how many reads of a file are in flight can
// be changed from the receiver page to find the fastest ones for the link;
// they apply to the files opened after that.
const (
	readAheadChunk = 1 << 20 // bytes per SMB read by default
	readAheadDepth = 6       // reads in flight by default

	MinReadBlock     = 32 << 10
	MaxReadBlock     = 4 << 20
	DefaultReadBlock = readAheadChunk

	MinReadDepth     = 1
	MaxReadDepth     = 16
	DefaultReadDepth = readAheadDepth
)

var (
	readBlock atomic.Int64
	readDepth atomic.Int32
)

// SetReadDepth sets how many reads of one file are in flight at once; 0 =
// the default. It is kept within MinReadDepth..MaxReadDepth.
func SetReadDepth(n int) {
	if n <= 0 {
		n = DefaultReadDepth
	}
	readDepth.Store(int32(min(max(n, MinReadDepth), MaxReadDepth)))
}

// ReadDepth returns how many reads of one file are in flight at once.
func ReadDepth() int {
	if n := readDepth.Load(); n > 0 {
		return int(n)
	}
	return DefaultReadDepth
}

// SetReadBlock sets the size of one read from the ASIAIR, bytes; 0 = the
// default. It is kept within MinReadBlock..MaxReadBlock.
func SetReadBlock(n int64) {
	if n <= 0 {
		n = DefaultReadBlock
	}
	readBlock.Store(min(max(n, MinReadBlock), MaxReadBlock))
}

// ReadBlock returns the size of one read from the ASIAIR, bytes.
func ReadBlock() int64 {
	if n := readBlock.Load(); n > 0 {
		return n
	}
	return DefaultReadBlock
}

type chunkResult struct {
	b   []byte
	err error
}

type readAhead struct {
	f     io.ReaderAt
	c     io.Closer
	chunk int64 // bytes per read
	depth int   // reads in flight
	size  int64
	pos   int64              // offset of the next byte handed out
	next  int64              // offset of the next read to start
	queue []chan chunkResult // reads in flight, in order
	cur   []byte             // data handed out next
	err   error              // sticky error
}

func newReadAhead(f io.ReaderAt, c io.Closer, size int64) *readAhead {
	return &readAhead{f: f, c: c, chunk: ReadBlock(), depth: ReadDepth(), size: size}
}

func (r *readAhead) fill() {
	for len(r.queue) < r.depth && r.next < r.size {
		off, n := r.next, min(r.chunk, r.size-r.next)
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
		want := min(r.chunk, r.size-r.pos)
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
