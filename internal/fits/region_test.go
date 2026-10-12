package fits

import (
	"bytes"
	"testing"
)

// starFrame: a 100×80 frame, background 1000, a star at (60, 20) with the
// peak given and a ring of half of it around.
func starFrame(peak uint16, extra ...string) []byte {
	const w, h = 100, 80
	vals := make([]uint16, w*h)
	for i := range vals {
		vals[i] = 1000
	}
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			vals[(20+dy)*w+60+dx] = peak / 2
		}
	}
	vals[20*w+60] = peak
	cards := append([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 100", "NAXIS2 = 80", "BZERO = 32768"}, extra...)
	return makeFITS(cards, u16(vals))
}

func region(t *testing.T, f []byte, x, y, r int) *RegionStats {
	t.Helper()
	st, err := ReadRegion(bytes.NewReader(f), int64(len(f)), x, y, r)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReadRegion(t *testing.T) {
	// clicked a little off the star: the area is centred on it
	st := region(t, starFrame(40000), 55, 24, 5)
	if st.PeakX != 60 || st.PeakY != 20 || st.Peak != 40000 || st.X != 55 || st.Y != 15 || st.W != 11 || st.H != 11 {
		t.Fatalf("%+v", st)
	}
	if st.Saturated != 0 || st.Saturation < 64000 || st.Saturation > 65535 || st.Background != 1000 {
		t.Fatalf("%+v", st)
	}
	c := st.Channels[0]
	if c.Name != "K" || c.Count != 121 || c.Max != 40000 || c.Median != 1000 || c.Min != 1000 {
		t.Fatalf("%+v", c)
	}
	// saturated
	if st := region(t, starFrame(65535), 60, 20, 5); st.Saturated != 1 {
		t.Fatalf("saturated %d", st.Saturated)
	}
	// a saturated plateau: the area is centred on its middle, not its corner
	if st := region(t, starFrame(65535, "DATAMAX = 30000"), 57, 17, 5); st.Saturated != 9 || st.PeakX != 60 || st.PeakY != 20 || st.X != 55 {
		t.Fatalf("%+v", st)
	}
	// DATAMAX of the header counts
	if st := region(t, starFrame(30000, "DATAMAX = 10000"), 60, 20, 5); st.Saturation != 10000 || st.Saturated != 9 {
		t.Fatalf("%+v", st)
	}
	// rows stored from the bottom: shown at 80-1-20
	if st := region(t, starFrame(40000, "ROWORDER = 'BOTTOM-UP'"), 60, 59, 5); st.PeakY != 59 || st.Peak != 40000 {
		t.Fatalf("%+v", st)
	}
	// at the edge of the frame the area is cut
	if st := region(t, starFrame(40000), 0, 0, 5); st.X != 0 || st.Y != 0 || st.W > 6 || st.H > 6 {
		t.Fatalf("%+v", st)
	}
}

func TestReadRegionBayer(t *testing.T) {
	st := region(t, starFrame(40000, "BAYERPAT = 'RGGB'"), 60, 20, 4)
	if len(st.Channels) != 3 || st.Channels[0].Name != "R" {
		t.Fatalf("%+v", st.Channels)
	}
	// (60, 20) is on an R pixel of RGGB; of the 9×9 area 25 are R, 16 B
	if st.Channels[0].Max != 40000 || st.Channels[0].Count != 25 || st.Channels[1].Count != 40 || st.Channels[2].Count != 16 {
		t.Fatalf("%+v", st.Channels)
	}
}
