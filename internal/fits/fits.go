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
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	// Stats: of the whole frame, per channel (only from Preview).
	Stats []ChannelStats
	// Max: the brightest sample of each channel of the whole frame (not of
	// the preview), normalised as Data; nil when unknown. Binning lowers
	// the peaks of the preview, the level of saturation is known from it.
	Max []float32
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
	usePlanes := planes
	if channels == 1 {
		usePlanes = 1 // ignore extra planes of a mono cube
	}
	if bitpix == 16 && step <= 256 { // the sum of a preview pixel fits 32 bits
		return preview16(br, img, w, ht, usePlanes, isBayer, pattern, bscale, bzero)
	}
	sum := make([]float32, ow*oh*channels)
	cnt := make([]float32, ow*oh*channels)

	row := make([]byte, w*bpp)
	vals := make([]float64, w)
	acc := make([]momentAcc, channels)
	for p := 0; p < usePlanes; p++ {
		for y := 0; y < ht; y++ {
			if _, err := io.ReadFull(br, row); err != nil {
				return nil, fmt.Errorf("файл обрезан: %w", err)
			}
			decodeRow(row, bitpix, bscale, bzero, vals)
			for x, v := range vals {
				if math.IsNaN(v) {
					continue
				}
				c := p
				if isBayer {
					c = pattern[y%2][x%2]
				}
				acc[c].add(v)
			}
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
	norm := normalize(sum, bitpix, bscale, bzero, h)
	if strings.EqualFold(h.Get("ROWORDER"), "BOTTOM-UP") {
		flipRows(sum, ow*channels, oh)
	}
	img.Data = sum
	for c, name := range channelNames(channels) {
		img.Stats = append(img.Stats, acc[c].stats(name))
		img.Max = append(img.Max, float32(norm(acc[c].max)))
	}
	return img, nil
}

// preview16 is Preview of 16-bit integer data, the data of cameras: the
// samples are binned as integers and counted in a histogram per channel
// for the statistics. The data is read whole and worked on in bands of
// rows at once.
func preview16(br io.Reader, img *Image, w, ht, planes int, isBayer bool, pattern [2][2]int, bscale, bzero float64) (*Image, error) {
	ow, oh, ch, step := img.Width, img.Height, img.Channels, img.Binning
	data := make([]byte, w*ht*2*planes)
	if _, err := io.ReadFull(br, data); err != nil {
		return nil, fmt.Errorf("файл обрезан: %w", err)
	}
	sum := make([]uint32, ow*oh*ch)
	cw := ow * step // columns inside the preview
	// row adds row y of plane p; every band has its own histograms
	row := func(p, y int, hist [][]uint32) {
		r := data[(p*ht+y)*w*2:][:w*2]
		oy := y / step
		in := oy < oh
		base := oy * ow * ch
		if isBayer {
			pr := pattern[y%2]
			h0, h1 := hist[pr[0]], hist[pr[1]]
			// i: the preview pixel of column x, k: columns left in it
			i, k := base, step
			for x := 0; x+1 < w; x += 2 {
				// int16 + 32768: the sample as unsigned
				u0 := uint16(r[2*x])<<8 | uint16(r[2*x+1]) ^ 0x8000
				u1 := uint16(r[2*x+2])<<8 | uint16(r[2*x+3]) ^ 0x8000
				h0[u0]++
				h1[u1]++
				if in && x < cw {
					sum[i+pr[0]] += uint32(u0)
					sum[i+pr[1]] += uint32(u1)
					if k -= 2; k == 0 {
						i, k = i+ch, step
					}
				}
			}
			if w%2 == 1 {
				x := w - 1
				h0[uint16(r[2*x])<<8|uint16(r[2*x+1])^0x8000]++
			}
			return
		}
		hc := hist[p]
		i, k := base+p, step
		for x := 0; x < w; x++ {
			u := uint16(r[2*x])<<8 | uint16(r[2*x+1]) ^ 0x8000
			hc[u]++
			if in && x < cw {
				sum[i] += uint32(u)
				if k--; k == 0 {
					i, k = i+ch, step
				}
			}
		}
	}
	// bands of whole preview rows: no two bands add to the same pixel
	bands := max(1, min(runtime.GOMAXPROCS(0), 8, ht/64))
	per := ((ht+step-1)/step + bands - 1) / bands * step
	hists := make([][][]uint32, bands)
	var wg sync.WaitGroup
	for b := range bands {
		hist := make([][]uint32, ch)
		for c := range hist {
			hist[c] = make([]uint32, 65536)
		}
		hists[b] = hist
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := 0; p < planes; p++ {
				for y := b * per; y < min((b+1)*per, ht); y++ {
					row(p, y, hist)
				}
			}
		}()
	}
	wg.Wait()
	hist := hists[0]
	for _, h := range hists[1:] {
		for c := range h {
			for u, k := range h[c] {
				hist[c][u] += k
			}
		}
	}
	// samples in a preview pixel per channel: a Bayer square has one red,
	// two green and one blue sample per 2×2
	cnt := make([]float64, ch)
	for c := range cnt {
		cnt[c] = float64(step * step)
	}
	if isBayer {
		q := float64(step * step / 4)
		cnt[0], cnt[1], cnt[2] = q, 2*q, q
	}
	var mul, add [3]float32
	for c := range ch {
		mul[c] = float32(1 / (cnt[c] * 65535))
		if bscale < 0 {
			mul[c], add[c] = -mul[c], 1
		}
	}
	d := make([]float32, len(sum))
	for i, c := 0, 0; i < len(sum); i++ {
		d[i] = float32(sum[i])*mul[c] + add[c]
		if c++; c == ch {
			c = 0
		}
	}
	if strings.EqualFold(img.Header.Get("ROWORDER"), "BOTTOM-UP") {
		flipRows(d, ow*ch, oh)
	}
	img.Data = d
	for c, name := range channelNames(ch) {
		img.Stats = append(img.Stats, histStats(name, hist[c], bscale, bzero))
		img.Max = append(img.Max, histTop(hist[c], bscale < 0))
	}
	return img, nil
}

// histTop is the brightest sample of a histogram of 16-bit samples,
// normalised as preview16 does (inverted for a negative BSCALE).
func histTop(hist []uint32, inverted bool) float32 {
	if inverted {
		for u, k := range hist {
			if k > 0 {
				return 1 - float32(u)/65535
			}
		}
		return 0
	}
	for u := len(hist) - 1; u >= 0; u-- {
		if hist[u] > 0 {
			return float32(u) / 65535
		}
	}
	return 0
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
// floating point data by its actual minimum and maximum. It returns the
// mapping, for other values of the frame.
func normalize(d []float32, bitpix int, bscale, bzero float64, h *Header) func(float64) float64 {
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

// Uint16 returns the preview samples scaled to 0..65535, for sending to the browser.
func (im *Image) Uint16() []uint16 {
	out := make([]uint16, len(im.Data))
	for i, v := range im.Data {
		out[i] = uint16(v*65535 + 0.5)
	}
	return out
}
