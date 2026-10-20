package fits

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The cache reads the darks of the camera closest to the frame, once.
func TestHotCache(t *testing.T) {
	const w, h = 40, 30
	dir := t.TempDir()
	frame := func(gain, exp string, hot ...int) []byte {
		vals := make([]uint16, w*h)
		for i := range vals {
			vals[i] = 500
		}
		for _, i := range hot {
			vals[i] = 60000
		}
		return makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 40", "NAXIS2 = 30", "BZERO = 32768",
			"INSTRUME = 'ASI533MC'", "GAIN = " + gain, "EXPTIME = " + exp}, u16(vals))
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	darks := []string{
		write("d300a.fit", frame("100", "300", 5*w+5)),
		write("d300b.fit", frame("100", "300", 5*w+5)),
		write("d60.fit", frame("100", "60", 7*w+7)),                        // another exposure: left out, two of 300 s are enough
		write("g200.fit", frame("200", "300", 9*w+9)),                      // another gain: never
		write("light.fit", frame("100", "300", 1*w+1, 2*w+2, 3*w+3)[:100]), // broken: not a dark of the key
	}
	var c HotCache
	c.SetDarks(darks)
	hdr, err := ReadHeader(bytes.NewReader(frame("100", "300")))
	if err != nil {
		t.Fatal(err)
	}
	hp, pending := c.For(hdr, true)
	if pending || hp == nil || hp.Darks != 2 || !hp.has(5, 5) || hp.has(7, 7) || hp.has(9, 9) {
		t.Fatalf("%v %+v", pending, hp)
	}
	// no darks of the gain
	hdr2, _ := ReadHeader(bytes.NewReader(frame("300", "300")))
	if hp, pending := c.For(hdr2, false); hp != nil || pending {
		t.Fatal("a map without darks")
	}
	// a new dark: found in the background, the old map meanwhile not given
	c.SetDarks(append(darks, write("d300c.fit", frame("100", "300", 5*w+5))))
	if _, pending := c.For(hdr, false); !pending {
		t.Fatal("not pending")
	}
	if hp, _ := c.For(hdr, true); hp == nil || hp.Darks != 3 {
		t.Fatalf("%+v", hp)
	}
}
