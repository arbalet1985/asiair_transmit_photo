package fits

import (
	"bytes"
	"math/rand"
	"testing"
)

// BenchmarkPreviewBayer: a 533MC frame (3008×3008, RGGB) to a screen preview.
func BenchmarkPreviewBayer(b *testing.B) {
	const w, h = 3008, 3008
	rnd := rand.New(rand.NewSource(1))
	vals := make([]uint16, w*h)
	for i := range vals {
		vals[i] = uint16(1000 + rnd.Intn(4000))
	}
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 3008", "NAXIS2 = 3008",
		"BZERO = 32768", "BAYERPAT = 'RGGB'"}, u16(vals))
	b.SetBytes(int64(len(f)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Preview(bytes.NewReader(f), 1600); err != nil {
			b.Fatal(err)
		}
	}
}
