// Package wire compresses the images on their way from the agent to the
// receiver. Only the transfer is compressed: the receiver stores the file
// as it is on the ASIAIR, and offsets and checksums are those of the file.
//
// A FITS image is mostly 16-bit pixels whose high bytes change slowly and
// whose low bits are noise (or zero: a 14-bit camera keeps 2 bits at 0).
// The data are cut into blocks of BlockSize bytes; in every block the bytes
// at even places come first, then those at odd places, so the high bytes
// and the low bytes of the pixels are compressed apart. Then zstd.
// On real frames of an ASI533MC this gives about 2.1x for lights, 1.6x for
// flats and 5x for darks and bias, at 40-100 MB/s on one core: far more
// than an internet link.
package wire

import (
	"bytes"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Encoding is the Content-Encoding of a compressed upload.
const Encoding = "x-zstd-shuffle2"

// BlockSize is the block of the byte shuffle. A transfer cut in the middle
// keeps only whole blocks.
const BlockSize = 256 << 10

// maxWindow limits the memory the receiver gives to one upload.
const maxWindow = 8 << 20

// Compressible reports whether a file is worth compressing: images stored
// without compression (JPG and PNG are compressed already).
func Compressible(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".fit", ".fits", ".fts", ".tif", ".tiff", ".xisf":
		return true
	}
	return false
}

// Accepts reports whether a list of encodings ("a, b") has Encoding.
func Accepts(list string) bool {
	for _, e := range strings.Split(list, ",") {
		if strings.TrimSpace(e) == Encoding {
			return true
		}
	}
	return false
}

// shuffle puts the bytes of src at even places first, then those at odd
// places, into dst (same length).
func shuffle(dst, src []byte) {
	h := (len(src) + 1) / 2
	for i, b := range src {
		if i%2 == 0 {
			dst[i/2] = b
		} else {
			dst[h+i/2] = b
		}
	}
}

// unshuffle undoes shuffle.
func unshuffle(dst, src []byte) {
	h := (len(src) + 1) / 2
	for i := range dst {
		if i%2 == 0 {
			dst[i] = src[i/2]
		} else {
			dst[i] = src[h+i/2]
		}
	}
}

// Encoder reads the data of src and returns them compressed.
type Encoder struct {
	src        io.Reader
	enc        *zstd.Encoder
	out        bytes.Buffer
	block, tmp []byte
	done       bool
	err        error
	raw        int64 // bytes read from src
	sent       int64 // compressed bytes returned
	// Busy, if set, is told how long each compression took (the time is
	// spent inside Read, beside the reading of src).
	Busy func(time.Duration)
	// Done, if set, is told how many bytes of src have gone out compressed:
	// src is read a block ahead, so this is the progress of the transfer.
	Done func(raw int64)
}

// NewEncoder compresses src. Close it when done.
func NewEncoder(src io.Reader) *Encoder {
	e := &Encoder{src: src, block: make([]byte, BlockSize), tmp: make([]byte, BlockSize)}
	// one goroutine, small window: every block goes out as soon as it is
	// read; "better" compresses lights 2.1x instead of 1.7x for the default
	e.enc, e.err = zstd.NewWriter(&e.out, zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
		zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20))
	return e
}

func (e *Encoder) Read(p []byte) (int, error) {
	for e.out.Len() == 0 {
		if e.err != nil {
			return 0, e.err
		}
		if e.done {
			return 0, io.EOF
		}
		n, err := io.ReadFull(e.src, e.block)
		last := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !last {
			n = 0 // a block cut by a read error is not sent
		}
		e.raw += int64(n)
		start := time.Now()
		if n > 0 {
			shuffle(e.tmp[:n], e.block[:n])
			if _, werr := e.enc.Write(e.tmp[:n]); werr != nil {
				e.err = werr
			} else if ferr := e.enc.Flush(); ferr != nil {
				e.err = ferr
			}
		}
		switch {
		case last:
			e.done = true
			if cerr := e.enc.Close(); cerr != nil && e.err == nil {
				e.err = cerr
			}
		case err != nil && e.err == nil:
			e.err = err
		}
		if e.Busy != nil {
			e.Busy(time.Since(start))
		}
	}
	n, _ := e.out.Read(p)
	e.sent += int64(n)
	if e.out.Len() == 0 && e.Done != nil {
		e.Done(e.raw)
	}
	return n, nil
}

// Sent returns how many bytes were read from src and how many compressed
// bytes were returned for them.
func (e *Encoder) Sent() (raw, compressed int64) { return e.raw, e.sent }

// Close frees the encoder.
func (e *Encoder) Close() error {
	if e.enc != nil {
		e.enc.Close()
	}
	return nil
}

// ErrTruncated: the compressed data ended before the expected size.
var ErrTruncated = errors.New("сжатые данные оборвались")

// Decoder returns the data of an upload compressed by Encoder.
type Decoder struct {
	dec        *zstd.Decoder
	remain     int64 // bytes still expected
	block, out []byte
	pos        int // next byte of out to return
	err        error
}

// NewDecoder reads the compressed data of size bytes from r. Only whole
// blocks are returned: when r breaks off, the block in progress is dropped
// (it cannot be unshuffled) and Read returns the error.
func NewDecoder(r io.Reader, size int64) (*Decoder, error) {
	dec, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(maxWindow))
	if err != nil {
		return nil, err
	}
	return &Decoder{dec: dec, remain: size, block: make([]byte, BlockSize), out: make([]byte, 0, BlockSize)}, nil
}

func (d *Decoder) Read(p []byte) (int, error) {
	if d.pos == len(d.out) {
		if d.err != nil {
			return 0, d.err
		}
		if d.remain == 0 {
			return 0, io.EOF
		}
		n := int(min(int64(BlockSize), d.remain))
		if _, err := io.ReadFull(d.dec, d.block[:n]); err != nil {
			if err == io.EOF {
				err = ErrTruncated
			}
			d.err = err
			return 0, err
		}
		d.remain -= int64(n)
		d.out = d.out[:n]
		unshuffle(d.out, d.block[:n])
		d.pos = 0
	}
	n := copy(p, d.out[d.pos:])
	d.pos += n
	return n, nil
}

// Close frees the decoder.
func (d *Decoder) Close() error {
	d.dec.Close()
	return nil
}
