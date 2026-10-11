package fits

import (
	"bytes"
	"errors"
	"strings"
	"sync"
)

// Stream builds a preview from a FITS file written to it piece by piece,
// e.g. while the file is uploaded: Snapshot shows the rows received so far.
// It never fails a write: a file that is not a FITS image is ignored.
type Stream struct {
	maxWidth int

	mu     sync.Mutex
	head   []byte // header bytes until END
	ready  bool   // the header is read; the fields below are set
	bad    bool   // not a usable image: everything is ignored
	h      *Header
	bitpix int
	bpp    int
	w, ht  int
	bscale float64
	bzero  float64
	bayer  bool
	pat    [2][2]int
	ch     int
	step   int
	ow, oh int
	sum    []float32
	cnt    []float32
	row    []byte // the row being filled
	rowN   int    // bytes in row
	y      int    // rows done (of the first plane)
	vals   []float64
}

// NewStream returns a stream whose preview is at most maxWidth wide.
func NewStream(maxWidth int) *Stream { return &Stream{maxWidth: max(16, maxWidth)} }

// Write takes the next bytes of the file.
func (s *Stream) Write(p []byte) (int, error) {
	n := len(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(p) > 0 && !s.bad {
		if !s.ready {
			take := min(len(p), blockSize-len(s.head)%blockSize)
			s.head = append(s.head, p[:take]...)
			p = p[take:]
			if len(s.head)%blockSize == 0 {
				s.headerBlock()
			}
			continue
		}
		if s.y >= s.ht {
			break // the image is complete (other planes are not shown)
		}
		take := min(len(p), len(s.row)-s.rowN)
		copy(s.row[s.rowN:], p[:take])
		s.rowN += take
		p = p[take:]
		if s.rowN == len(s.row) {
			s.addRow()
			s.rowN = 0
		}
	}
	return n, nil
}

// headerBlock checks the last header block for END and sets up the image.
func (s *Stream) headerBlock() {
	if len(s.head) == blockSize && !strings.HasPrefix(string(s.head), "SIMPLE  =") {
		s.bad = true
		return
	}
	last := s.head[len(s.head)-blockSize:]
	end := false
	for i := 0; i < blockSize; i += 80 {
		if strings.TrimSpace(string(last[i:i+8])) == "END" {
			end = true
			break
		}
	}
	if !end {
		if len(s.head) > 100*blockSize {
			s.bad = true
		}
		return
	}
	h, err := ReadHeader(bytes.NewReader(s.head))
	s.head = nil
	if err != nil {
		s.bad = true
		return
	}
	if err := s.setup(h); err != nil {
		s.bad = true
		return
	}
	s.ready = true
}

func (s *Stream) setup(h *Header) error {
	s.h = h
	s.bitpix = h.int("BITPIX", 0)
	s.w, s.ht = h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	planes := 1
	if h.int("NAXIS", 0) >= 3 {
		planes = h.int("NAXIS3", 1)
	}
	if h.int("NAXIS", 0) < 2 || s.w <= 0 || s.ht <= 0 {
		return errors.New("в файле нет изображения")
	}
	s.bpp = map[int]int{8: 1, 16: 2, 32: 4, -32: 4, -64: 8}[s.bitpix]
	if s.bpp == 0 {
		return errors.New("неподдерживаемый BITPIX")
	}
	s.bscale, s.bzero = h.float("BSCALE", 1), h.float("BZERO", 0)
	base := 1
	s.ch = 1
	if planes == 1 && h.Get("BAYERPAT") != "" {
		if pat, err := bayerIndex(h.Get("BAYERPAT"), h.int("XBAYROFF", 0), h.int("YBAYROFF", 0)); err == nil {
			s.bayer, s.pat, s.ch, base = true, pat, 3, 2
		}
	}
	f := max(1, (s.w/base+s.maxWidth-1)/s.maxWidth)
	s.step = base * f
	s.ow, s.oh = s.w/s.step, s.ht/s.step
	if s.ow < 1 || s.oh < 1 {
		return errors.New("изображение слишком маленькое")
	}
	s.sum = make([]float32, s.ow*s.oh*s.ch)
	s.cnt = make([]float32, s.ow*s.oh*s.ch)
	s.row = make([]byte, s.w*s.bpp)
	s.vals = make([]float64, s.w)
	return nil
}

func (s *Stream) addRow() {
	decodeRow(s.row, s.bitpix, s.bscale, s.bzero, s.vals)
	y := s.y
	s.y++
	oy := y / s.step
	if oy >= s.oh {
		return
	}
	base := oy * s.ow * s.ch
	for x := 0; x < s.ow*s.step; x++ {
		v := s.vals[x]
		if v != v { // NaN
			continue
		}
		c := 0
		if s.bayer {
			c = s.pat[y%2][x%2]
		}
		i := base + (x/s.step)*s.ch + c
		s.sum[i] += float32(v)
		s.cnt[i]++
	}
}

// Progress is the share of the image rows received, 0..1.
func (s *Stream) Progress() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.ht == 0 {
		return 0
	}
	return float64(s.y) / float64(s.ht)
}

// Snapshot returns the preview of the rows received so far, or nil before
// the first rows. Full is the height of the whole preview; fromBottom tells
// that the rows received are the bottom of the picture (ROWORDER BOTTOM-UP).
func (s *Stream) Snapshot() (img *Image, full int, fromBottom bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return nil, 0, false
	}
	rows := s.y / s.step // preview rows complete
	if rows == 0 {
		return nil, 0, false
	}
	rows = min(rows, s.oh)
	n := rows * s.ow * s.ch
	d := make([]float32, n)
	for i := range d {
		if s.cnt[i] > 0 {
			d[i] = s.sum[i] / s.cnt[i]
		}
	}
	normalize(d, s.bitpix, s.bscale, s.bzero, s.h)
	img = &Image{Width: s.ow, Height: rows, Channels: s.ch, Data: d, Header: s.h, Binning: s.step}
	if s.bayer {
		img.Bayer = strings.ToUpper(s.h.Get("BAYERPAT"))
	}
	if strings.EqualFold(s.h.Get("ROWORDER"), "BOTTOM-UP") {
		flipRows(d, s.ow*s.ch, rows)
		fromBottom = true
	}
	return img, s.oh, fromBottom
}
