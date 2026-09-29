package fits

import (
	"image"
	"math"
	"sort"
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

// Stretched turns a preview into a picture with the auto stretch of the
// viewer (shadows at median − 2.8·σ, background at 0.25), channels linked.
func (im *Image) Stretched() image.Image {
	d := im.Data
	step := max(1, len(d)/200000)
	s := make([]float64, 0, len(d)/step+1)
	for i := 0; i < len(d); i += step {
		s = append(s, float64(d[i]))
	}
	sort.Float64s(s)
	med := s[len(s)/2]
	for i, v := range s {
		s[i] = math.Abs(v - med)
	}
	sort.Float64s(s)
	madn := 1.4826 * s[len(s)/2]
	c0 := math.Max(0, med-2.8*madn)
	m := mtf(0.25, math.Max((med-c0)/(1-c0), 1e-6))
	lut := func(v float32) uint8 {
		x := (float64(v) - c0) / (1 - c0)
		return uint8(math.Round(mtf(m, x) * 255))
	}
	w, h, ch := im.Width, im.Height, im.Channels
	if ch == 1 {
		g := image.NewGray(image.Rect(0, 0, w, h))
		for i, v := range d {
			g.Pix[i] = lut(v)
		}
		return g
	}
	rgb := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		rgb.Pix[i*4], rgb.Pix[i*4+1], rgb.Pix[i*4+2], rgb.Pix[i*4+3] = lut(d[i*ch]), lut(d[i*ch+1]), lut(d[i*ch+2]), 255
	}
	return rgb
}
