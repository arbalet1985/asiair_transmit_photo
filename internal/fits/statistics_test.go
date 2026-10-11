package fits

import (
	"bytes"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// direct: the statistics computed plainly from the values.
func direct(v []float64) ChannelStats {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	med := s[(len(s)-1)/2]
	var sum, ad float64
	dev := make([]float64, len(s))
	for i, x := range s {
		sum += x
		dev[i] = math.Abs(x - med)
		ad += dev[i]
	}
	mean := sum / float64(len(s))
	var ss float64
	for _, x := range s {
		ss += (x - mean) * (x - mean)
	}
	sort.Float64s(dev)
	return ChannelStats{Count: int64(len(s)), Mean: mean, Median: med, AvgDev: ad / float64(len(s)),
		MAD: dev[(len(dev)-1)/2], StdDev: math.Sqrt(ss / float64(len(s)-1)), Min: s[0], Max: s[len(s)-1]}
}

func sameStats(t *testing.T, got, want ChannelStats) {
	t.Helper()
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b)) }
	if got.Count != want.Count || !near(got.Mean, want.Mean) || !near(got.Median, want.Median) || !near(got.AvgDev, want.AvgDev) ||
		!near(got.MAD, want.MAD) || !near(got.StdDev, want.StdDev) || got.Min != want.Min || got.Max != want.Max {
		t.Fatalf("%s:\n got %+v\nwant %+v", got.Name, got, want)
	}
}

func TestStatsMono16(t *testing.T) {
	const w, h = 301, 203 // odd sizes: rows and columns outside the preview count too
	rnd := rand.New(rand.NewSource(2))
	vals := make([]uint16, w*h)
	all := make([]float64, w*h)
	for i := range vals {
		vals[i] = uint16(500 + rnd.Intn(3000))
		all[i] = float64(vals[i])
	}
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 301", "NAXIS2 = 203", "BZERO = 32768"}, u16(vals))
	im, err := Preview(bytes.NewReader(f), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Stats) != 1 || im.Stats[0].Name != "K" {
		t.Fatalf("%+v", im.Stats)
	}
	sameStats(t, im.Stats[0], direct(all))
}

func TestStatsBayer16(t *testing.T) {
	const w, h = 130, 98
	rnd := rand.New(rand.NewSource(3))
	vals := make([]uint16, w*h)
	var by [3][]float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := 1
			if y%2 == 0 && x%2 == 0 {
				c = 0
			} else if y%2 == 1 && x%2 == 1 {
				c = 2
			}
			v := uint16(1000*(c+1) + rnd.Intn(500))
			vals[y*w+x] = v
			by[c] = append(by[c], float64(v))
		}
	}
	f := makeFITS([]string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 130", "NAXIS2 = 98",
		"BZERO = 32768", "BAYERPAT = 'RGGB'"}, u16(vals))
	im, err := Preview(bytes.NewReader(f), 20)
	if err != nil {
		t.Fatal(err)
	}
	for c, name := range []string{"R", "G", "B"} {
		want := direct(by[c])
		want.Name = name
		if im.Stats[c].Name != name {
			t.Fatalf("channel %d: %q", c, im.Stats[c].Name)
		}
		sameStats(t, im.Stats[c], want)
	}
}

// the fast 16-bit path bins like the general one
func TestPreview16MatchesGeneral(t *testing.T) {
	const w, h = 200, 120
	rnd := rand.New(rand.NewSource(4))
	vals := make([]uint16, w*h)
	var fl bytes.Buffer
	for i := range vals {
		vals[i] = uint16(rnd.Intn(65536))
		x := math.Float32bits(float32(vals[i]))
		fl.Write([]byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)})
	}
	for _, bayer := range []string{"", "BAYERPAT = 'GBRG'"} {
		cards := []string{"SIMPLE = T", "BITPIX = 16", "NAXIS = 2", "NAXIS1 = 200", "NAXIS2 = 120", "BZERO = 32768"}
		fcards := []string{"SIMPLE = T", "BITPIX = -32", "NAXIS = 2", "NAXIS1 = 200", "NAXIS2 = 120",
			"DATAMIN = 0", "DATAMAX = 65535"}
		if bayer != "" {
			cards, fcards = append(cards, bayer), append(fcards, bayer)
		}
		a, err := Preview(bytes.NewReader(makeFITS(cards, u16(vals))), 30)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Preview(bytes.NewReader(makeFITS(fcards, fl.Bytes())), 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Data) != len(b.Data) || a.Width != b.Width || a.Channels != b.Channels {
			t.Fatalf("%q: sizes differ", bayer)
		}
		// float data is normalised by the range of its preview, the 16-bit
		// by the range of the type
		var lo, hi float64 = 1, 0
		for _, v := range a.Data {
			lo, hi = math.Min(lo, float64(v)), math.Max(hi, float64(v))
		}
		for i := range a.Data {
			want := float64(b.Data[i])*(hi-lo) + lo
			if math.Abs(float64(a.Data[i])-want) > 1e-4 {
				t.Fatalf("%q: sample %d: %v, want %v", bayer, i, a.Data[i], want)
			}
		}
		if !b.Stats[0].NoMedian || a.Stats[0].NoMedian || math.Abs(a.Stats[0].Mean-b.Stats[0].Mean) > 1e-3 {
			t.Fatalf("%q: stats %+v vs %+v", bayer, a.Stats[0], b.Stats[0])
		}
	}
}
