package fits

import (
	"bytes"
	"math"
	"testing"
)

func TestFramingOf(t *testing.T) {
	const s = 0.001 // degrees per pixel
	cards := []string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 400", "NAXIS2 = 200",
		"CRVAL1 = 100", "CRVAL2 = 20", "CRPIX1 = 200.5", "CRPIX2 = 100.5",
		"CD1_1 = -0.001", "CD1_2 = 0", "CD2_1 = 0", "CD2_2 = 0.001"}
	hdr := func(extra ...string) *Header {
		h, err := ReadHeader(bytes.NewReader(makeFITS(append(cards, extra...), make([]byte, 400*200*2))))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	near := func(a, b, e float64) bool { return math.Abs(a-b) < e }
	// rows from the top: the file's row 0 is shown on top, and the rows go
	// north with y, so north is down
	f, ok := FramingOf(hdr(), "x.fit", 100, 50, 4)
	if !ok || !near(f.RA, 100, 1e-6) || !near(f.Dec, 20, 1e-6) || !near(f.Angle, 180, 0.01) ||
		!near(f.Width, 400*s, 1e-5) || !near(f.Height, 200*s, 1e-5) || f.Approx {
		t.Fatalf("%+v", f)
	}
	// the picture: 4 frame pixels a pixel; up is south, right is west
	if !near(f.WCS["CD1_1"].(float64), -4*s, 1e-7) || !near(f.WCS["CD2_2"].(float64), -4*s, 1e-7) || f.WCS["NAXIS1"] != 100 {
		t.Fatalf("%v", f.WCS)
	}
	// rows from the bottom: north up
	f, _ = FramingOf(hdr("ROWORDER = 'BOTTOM-UP'"), "x.fit", 100, 50, 4)
	if !near(f.Angle, 0, 0.01) && !near(f.Angle, 360, 0.01) || !near(f.WCS["CD2_2"].(float64), 4*s, 1e-7) {
		t.Fatalf("bottom-up: %+v", f)
	}
	// no solution, no pointing: nothing
	if _, ok := FramingOf(&Header{keys: map[string]string{}}, "x.fit", 1, 1, 1); ok {
		t.Fatal("framing of nothing")
	}
}
