// Package fits reads FITS images (as written by the ASIAIR and other
// astro cameras) into small previews: the image is binned down while it is
// read, and one-shot-colour (Bayer) frames are debayered on the way.
package fits

import (
	"bufio"
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
	keys  map[string]string
	order []string
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

// Keys returns the keywords in file order.
func (h *Header) Keys() []string { return h.order }

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
				if _, seen := h.keys[key]; !seen {
					h.order = append(h.order, key)
				}
				h.keys[key] = parseValue(card[10:])
			}
		}
	}
}

// Image is a binned preview. Data holds Channels interleaved samples per
// pixel, row by row from the top, normalised to 0..1.
type Image struct {
	Width, Height, Channels int
	Data                    []float32
	Header                  *Header
	// Binning is how many source pixels form one preview pixel (per axis).
	Binning int
	Bayer   string // debayered pattern, "" for mono or RGB data
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

// Preview reads a FITS image and bins it so that it is at most maxWidth
// pixels wide. Bayer frames (BAYERPAT keyword) become RGB.
func Preview(r io.Reader, maxWidth int) (*Image, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	h, err := ReadHeader(br)
	if err != nil {
		return nil, err
	}
	bitpix := h.int("BITPIX", 0)
	naxis := h.int("NAXIS", 0)
	w, ht := h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	planes := 1
	if naxis >= 3 {
		planes = h.int("NAXIS3", 1)
	}
	if naxis < 2 || w <= 0 || ht <= 0 {
		return nil, errors.New("в файле нет изображения")
	}
	bpp := map[int]int{8: 1, 16: 2, 32: 4, -32: 4, -64: 8}[bitpix]
	if bpp == 0 {
		return nil, fmt.Errorf("неподдерживаемый BITPIX=%d", bitpix)
	}
	bscale, bzero := h.float("BSCALE", 1), h.float("BZERO", 0)

	img := &Image{Header: h}
	var pattern [2][2]int
	isBayer := planes == 1 && h.Get("BAYERPAT") != ""
	if isBayer {
		pattern, err = bayerIndex(h.Get("BAYERPAT"), h.int("XBAYROFF", 0), h.int("YBAYROFF", 0))
		if err != nil {
			isBayer = false
		} else {
			img.Bayer = strings.ToUpper(h.Get("BAYERPAT"))
		}
	}
	channels, base := 1, 1
	switch {
	case isBayer:
		channels, base = 3, 2
	case planes == 3:
		channels = 3
	}
	if maxWidth < 16 {
		maxWidth = 16
	}
	f := (w/base + maxWidth - 1) / maxWidth
	if f < 1 {
		f = 1
	}
	step := base * f
	ow, oh := w/step, ht/step
	if ow < 1 || oh < 1 {
		return nil, errors.New("изображение слишком маленькое")
	}
	img.Width, img.Height, img.Channels, img.Binning = ow, oh, channels, step
	sum := make([]float32, ow*oh*channels)
	cnt := make([]float32, ow*oh*channels)

	row := make([]byte, w*bpp)
	vals := make([]float64, w)
	usePlanes := planes
	if channels == 1 {
		usePlanes = 1 // ignore extra planes of a mono cube
	}
	for p := 0; p < usePlanes; p++ {
		for y := 0; y < ht; y++ {
			if _, err := io.ReadFull(br, row); err != nil {
				return nil, fmt.Errorf("файл обрезан: %w", err)
			}
			decodeRow(row, bitpix, bscale, bzero, vals)
			oy := y / step
			if oy >= oh {
				continue
			}
			base := oy * ow * channels
			for x := 0; x < ow*step; x++ {
				v := vals[x]
				if math.IsNaN(v) {
					continue
				}
				c := p
				if isBayer {
					c = pattern[y%2][x%2]
				}
				i := base + (x/step)*channels + c
				sum[i] += float32(v)
				cnt[i]++
			}
		}
	}
	for i := range sum {
		if cnt[i] > 0 {
			sum[i] /= cnt[i]
		}
	}
	normalize(sum, bitpix, bscale, bzero, h)
	if strings.EqualFold(h.Get("ROWORDER"), "BOTTOM-UP") {
		flipRows(sum, ow*channels, oh)
	}
	img.Data = sum
	return img, nil
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
func normalize(d []float32, bitpix int, bscale, bzero float64, h *Header) {
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
	for i, v := range d {
		x := (float64(v) - lo) / span
		if x < 0 {
			x = 0
		} else if x > 1 {
			x = 1
		}
		d[i] = float32(x)
	}
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

// Uint16 returns the preview samples scaled to 0..65535, for sending to the browser.
func (im *Image) Uint16() []uint16 {
	out := make([]uint16, len(im.Data))
	for i, v := range im.Data {
		out[i] = uint16(v*65535 + 0.5)
	}
	return out
}
