package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"testing"
)

// image returns 16-bit pixels like a sky frame of a 14-bit camera.
func image(n int) []byte {
	r := rand.New(rand.NewSource(1))
	b := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		v := uint16(1200+r.Intn(64)) << 2
		binary.BigEndian.PutUint16(b[i:], v)
	}
	return b
}

func roundTrip(t *testing.T, data []byte) int64 {
	t.Helper()
	enc := NewEncoder(bytes.NewReader(data))
	defer enc.Close()
	z, err := io.ReadAll(enc)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(bytes.NewReader(z), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%d bytes: data differ", len(data))
	}
	raw, sent := enc.Sent()
	if raw != int64(len(data)) || sent != int64(len(z)) {
		t.Fatalf("Sent() = %d, %d; want %d, %d", raw, sent, len(data), len(z))
	}
	return int64(len(z))
}

func TestRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, BlockSize - 1, BlockSize, BlockSize + 1, 3*BlockSize + 777} {
		roundTrip(t, image(n))
	}
}

func TestCompresses(t *testing.T) {
	data := image(4 * BlockSize)
	if z := roundTrip(t, data); z*3 > int64(len(data))*2 { // at least 1.5x
		t.Fatalf("%d bytes compressed to %d", len(data), z)
	}
}

// A transfer cut in the middle gives only whole blocks, all correct.
func TestTruncatedKeepsWholeBlocks(t *testing.T) {
	data := image(5 * BlockSize)
	enc := NewEncoder(bytes.NewReader(data))
	z, err := io.ReadAll(enc)
	enc.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{0, 10, len(z) / 3, len(z) / 2, len(z) - 10} {
		dec, err := NewDecoder(bytes.NewReader(z[:cut]), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(dec)
		dec.Close()
		if err == nil {
			t.Fatalf("cut at %d: no error", cut)
		}
		if len(got)%BlockSize != 0 || !bytes.Equal(got, data[:len(got)]) {
			t.Fatalf("cut at %d: %d bytes, not whole correct blocks", cut, len(got))
		}
	}
}

type failReader struct {
	r     io.Reader
	after int
}

func (f *failReader) Read(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("ASIAIR gone")
	}
	p = p[:min(len(p), f.after)]
	n, err := f.r.Read(p)
	f.after -= n
	return n, err
}

// A read error of the source reaches the request and no part block is sent.
func TestSourceError(t *testing.T) {
	data := image(3 * BlockSize)
	enc := NewEncoder(&failReader{r: bytes.NewReader(data), after: BlockSize + 100})
	defer enc.Close()
	z, err := io.ReadAll(enc)
	if err == nil || err.Error() != "ASIAIR gone" {
		t.Fatalf("error %v", err)
	}
	dec, _ := NewDecoder(bytes.NewReader(z), int64(len(data)))
	defer dec.Close()
	got, _ := io.ReadAll(dec)
	if !bytes.Equal(got, data[:BlockSize]) {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestShuffle(t *testing.T) {
	for n := 0; n < 9; n++ {
		src := make([]byte, n)
		for i := range src {
			src[i] = byte(i + 1)
		}
		s, u := make([]byte, n), make([]byte, n)
		shuffle(s, src)
		unshuffle(u, s)
		if !bytes.Equal(u, src) {
			t.Fatalf("%d: %v -> %v -> %v", n, src, s, u)
		}
	}
}

func TestAcceptsCompressible(t *testing.T) {
	if !Accepts("gzip, "+Encoding) || Accepts("gzip") || Accepts("") {
		t.Fatal("Accepts")
	}
	if !Compressible("EMMC Images/Light/a.FIT") || Compressible("a/b.jpg") {
		t.Fatal("Compressible")
	}
}
