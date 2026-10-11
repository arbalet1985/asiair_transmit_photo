package fits

import (
	"image"
	"math"
)

// mtf is the midtones transfer function of PixInsight's STF.
func mtf(m, x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	return (m - 1) * x / ((2*m-1)*x - m)
}

// AutoStretch is the auto stretch of the viewer: shadows at median − 2.8·σ,
// background at 0.25, channels linked. Black is the black point (0..1),
// Mid the midtones balance.
func (im *Image) AutoStretch() (black, mid float64) {
	// median and MAD of a sample of the pixels, from a 16-bit histogram
	d := im.Data
	step := max(1, len(d)/400000)
	var hist [65536]uint32
	n := 0
	for i := 0; i < len(d); i += step {
		hist[q16(d[i])]++
		n++
	}
	med := histMedian(hist[:], int64(n))
	var dev [65536]uint32
	for v, k := range hist {
		if k > 0 {
			m := v - med
			if m < 0 {
				m = -m
			}
			dev[m] += k
		}
	}
	medf := float64(med) / 65535
	madn := 1.4826 * float64(histMedian(dev[:], int64(n))) / 65535
	black = math.Max(0, medf-2.8*madn)
	return black, mtf(0.25, math.Max((medf-black)/(1-black), 1e-6))
}

// q16 is a sample 0..1 as 16 bits.
func q16(v float32) uint16 {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 65535
	}
	return uint16(v*65535 + 0.5)
}

// stretchLUT: the screen value of every 16-bit sample.
func stretchLUT(black, mid float64) *[65536]uint8 {
	var lut [65536]uint8
	for v := range lut {
		lut[v] = uint8(math.Round(mtf(mid, (float64(v)/65535-black)/(1-black)) * 255))
	}
	return &lut
}

// Stretched turns a preview into a picture with the auto stretch.
func (im *Image) Stretched() image.Image {
	lut := stretchLUT(im.AutoStretch())
	d := im.Data
	w, h, ch := im.Width, im.Height, im.Channels
	if ch == 1 {
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i, v := range d {
			g.Pix[i] = lut[q16(v)]
		}
		return g
	}
	rgb := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		rgb.Pix[i*4], rgb.Pix[i*4+1], rgb.Pix[i*4+2], rgb.Pix[i*4+3] = lut[q16(d[i*ch])], lut[q16(d[i*ch+1])], lut[q16(d[i*ch+2])], 255
	}
	return rgb
}

// Histogram of a preview, per channel.
type Histogram struct {
	// Lo, Hi: the range counted in Counts, 0..1 of the data: from 0.01% to
	// 99.95% of the samples, where the picture is, with a margin
	Lo, Hi float64
	Counts [][]uint32
	// Shown: 256 bins of the screen after the auto stretch
	Shown [][]uint32
	// Black, White: the points of the auto stretch, 0..1 of the data
	Black, White float64
}

// Histogram counts the samples of the preview in bins over where they are.
func (im *Image) Histogram(bins int) *Histogram {
	ch := im.Channels
	full := make([][]uint32, ch)
	for c := range full {
		full[c] = make([]uint32, 65536)
	}
	for i, v := range im.Data {
		full[i%ch][q16(v)]++
	}
	black, mid := im.AutoStretch()
	lut := stretchLUT(black, mid)
	h := &Histogram{Black: black, White: 1, Counts: make([][]uint32, ch), Shown: make([][]uint32, ch)}
	all := make([]uint64, 65536)
	var n uint64
	for c := range full {
		h.Shown[c] = make([]uint32, 256)
		for v, k := range full[c] {
			all[v] += uint64(k)
			n += uint64(k)
			h.Shown[c][lut[v]] += k
		}
	}
	lo, hi := -1, 65535
	var acc uint64
	for v, k := range all {
		acc += k
		if lo < 0 && acc*10000 >= n {
			lo = v
		}
		if acc*10000 >= n*9995 {
			hi = v
			break
		}
	}
	pad := max(8, (hi-lo)/20)
	lo, hi = max(0, lo-pad), min(65535, hi+pad)
	h.Lo, h.Hi = float64(lo)/65535, float64(hi)/65535
	span := float64(hi - lo + 1)
	for c := range full {
		h.Counts[c] = make([]uint32, bins)
		for v := lo; v <= hi; v++ {
			h.Counts[c][int(float64(v-lo)/span*float64(bins))] += full[c][v]
		}
	}
	return h
}
