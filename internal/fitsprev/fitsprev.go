package fitsprev

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const blockSize = 2880

// Header holds the keywords of the primary HDU.
type Header struct {
	keys map[string]string
}

// Get returns the value of a keyword ("" if absent); strings are unquoted.
func (h *Header) Get(key string) string { return h.keys[key] }

func (h *Header) int(key string, def int) int {
	if v, err := strconv.Atoi(h.Get(key)); err == nil {
		return v
	}
	return def
}

func (h *Header) float(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.Replace(h.Get(key), "D", "E", 1), 64); err == nil {
		return v
	}
	return def
}

// Image is the preview: Channels interleaved samples per pixel, row by row
// from the top, normalised to 0..1.
type Image struct {
	Width, Height, Channels int
	Data                    []float32
	Binning                 int    // source pixels per preview pixel (per axis)
	Bayer                   string // debayered pattern, "" for mono or RGB data
}

// parseValue extracts the value of a header card (quotes and comments removed).
func parseValue(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "'") {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' { // escaped quote
					b.WriteByte('\'')
					i++
					continue
				}
				break
			}
			b.WriteByte(s[i])
		}
		return strings.TrimRight(b.String(), " ")
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ReadHeader reads the primary header, leaving r at the start of the data.
func ReadHeader(r io.Reader) (*Header, error) {
	h := &Header{keys: map[string]string{}}
	block := make([]byte, blockSize)
	for first := true; ; first = false {
		if _, err := io.ReadFull(r, block); err != nil {
			return nil, fmt.Errorf("не FITS: %w", err)
		}
		if first && !strings.HasPrefix(string(block), "SIMPLE  =") {
			return nil, errors.New("не FITS: нет SIMPLE в начале")
		}
		for i := 0; i < blockSize; i += 80 {
			card := string(block[i : i+80])
			key := strings.TrimSpace(card[:8])
			if key == "END" {
				return h, nil
			}
			if len(card) > 10 && card[8:10] == "= " && key != "" {
				h.keys[key] = parseValue(card[10:])
			}
		}
	}
}

// bayer colour index (0=R,1=G,2=B) for a pattern at (x%2, y%2).
func bayerIndex(pattern string, xOff, yOff int) ([2][2]int, error) {
	var idx [2][2]int
	p := strings.ToUpper(strings.TrimSpace(pattern))
	if len(p) != 4 || strings.Trim(p, "RGB") != "" {
		return idx, fmt.Errorf("неизвестный шаблон Байера %q", pattern)
	}
	col := map[byte]int{'R': 0, 'G': 1, 'B': 2}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			// the pattern describes pixel (0,0) of the sensor; offsets
			// shift where the image starts on it
			idx[y][x] = col[p[((y+yOff)%2)*2+(x+xOff)%2]]
		}
	}
	return idx, nil
}

func decodeRow(row []byte, bitpix int, bscale, bzero float64, out []float64) {
	be := binary.BigEndian
	for i := range out {
		var raw float64
		switch bitpix {
		case 8:
			raw = float64(row[i])
		case 16:
			raw = float64(int16(be.Uint16(row[i*2:])))
		case 32:
			raw = float64(int32(be.Uint32(row[i*4:])))
		case -32:
			raw = float64(math.Float32frombits(be.Uint32(row[i*4:])))
		case -64:
			raw = math.Float64frombits(be.Uint64(row[i*8:]))
		}
		out[i] = bzero + bscale*raw
	}
}

// normalize maps values to 0..1: integer data by the range of its type,
// floating point data by its actual minimum and maximum.
func normalize(d []float32, bitpix int, bscale, bzero float64) func(float64) float64 {
	var lo, hi float64
	switch bitpix {
	case 8:
		lo, hi = bzero, bzero+255*bscale
	case 16:
		lo, hi = bzero-32768*bscale, bzero+32767*bscale
	case 32:
		lo, hi = bzero-2147483648*bscale, bzero+2147483647*bscale
	default:
		lo, hi = math.Inf(1), math.Inf(-1)
		for _, v := range d {
			lo = math.Min(lo, float64(v))
			hi = math.Max(hi, float64(v))
		}
	}
	if bscale < 0 {
		lo, hi = hi, lo
	}
	span := hi - lo
	if span <= 0 || math.IsInf(span, 0) || math.IsNaN(span) {
		span = 1
	}
	norm := func(v float64) float64 {
		x := (v - lo) / span
		if x < 0 {
			x = 0
		} else if x > 1 {
			x = 1
		}
		return x
	}
	for i, v := range d {
		d[i] = float32(norm(float64(v)))
	}
	return norm
}

func flipRows(d []float32, rowLen, rows int) {
	tmp := make([]float32, rowLen)
	for y := 0; y < rows/2; y++ {
		a, b := d[y*rowLen:(y+1)*rowLen], d[(rows-1-y)*rowLen:(rows-y)*rowLen]
		copy(tmp, a)
		copy(a, b)
		copy(b, tmp)
	}
}
