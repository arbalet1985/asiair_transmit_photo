package stars

import (
	"errors"
	"math"
	"os"
	"sort"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// Tilt of the sensor, measured as Siril's "tilt" command does: the stars of
// the four quarters of the frame are compared. A tilted sensor (or a bent
// focuser) leaves stars of one side bigger than of the other. Sizes are
// HFR in pixels of the original frame (FWHM is about twice the HFR).
type Tilt struct {
	Stars int     `json:"stars"`
	HFR   float64 `json:"hfr"` // of all stars
	// Corners: top left, top right, bottom left, bottom right, as shown
	Corners [4]float64 `json:"corners"`
	Counts  [4]int     `json:"counts"` // stars of each quarter
	Center  float64    `json:"center"` // within a quarter of the half diagonal
	Edge    float64    `json:"edge"`   // beyond three quarters of it
	Tilt    float64    `json:"tilt"`   // the biggest corner less the smallest
	Percent float64    `json:"percent"`
	OffAxis float64    `json:"off_axis"` // edge less centre: field curvature, coma
	Width   int        `json:"width"`
	Height  int        `json:"height"`
}

// ErrFewStars: some part of the frame has no stars.
var ErrFewStars = errors.New("мало звёзд: в каждой четверти кадра, в центре и по краям нужны звёзды")

// TiltFile reads a FITS file at its full resolution and measures the tilt.
func TiltFile(name string) (Tilt, error) {
	f, err := os.Open(name)
	if err != nil {
		return Tilt{}, err
	}
	defer f.Close()
	img, err := fits.Preview(f, 16000)
	if err != nil {
		return Tilt{}, err
	}
	return MeasureTilt(img)
}

// MeasureTilt finds the stars of an image and compares the four quarters.
func MeasureTilt(img *fits.Image) (Tilt, error) {
	b := float64(max(img.Binning, 1))
	w, h := float64(img.Width)*b, float64(img.Height)*b
	t := Tilt{Width: int(w), Height: int(h)}
	stars := find(img)
	t.Stars = len(stars)
	cx, cy := w/2, h/2
	r := math.Hypot(cx, cy)
	var all, centre, edge []float64
	var quarter [4][]float64
	for _, s := range stars {
		all = append(all, s.HFR)
		q := 0
		if s.X > cx {
			q++
		}
		if s.Y > cy {
			q += 2
		}
		quarter[q] = append(quarter[q], s.HFR)
		switch d := math.Hypot(s.X-cx, s.Y-cy); {
		case d < 0.25*r:
			centre = append(centre, s.HFR)
		case d > 0.75*r:
			edge = append(edge, s.HFR)
		}
	}
	for q := range quarter {
		t.Counts[q] = len(quarter[q])
		if len(quarter[q]) == 0 {
			return t, ErrFewStars
		}
	}
	if len(centre) == 0 || len(edge) == 0 {
		return t, ErrFewStars
	}
	t.HFR = trimmedMean(all)
	best, worst, sum := math.Inf(1), 0.0, 0.0
	for q := range quarter {
		v := trimmedMean(quarter[q])
		t.Corners[q] = v
		best, worst, sum = math.Min(best, v), math.Max(worst, v), sum+v
	}
	t.Center, t.Edge = trimmedMean(centre), trimmedMean(edge)
	t.Tilt = worst - best
	t.Percent = 100 * t.Tilt / (sum / 4)
	t.OffAxis = t.Edge - t.Center
	return t, nil
}

// trimmedMean is the mean of the middle half (a quarter cut off each end),
// as Siril takes it.
func trimmedMean(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	lo := int(math.Floor(0.25 * float64(len(s))))
	hi := len(s) - lo
	var sum float64
	for _, x := range s[lo:hi] {
		sum += x
	}
	return sum / float64(hi-lo)
}
