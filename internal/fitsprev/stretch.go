package fitsprev

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
	return autoStretch(im.Data, 1, 0)
}

// AutoStretchChannels: the auto stretch of each channel, of all of them
// together (linked) or of each on its own, as "Каналы вместе" of the
// receiver's viewer.
func (im *Image) AutoStretchChannels(linked bool) (black, mid []float64) {
	ch := max(1, im.Channels)
	black, mid = make([]float64, ch), make([]float64, ch)
	if linked || ch == 1 {
		b, m := im.AutoStretch()
		for c := range black {
			black[c], mid[c] = b, m
		}
		return
	}
	for c := range black {
		black[c], mid[c] = autoStretch(im.Data, ch, c)
	}
	return
}

// autoStretch of the samples c, c+stride, c+2·stride… of d.
func autoStretch(d []float32, stride, c int) (black, mid float64) {
	// median and MAD of a sample of the pixels, from a 16-bit histogram
	step := max(1, len(d)/stride/400000) * stride
	var hist [65536]uint32
	n := 0
	for i := c; i < len(d); i += step {
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
func (im *Image) Stretched() image.Image { return im.StretchedChannels(true) }

// StretchedChannels: the picture with the auto stretch of the channels
// linked or each on its own.
func (im *Image) StretchedChannels(linked bool) image.Image {
	d := im.Data
	w, h, ch := im.Width, im.Height, im.Channels
	if ch == 1 {
		lut := stretchLUT(im.AutoStretch())
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i, v := range d {
			g.Pix[i] = lut[q16(v)]
		}
		return g
	}
	luts := im.channelLUTs(linked)
	r, gr, b := luts[0], luts[1], luts[2]
	rgb := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		rgb.Pix[i*4], rgb.Pix[i*4+1], rgb.Pix[i*4+2], rgb.Pix[i*4+3] = r[q16(d[i*ch])], gr[q16(d[i*ch+1])], b[q16(d[i*ch+2])], 255
	}
	return rgb
}

// channelLUTs: the screen values of each channel; linked channels share one.
func (im *Image) channelLUTs(linked bool) []*[65536]uint8 {
	black, mid := im.AutoStretchChannels(linked)
	luts := make([]*[65536]uint8, len(black))
	for c := range luts {
		if c > 0 && black[c] == black[0] && mid[c] == mid[0] {
			luts[c] = luts[0]
		} else {
			luts[c] = stretchLUT(black[c], mid[c])
		}
	}
	return luts
}

// histMedian: the bin of the median of n counts.
func histMedian(hist []uint32, n int64) int {
	var acc int64
	for u, k := range hist {
		acc += int64(k)
		if 2*acc >= n {
			return u
		}
	}
	return len(hist) - 1
}
