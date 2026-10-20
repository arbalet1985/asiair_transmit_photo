package fits

import (
	"bytes"
	"io"
	"testing"
)

func TestReadSaturated(t *testing.T) {
	const w, h = 100, 80
	vals := make([]uint16, w*h)
	for i := range vals {
		vals[i] = 1000
	}
	// a big saturated star at (30, 40): a checkerboard, as one colour of a
	// Bayer matrix saturates; a single pixel at (80, 10) with no light
	// around: a hot pixel, left out
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
		a, err := ReadSaturated(bytes.NewReader(f), int64(len(f)), nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := read(makeFITS(cards, u16(vals)))
	if a.Count != 1 || a.Pixels != 41 || a.Hot != 1 || a.Width != w || a.Height != h {
		t.Fatalf("%+v", a)
	}
	s := a.Spots[0]
	if s.Pixels != 41 || s.X != 30 || s.Y != 40 || s.R < 5 || s.R > 10 {
		t.Fatalf("%+v", s)
	}
	// rows stored from the bottom: shown at 80-1-y
	a = read(makeFITS(append(cards, "ROWORDER = 'BOTTOM-UP'"), u16(vals)))
	if s := a.Spots[0]; s.X != 30 || s.Y != 39 || a.Hot != 1 {
		t.Fatalf("%+v", a)
	}
	// nothing saturated
	for i := range vals {
		vals[i] = 1000
	}
	if a := read(makeFITS(cards, u16(vals))); a.Count != 0 || a.Pixels != 0 {
		t.Fatalf("%+v", a)
	}
}

// A hot pixel next to some light is not lone: the darks tell it.
func TestHotFromDarks(t *testing.T) {
	const w, h = 60, 40
	cards := []string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 60", "NAXIS2 = 40", "BZERO = 32768",
		"INSTRUME = 'ZWO ASI533MC Pro'", "GAIN = 100", "EXPTIME = 300"}
	frame := func(set map[int]uint16) []byte {
		vals := make([]uint16, w*h)
		for i := range vals {
			vals[i] = 500
		}
		for i, v := range set {
			vals[i] = v
		}
		return makeFITS(cards, u16(vals))
	}
	// (20, 10) is hot in both darks, (40, 30) in one only
	d1 := frame(map[int]uint16{10*w + 20: 60000, 30*w + 40: 60000})
	d2 := frame(map[int]uint16{10*w + 20: 50000})
	hp, err := HotFromDarks([]io.ReaderAt{bytes.NewReader(d1), bytes.NewReader(d2)}, []int64{int64(len(d1)), int64(len(d2))})
	if err != nil || hp.Darks != 2 || hp.Count() != 1 {
		t.Fatalf("%v %+v", err, hp)
	}
	if !hp.has(20, 10) {
		t.Fatal("(20, 10) not hot")
	}
	// the light: saturated at (20, 10) with a bright neighbour, and at (40, 30) the same
	light := frame(map[int]uint16{10*w + 20: 65535, 10*w + 21: 30000, 30*w + 40: 65535, 30*w + 41: 30000})
	a, err := ReadSaturated(bytes.NewReader(light), int64(len(light)), func(*Header) *HotPixels { return hp })
	if err != nil {
		t.Fatal(err)
	}
	if a.Pixels != 1 || a.Hot != 1 || a.HotDarks != 2 {
		t.Fatalf("%+v", a)
	}
	a, _ = ReadSaturated(bytes.NewReader(light), int64(len(light)), nil)
	if a.Pixels != 2 || a.Hot != 0 {
		t.Fatalf("without darks: %+v", a)
	}
	// the statistics leave them out the same way, lone pixels too
	light = frame(map[int]uint16{10*w + 20: 65535, 10*w + 21: 30000, 30*w + 40: 65535, 30*w + 41: 30000, 5*w + 50: 65535})
	img, err := PreviewHot(bytes.NewReader(light), 60, func(*Header) *HotPixels { return hp })
	if err != nil {
		t.Fatal(err)
	}
	if st := img.Stats[0]; st.Saturated != 1 || st.Hot != 2 || img.HotDarks != 2 {
		t.Fatalf("%+v", st)
	}
	// a dark of another size is left out
	key, exp, _ := FrameTraits(img.Header)
	if key != "ZWO ASI533MC Pro|100|1|60x40" || exp != 300 {
		t.Fatalf("%q %v", key, exp)
	}
}
