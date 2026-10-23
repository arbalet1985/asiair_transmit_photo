package agent

import (
	"encoding"
	"hash"
	"sort"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
	"github.com/arbalet1985/asiair_transmit_photo/internal/wire"
)

// The checksum covers the whole file, so an interrupted upload had to read
// again from the ASIAIR everything the receiver already had before sending
// on — over a slow card or Wi-Fi a minute and more. Instead the state of
// the checksum is kept every markStep bytes of the upload; the receiver
// keeps whole compression blocks (wire.BlockSize) counted from the start of
// the upload, so it usually stops right at a mark and nothing is read again.

const (
	markStep  = wire.BlockSize
	maxMarked = 64 // files whose marks are kept
)

// hashMarks are the states of the checksum of one file at some offsets.
type hashMarks struct {
	size, mtime int64
	at          []int64  // offsets, growing
	state       [][]byte // the checksum of the bytes before at[i]
	used        time.Time
}

// markHash hashes the bytes written to it and gives the state of the hash
// at base and every markStep bytes after it.
type markHash struct {
	h         hash.Hash
	pos, next int64
	save      func(pos int64, h hash.Hash)
}

func newMarkHash(h hash.Hash, base int64, save func(int64, hash.Hash)) *markHash {
	save(base, h)
	return &markHash{h: h, pos: base, next: base + markStep, save: save}
}

func (m *markHash) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		k := min(int64(len(p)), m.next-m.pos)
		m.h.Write(p[:k])
		m.pos += k
		p = p[k:]
		if m.pos == m.next {
			m.save(m.pos, m.h)
			m.next += markStep
		}
	}
	return n, nil
}

// saveMark keeps the state of the checksum of f at pos.
func (a *Agent) saveMark(f source.File, pos int64, h hash.Hash) {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return
	}
	st, err := m.MarshalBinary()
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	hm := a.marks[f.Path]
	if hm == nil || hm.size != f.Size || hm.mtime != f.ModTime.UnixNano() {
		hm = &hashMarks{size: f.Size, mtime: f.ModTime.UnixNano()}
		a.marks[f.Path] = hm
		a.trimMarksLocked()
	}
	hm.used = time.Now()
	i := sort.Search(len(hm.at), func(i int) bool { return hm.at[i] >= pos })
	if i < len(hm.at) && hm.at[i] == pos {
		hm.state[i] = st
		return
	}
	hm.at = append(hm.at, 0)
	hm.state = append(hm.state, nil)
	copy(hm.at[i+1:], hm.at[i:])
	copy(hm.state[i+1:], hm.state[i:])
	hm.at[i], hm.state[i] = pos, st
}

// trimMarksLocked forgets the marks of the files sent longest ago. Caller
// holds a.mu.
func (a *Agent) trimMarksLocked() {
	for len(a.marks) > maxMarked {
		oldest := ""
		var t time.Time
		for p, hm := range a.marks {
			if oldest == "" || hm.used.Before(t) {
				oldest, t = p, hm.used
			}
		}
		delete(a.marks, oldest)
	}
}

// resumeHash restores into h the checksum of f at the last mark not after
// offset and returns that mark (0 when there is none and h is untouched).
func (a *Agent) resumeHash(f source.File, offset int64, h hash.Hash) int64 {
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok || offset <= 0 {
		return 0
	}
	a.mu.Lock()
	hm := a.marks[f.Path]
	var pos int64
	var st []byte
	if hm != nil && hm.size == f.Size && hm.mtime == f.ModTime.UnixNano() {
		i := sort.Search(len(hm.at), func(i int) bool { return hm.at[i] > offset })
		if i > 0 {
			pos, st = hm.at[i-1], hm.state[i-1]
		}
	}
	a.mu.Unlock()
	if pos <= 0 || u.UnmarshalBinary(st) != nil {
		h.Reset()
		return 0
	}
	return pos
}

// forgetMarks drops the marks of a file sent or gone. Caller holds a.mu.
func (a *Agent) forgetMarksLocked(p string) { delete(a.marks, p) }
