package fits

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
)

// makeFITS builds a FITS file: cards are "KEY = value" strings.
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

// u16 encodes unsigned values the way cameras store them (BZERO=32768).
func u16(vals []uint16) []byte {
	out := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.BigEndian.PutUint16(out[i*2:], uint16(int16(int(v)-32768)))
	}
	return out
}

func TestHeader(t *testing.T) {
	f := makeFITS([]string{
		"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 4", "NAXIS2 = 2",
		"OBJECT = 'M 31 ''Andromeda'''  / target", "EXPTIME = 300.0 / s",
	}, u16(make([]uint16, 8)))
	h, err := ReadHeader(bytes.NewReader(f))
	if err != nil {
		t.Fatal(err)
	}
	if h.Get("OBJECT") != "M 31 'Andromeda'" || h.Get("EXPTIME") != "300.0" {
		t.Fatalf("header values: %q %q", h.Get("OBJECT"), h.Get("EXPTIME"))
	}
	if _, err := ReadHeader(strings.NewReader("hello")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPreviewMono16(t *testing.T) {
	const w, h = 400, 200
	vals := make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			vals[y*w+x] = uint16(x * 65535 / (w - 1)) // horizontal gradient
		}
	}
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2",
		fmt.Sprintf("NAXIS1 = %d", w), fmt.Sprintf("NAXIS2 = %d", h), "BZERO = 32768", "BSCALE = 1"}, u16(vals))
	im, err := Preview(bytes.NewReader(f), 100)
	if err != nil {
		t.Fatal(err)
	}
	if im.Width != 100 || im.Height != 50 || im.Channels != 1 || im.Binning != 4 {
		t.Fatalf("size %dx%d ch=%d bin=%d", im.Width, im.Height, im.Channels, im.Binning)
	}
	if l, r := im.Data[0], im.Data[99]; l > 0.02 || r < 0.98 {
		t.Fatalf("gradient not preserved: left %.3f right %.3f", l, r)
	}
}

func TestPreviewBayer(t *testing.T) {
	// RGGB: R pixels bright, G medium, B dark
	const w, h = 64, 32
	vals := make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case y%2 == 0 && x%2 == 0:
				vals[y*w+x] = 60000 // R
			case y%2 == 1 && x%2 == 1:
				vals[y*w+x] = 5000 // B
			default:
				vals[y*w+x] = 30000 // G
			}
		}
	}
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 64", "NAXIS2 = 32",
		"BZERO = 32768", "BAYERPAT = 'RGGB'"}, u16(vals))
	im, err := Preview(bytes.NewReader(f), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if im.Channels != 3 || im.Width != 32 || im.Height != 16 || im.Bayer != "RGGB" {
		t.Fatalf("bayer preview: %dx%d ch=%d %q", im.Width, im.Height, im.Channels, im.Bayer)
	}
	r, g, b := im.Data[0], im.Data[1], im.Data[2]
	if math.Abs(float64(r)-60000.0/65535) > 0.01 || math.Abs(float64(g)-30000.0/65535) > 0.01 || math.Abs(float64(b)-5000.0/65535) > 0.01 {
		t.Fatalf("debayer: r=%.3f g=%.3f b=%.3f", r, g, b)
	}

	// the same data read as BGGR swaps red and blue
	f2 := bytes.Replace(f, []byte("'RGGB'"), []byte("'BGGR'"), 1)
	im2, _ := Preview(bytes.NewReader(f2), 1000)
	if im2.Data[0] != b || im2.Data[2] != r {
		t.Fatal("pattern BGGR not applied")
	}
}

func TestPreviewFloatAndFlip(t *testing.T) {
	const w, h = 8, 4
	var data bytes.Buffer
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			binary.Write(&data, binary.BigEndian, float32(y)) // rows 0..3
		}
	}
	cards := []string{"SIMPLE = T", "BITPIX = -32", "NAXIS = 2", "NAXIS1 = 8", "NAXIS2 = 4"}
	im, err := Preview(bytes.NewReader(makeFITS(cards, data.Bytes())), 100)
	if err != nil {
		t.Fatal(err)
	}
	if im.Data[0] != 0 || im.Data[len(im.Data)-1] != 1 {
		t.Fatalf("float normalisation: first %.2f last %.2f", im.Data[0], im.Data[len(im.Data)-1])
	}
	im, _ = Preview(bytes.NewReader(makeFITS(append(cards, "ROWORDER = 'BOTTOM-UP'"), data.Bytes())), 100)
	if im.Data[0] != 1 {
		t.Fatalf("BOTTOM-UP not flipped: first %.2f", im.Data[0])
	}
}

func TestPreviewTruncated(t *testing.T) {
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 100", "NAXIS2 = 100"}, u16(make([]uint16, 10000)))
	if _, err := Preview(bytes.NewReader(f[:blockSize+500]), 50); err == nil {
		t.Fatal("truncated file accepted")
	}
}

// TestStreamMatchesPreview: the stream fed in odd pieces ends with the
// same picture as Preview, and shows a part of it on the way.
