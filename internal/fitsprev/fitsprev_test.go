package fitsprev

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

func makeFITS(cards []string, data []byte) []byte {
	var b bytes.Buffer
	for _, c := range append(cards, "END") {
		if i := strings.Index(c, "="); i > 0 && c != "END" {
			key := strings.TrimSpace(c[:i])
			c = fmt.Sprintf("%-8s= %s", key, strings.TrimSpace(c[i+1:]))
		}
		b.WriteString(fmt.Sprintf("%-80s", c))
	}
	for b.Len()%blockSize != 0 {
		b.WriteByte(' ')
	}
	b.Write(data)
	for b.Len()%blockSize != 0 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func TestStreamMatchesPreview(t *testing.T) {
	w, h := 64, 40
	var data bytes.Buffer
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			binary.Write(&data, binary.BigEndian, int16(x*100+y*10))
		}
	}
	f := makeFITS([]string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2", "NAXIS1  = 64", "NAXIS2  = 40", "BZERO   = 32768"}, data.Bytes())
	want, err := fits.Preview(bytes.NewReader(f), 16)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStream(16)
	half := len(f) / 2
	for i := 0; i < half; i += 777 {
		s.Write(f[i:min(i+777, half)])
	}
	part, full, _ := s.Snapshot()
	if part == nil || part.Height >= full || full != want.Height {
		t.Fatalf("half way: %+v of %d", part, full)
	}
	s.Write(f[half:])
	got, _, _ := s.Snapshot()
	if got.Width != want.Width || got.Height != want.Height || len(got.Data) != len(want.Data) {
		t.Fatalf("size %dx%d, want %dx%d", got.Width, got.Height, want.Width, want.Height)
	}
	for i := range got.Data {
		if math.Abs(float64(got.Data[i]-want.Data[i])) > 1e-6 {
			t.Fatalf("pixel %d: %v, want %v", i, got.Data[i], want.Data[i])
		}
	}
	if p := s.Progress(); p != 1 {
		t.Fatalf("progress %v", p)
	}
	// not a FITS file: ignored
	j := NewStream(16)
	j.Write(bytes.Repeat([]byte{0xff}, 10000))
	if im, _, _ := j.Snapshot(); im != nil {
		t.Fatal("preview of a non-FITS file")
	}
}

// The picture in the tray is the one of the receiver's viewer: the same
// binning, debayering and auto stretch as package fits.
func TestSameAsFits(t *testing.T) {
	w, h := 96, 64
	var data bytes.Buffer
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 1000 + x*37 + y*91 + (x%2)*700 + (y%2)*300
			if (x*7+y*13)%97 == 0 {
				v = 60000 // a star
			}
			binary.Write(&data, binary.BigEndian, uint16(v))
		}
	}
	for _, bayer := range []string{"", "RGGB"} {
		cards := []string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2", "NAXIS1  = 96", "NAXIS2  = 64", "BZERO   = 32768", "ROWORDER= 'BOTTOM-UP'"}
		if bayer != "" {
			cards = append(cards, "BAYERPAT= '"+bayer+"'")
		}
		raw := data.Bytes()
		d := make([]byte, len(raw))
		for i := 0; i < len(raw); i += 2 { // stored with BZERO 32768
			binary.BigEndian.PutUint16(d[i:], binary.BigEndian.Uint16(raw[i:])-32768)
		}
		f := makeFITS(cards, d)
		want, err := fits.Preview(bytes.NewReader(f), 24)
		if err != nil {
			t.Fatal(err)
		}
		s := NewStream(24)
		s.Write(f)
		got, _, _ := s.Snapshot()
		if got == nil || got.Channels != want.Channels || got.Width != want.Width || got.Height != want.Height || got.Bayer != want.Bayer {
			t.Fatalf("%q: %+v, want %dx%dx%d %q", bayer, got, want.Width, want.Height, want.Channels, want.Bayer)
		}
		a, b := got.Stretched(), want.Stretched()
		for y := 0; y < got.Height; y++ {
			for x := 0; x < got.Width; x++ {
				r1, g1, b1, _ := a.At(x, y).RGBA()
				r2, g2, b2, _ := b.At(x, y).RGBA()
				if r1 != r2 || g1 != g2 || b1 != b2 {
					t.Fatalf("%q: pixel %d,%d differs", bayer, x, y)
				}
			}
		}
	}
}
