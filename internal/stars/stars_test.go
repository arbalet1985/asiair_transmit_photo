package stars

import (
	"math"
	"math/rand"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// frame draws n Gaussian stars of the given sigma on a noisy background.
// transparency scales the stars (clouds make them fainter).
func frame(n int, sigma, transparency float64, seed int64) *fits.Image {
	const w, h = 800, 600
	rnd := rand.New(rand.NewSource(seed))
	d := make([]float32, w*h)
	for i := range d {
		d[i] = float32(0.05 + rnd.NormFloat64()*0.002)
	}
	for s := 0; s < n; s++ {
		cx, cy := 30+rnd.Float64()*(w-60), 30+rnd.Float64()*(h-60)
		u := rnd.Float64()
		amp := (0.02 + 0.5*u*u*u) * transparency // many faint stars, few bright ones
		r := int(5 * sigma)
		for y := int(cy) - r; y <= int(cy)+r; y++ {
			for x := int(cx) - r; x <= int(cx)+r; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				d[y*w+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
			}
		}
	}
	// hot pixels
	for i := 0; i < 200; i++ {
		d[rnd.Intn(len(d))] = 0.9
	}
	return &fits.Image{Width: w, Height: h, Channels: 1, Data: d, Binning: 1}
}

func TestMeasure(t *testing.T) {
	// the HFR of a Gaussian star is sigma·√(π/2)
	sharp := Measure(frame(150, 1.5, 1, 1))
	want := 1.5 * math.Sqrt(math.Pi/2)
	if math.Abs(sharp.HFR-want)/want > 0.2 {
		t.Errorf("sharp: HFR %.2f, want about %.2f", sharp.HFR, want)
	}
	if sharp.Stars < 110 || sharp.Stars > 150 {
		t.Errorf("sharp: %d stars of 150 (hot pixels must not count)", sharp.Stars)
	}
	soft := Measure(frame(150, 3, 1, 1))
	if soft.HFR < 1.6*sharp.HFR {
		t.Errorf("out of focus: HFR %.2f vs %.2f", soft.HFR, sharp.HFR)
	}
	clouds := Measure(frame(150, 1.5, 0.25, 1))
	if float64(clouds.Stars) > 0.7*float64(sharp.Stars) {
		t.Errorf("clouds: %d stars vs %d", clouds.Stars, sharp.Stars)
	}
	empty := Measure(frame(0, 1.5, 1, 2))
	if empty.Stars != 0 || empty.HFR != 0 {
		t.Errorf("empty frame: %+v", empty)
	}
	// binning is undone
	img := frame(150, 1.5, 1, 1)
	img.Binning = 2
	if b := Measure(img); math.Abs(b.HFR-2*sharp.HFR) > 1e-9 {
		t.Errorf("binned: %.2f", b.HFR)
	}
}

// A sensor that clips below the top of its range (0.94 here) leaves bright
// stars flat topped: they count as stars, but not for the size, as in Siril.
func TestSaturated(t *testing.T) {
	const w, h, sigma = 800, 600, 1.5
	draw := func(clip float32) *fits.Image {
		rnd := rand.New(rand.NewSource(3))
		d := make([]float32, w*h)
		for i := range d {
			d[i] = float32(0.05 + rnd.NormFloat64()*0.002)
		}
		for s := 0; s < 120; s++ {
			cx, cy := 40+rnd.Float64()*(w-80), 40+rnd.Float64()*(h-80)
			amp := 0.05 + 0.4*rnd.Float64()
			if s%4 == 0 {
				amp = 3 + 5*rnd.Float64() // far brighter than the sensor holds
			}
			for y := int(cy) - 12; y <= int(cy)+12; y++ {
				for x := int(cx) - 12; x <= int(cx)+12; x++ {
					dx, dy := float64(x)-cx, float64(y)-cy
					d[y*w+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
				}
			}
		}
		for i, v := range d {
			d[i] = min(v, clip)
		}
		return &fits.Image{Width: w, Height: h, Channels: 1, Data: d, Binning: 1, Max: []float32{clip}}
	}
	faint := Measure(draw(100)) // nothing clipped: the size of the faint stars
	clipped := draw(0.94)
	found := find(clipped)
	sat := len(found) - len(unsaturated(found))
	if sat < 25 || sat > 32 {
		t.Errorf("%d stars saturated of 30 drawn so", sat)
	}
	got := Measure(clipped)
	if got.Stars < 110 {
		t.Errorf("%d stars: saturated stars must count", got.Stars)
	}
	want := sigma * math.Sqrt(math.Pi/2)
	if math.Abs(got.HFR-want)/want > 0.15 {
		t.Errorf("HFR %.2f with saturated stars, want about %.2f (unclipped bright stars: %.2f)", got.HFR, want, faint.HFR)
	}
	// without the maximum of the frame, that of the image itself is taken
	clipped.Max = nil
	if n := len(find(clipped)) - len(unsaturated(find(clipped))); n != sat {
		t.Errorf("without Max: %d saturated, with: %d", n, sat)
	}
}

// shaped draws n Gaussian stars stretched sx × sy pixels (sigmas) along
// angle (radians from the rows), all the same way, or each its own way.
func shaped(n int, sx, sy, angle float64, random bool, seed int64) *fits.Image {
	const w, h = 800, 600
	rnd := rand.New(rand.NewSource(seed))
	d := make([]float32, w*h)
	for i := range d {
		d[i] = float32(0.05 + rnd.NormFloat64()*0.002)
	}
	for s := 0; s < n; s++ {
		cx, cy := 30+rnd.Float64()*(w-60), 30+rnd.Float64()*(h-60)
		amp := 0.05 + 0.3*rnd.Float64()
		a := angle
		if random {
			a = rnd.Float64() * math.Pi
		}
		ca, sa := math.Cos(a), math.Sin(a)
		for y := int(cy) - 15; y <= int(cy)+15; y++ {
			for x := int(cx) - 15; x <= int(cx)+15; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				u, v := dx*ca+dy*sa, -dx*sa+dy*ca
				d[y*w+x] += float32(amp * math.Exp(-u*u/(2*sx*sx)-v*v/(2*sy*sy)))
			}
		}
	}
	return &fits.Image{Width: w, Height: h, Channels: 1, Data: d, Binning: 1}
}

// Round stars are told round; stars trailed one way (wind, guiding) are
// told stretched, with the direction; stars stretched every way (optics)
// are told not aligned.
func TestRoundness(t *testing.T) {
	round := Measure(shaped(120, 2, 2, 0, false, 1))
	if round.Round < 0.9 {
		t.Errorf("round stars: %+v", round)
	}
	trail := Measure(shaped(120, 3.2, 2, 30*math.Pi/180, false, 1))
	if trail.Round > 0.75 || trail.Round < 0.5 || math.Abs(trail.Angle-30) > 5 || trail.Aligned < 0.7 {
		t.Errorf("trailed stars: %+v", trail)
	}
	t.Logf("round %+v\ntrail %+v", round, trail)
	optics := Measure(shaped(120, 3.2, 2, 0, true, 1))
	t.Logf("optics %+v", optics)
	if optics.Round > 0.75 || optics.Aligned > 0.45 {
		t.Errorf("stretched every way: %+v", optics)
	}
}
