package fits

import (
	"bytes"
	"testing"
)

func TestReadSaturated(t *testing.T) {
	const w, h = 100, 80
	vals := make([]uint16, w*h)
	for i := range vals {
		vals[i] = 1000
	}
	// a big saturated star at (30, 40): a checkerboard, as one colour of a
	// Bayer matrix saturates; a single pixel at (80, 10)
	for y := 36; y <= 44; y++ {
		for x := 26; x <= 34; x++ {
			if (x+y)%2 == 0 {
				vals[y*w+x] = 65535
			}
		}
	}
	vals[10*w+80] = 65535
	cards := []string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 100", "NAXIS2 = 80", "BZERO = 32768"}
	read := func(f []byte) *SaturatedAreas {
		t.Helper()
		a, err := ReadSaturated(bytes.NewReader(f), int64(len(f)))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := read(makeFITS(cards, u16(vals)))
	if a.Count != 2 || a.Pixels != 42 || a.Width != w || a.Height != h {
		t.Fatalf("%+v", a)
	}
	s := a.Spots[0]
	if s.Pixels != 41 || s.X != 30 || s.Y != 40 || s.R < 5 || s.R > 10 {
		t.Fatalf("%+v", s)
	}
	if s := a.Spots[1]; s.Pixels != 1 || s.X != 80 || s.Y != 10 {
		t.Fatalf("%+v", s)
	}
	// rows stored from the bottom: shown at 80-1-y
	a = read(makeFITS(append(cards, "ROWORDER = 'BOTTOM-UP'"), u16(vals)))
	if s := a.Spots[1]; s.X != 80 || s.Y != 69 {
		t.Fatalf("%+v", s)
	}
	// nothing saturated
	for i := range vals {
		vals[i] = 1000
	}
	if a := read(makeFITS(cards, u16(vals))); a.Count != 0 || a.Pixels != 0 {
		t.Fatalf("%+v", a)
	}
}
