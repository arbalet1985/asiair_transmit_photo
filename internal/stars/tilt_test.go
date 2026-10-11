package stars

import (
	"math"
	"math/rand"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// field draws n stars whose sigma depends on where they are.
func field(n int, sigma func(x, y float64) float64) *fits.Image {
	const w, h = 1200, 900
	rnd := rand.New(rand.NewSource(7))
	d := make([]float32, w*h)
	for i := range d {
		d[i] = float32(0.05 + rnd.NormFloat64()*0.002)
	}
	for s := 0; s < n; s++ {
		cx, cy := 30+rnd.Float64()*(w-60), 30+rnd.Float64()*(h-60)
		sg := sigma(cx/w, cy/h)
		amp := 0.1 + 0.3*rnd.Float64()
		r := int(5 * sg)
		for y := max(0, int(cy)-r); y <= min(h-1, int(cy)+r); y++ {
			for x := max(0, int(cx)-r); x <= min(w-1, int(cx)+r); x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				d[y*w+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sg*sg)))
			}
		}
	}
	return &fits.Image{Width: w, Height: h, Channels: 1, Data: d, Binning: 1}
}

func TestTilt(t *testing.T) {
	flat, err := MeasureTilt(field(400, func(x, y float64) float64 { return 1.6 }))
	if err != nil {
		t.Fatal(err)
	}
	if flat.Percent > 8 || math.Abs(flat.OffAxis) > 0.2 {
		t.Errorf("even field: %+v", flat)
	}
	// stars grow to the right: the right corners are the big ones
	tilted, err := MeasureTilt(field(400, func(x, y float64) float64 { return 1.3 + 0.9*x }))
	if err != nil {
		t.Fatal(err)
	}
	c := tilted.Corners
	if c[1] < c[0]+0.4 || c[3] < c[2]+0.4 || tilted.Percent < 15 {
		t.Errorf("tilted: %+v", tilted)
	}
	// stars grow from the centre: off axis, not a tilt
	curved, err := MeasureTilt(field(400, func(x, y float64) float64 {
		return 1.3 + 2*math.Hypot(x-0.5, y-0.5)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if curved.OffAxis < 0.6 || curved.Percent > 8 {
		t.Errorf("curved: %+v", curved)
	}
	if _, err := MeasureTilt(field(0, func(x, y float64) float64 { return 1.6 })); err != ErrFewStars {
		t.Errorf("empty: %v", err)
	}
}
