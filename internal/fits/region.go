package fits

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// RegionStats are the statistics of a small area of a frame, read from the
// file itself (not the preview, where binning lowers the peaks of stars):
// to tell whether a star is saturated.
type RegionStats struct {
	// the area, in pixels of the frame counted from the top left as shown
	X      int `json:"x"`
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
	Width  int `json:"width"` // of the frame
	Height int `json:"height"`
	// the brightest pixel of the area, its value in the units of the file
	PeakX int     `json:"peak_x"`
	PeakY int     `json:"peak_y"`
	Peak  float64 `json:"peak"`
	// per channel as the frame statistics: K, or R, G, B of the Bayer matrix
	Channels []ChannelStats `json:"channels"`
	// Background: the median of the ring around the area
	Background float64 `json:"background"`
	// Saturation: the level counted as saturated (0 = unknown, floating
	// point data); Saturated: pixels of the area at or above it
	Saturation float64 `json:"saturation"`
	Saturated  int     `json:"saturated"`
}

// regionSnap: the area is centred on the brightest pixel this near the
// point asked, so the click need not hit the star exactly.
const regionSnap = 8

// ReadRegion reads the area of radius r around the brightest pixel near
// cx, cy (pixels of the frame from the top left, as shown) and counts its
// statistics. Only the rows of the area are read.
func ReadRegion(f io.ReaderAt, size int64, cx, cy, r int) (*RegionStats, error) {
	sr := io.NewSectionReader(f, 0, size)
	h, err := ReadHeader(sr)
	if err != nil {
		return nil, err
	}
	start, _ := sr.Seek(0, io.SeekCurrent)
	bitpix := h.int("BITPIX", 0)
	w, ht := h.int("NAXIS1", 0), h.int("NAXIS2", 0)
	planes := 1
	if h.int("NAXIS", 0) >= 3 {
		planes = h.int("NAXIS3", 1)
	}
	if h.int("NAXIS", 0) < 2 || w <= 0 || ht <= 0 {
		return nil, errors.New("в файле нет изображения")
	}
	bpp := map[int]int{8: 1, 16: 2, 32: 4, -32: 4, -64: 8}[bitpix]
	if bpp == 0 {
		return nil, fmt.Errorf("неподдерживаемый BITPIX=%d", bitpix)
	}
	bscale, bzero := h.float("BSCALE", 1), h.float("BZERO", 0)
	var pattern [2][2]int
	isBayer := planes == 1 && h.Get("BAYERPAT") != ""
	if isBayer {
		if pattern, err = bayerIndex(h.Get("BAYERPAT"), h.int("XBAYROFF", 0), h.int("YBAYROFF", 0)); err != nil {
			isBayer = false
		}
	}
	channels := 1
	if isBayer || planes == 3 {
		channels = 3
	}
	usePlanes := min(planes, channels)
	bottomUp := strings.EqualFold(h.Get("ROWORDER"), "BOTTOM-UP")
	r = min(max(r, 1), 100)
	cx, cy = min(max(cx, 0), w-1), min(max(cy, 0), ht-1)

	// the rows and columns that may be needed: the snap, the area and its ring
	reach := regionSnap + 2*r
	x0, x1 := max(0, cx-reach), min(w-1, cx+reach)
	y0, y1 := max(0, cy-reach), min(ht-1, cy+reach)
	bw, bh := x1-x0+1, y1-y0+1
	// vals[p][y][x] of the block, rows as shown
	vals := make([][]float64, usePlanes)
	row := make([]byte, bw*bpp)
	dec := make([]float64, bw)
	planeBytes := int64(w) * int64(ht) * int64(bpp)
	for p := range vals {
		vals[p] = make([]float64, bw*bh)
		for y := y0; y <= y1; y++ {
			fy := y
			if bottomUp {
				fy = ht - 1 - y
			}
			off := start + int64(p)*planeBytes + (int64(fy)*int64(w)+int64(x0))*int64(bpp)
			if _, err := f.ReadAt(row, off); err != nil {
				return nil, fmt.Errorf("файл обрезан: %w", err)
			}
			decodeRow(row, bitpix, bscale, bzero, dec)
			copy(vals[p][(y-y0)*bw:], dec)
		}
	}
	at := func(p, x, y int) float64 { return vals[p][(y-y0)*bw+x-x0] }
	// the colour of a pixel: its plane, or its place on the Bayer matrix
	colour := func(p, x, y int) int {
		if !isBayer {
			return p
		}
		fy := y
		if bottomUp {
			fy = ht - 1 - y
		}
		return pattern[fy%2][x%2]
	}

	st := &RegionStats{Width: w, Height: ht, Peak: math.Inf(-1)}
	for y := max(0, cy-regionSnap); y <= min(ht-1, cy+regionSnap); y++ {
		for x := max(0, cx-regionSnap); x <= min(w-1, cx+regionSnap); x++ {
			for p := range usePlanes {
				if v := at(p, x, y); v > st.Peak {
					st.Peak, st.PeakX, st.PeakY = v, x, y
				}
			}
		}
	}
	st.Saturation = saturation(h, bitpix, bscale, bzero)
	px, py := st.PeakX, st.PeakY
	if st.Saturation > 0 && st.Peak >= st.Saturation {
		// a saturated star is a plateau: its middle, not its corner
		var sx, sy, n int
		for y := max(0, cy-regionSnap); y <= min(ht-1, cy+regionSnap); y++ {
			for x := max(0, cx-regionSnap); x <= min(w-1, cx+regionSnap); x++ {
				for p := range usePlanes {
					if at(p, x, y) >= st.Saturation {
						sx, sy, n = sx+x, sy+y, n+1
						break
					}
				}
			}
		}
		px, py = (sx+n/2)/n, (sy+n/2)/n
		st.PeakX, st.PeakY = px, py
	}
	st.X, st.Y = max(0, px-r), max(0, py-r)
	st.W, st.H = min(w-1, px+r)-st.X+1, min(ht-1, py+r)-st.Y+1

	per := make([][]float64, channels)
	var ring []float64
	for y := max(y0, py-2*r); y <= min(y1, py+2*r); y++ {
		for x := max(x0, px-2*r); x <= min(x1, px+2*r); x++ {
			in := x >= st.X && x < st.X+st.W && y >= st.Y && y < st.Y+st.H
			for p := range usePlanes {
				v := at(p, x, y)
				if math.IsNaN(v) {
					continue
				}
				if !in {
					ring = append(ring, v)
					continue
				}
				c := colour(p, x, y)
				per[c] = append(per[c], v)
				if v > st.Peak {
					st.Peak, st.PeakX, st.PeakY = v, x, y
				}
				if st.Saturation > 0 && v >= st.Saturation {
					st.Saturated++
				}
			}
		}
	}
	for c, name := range channelNames(channels) {
		st.Channels = append(st.Channels, sampleStats(name, per[c]))
	}
	if len(ring) > 0 {
		sort.Float64s(ring)
		st.Background = ring[len(ring)/2]
	}
	return st, nil
}

// saturation: the level counted as saturated: DATAMAX or SATURATE of the
// header, else just below the top of the integer type (12- and 14-bit
// cameras stretched to 16 bits stop at 65520); unknown for floating point.
func saturation(h *Header, bitpix int, bscale, bzero float64) float64 {
	for _, k := range []string{"SATURATE", "DATAMAX"} {
		if v := h.float(k, 0); v > 0 {
			return v
		}
	}
	var lo, hi float64
	switch bitpix {
	case 8:
		lo, hi = bzero, bzero+255*bscale
	case 16:
		lo, hi = bzero-32768*bscale, bzero+32767*bscale
	case 32:
		lo, hi = bzero-2147483648*bscale, bzero+2147483647*bscale
	default:
		return 0
	}
	if bscale < 0 {
		return 0
	}
	return hi - 0.01*(hi-lo)
}

// sampleStats: the statistics of a few samples, counted exactly.
func sampleStats(name string, v []float64) ChannelStats {
	st := ChannelStats{Name: name, Count: int64(len(v))}
	if len(v) == 0 {
		return st
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	n := float64(len(s))
	st.Mean, st.Min, st.Max = sum/n, s[0], s[len(s)-1]
	st.Median = median(s)
	dev := make([]float64, len(s))
	var ss, ad float64
	for i, x := range s {
		d := x - st.Mean
		ss += d * d
		dev[i] = math.Abs(x - st.Median)
		ad += dev[i]
	}
	sort.Float64s(dev)
	st.AvgDev, st.MAD = ad/n, median(dev)
	if len(s) > 1 {
		st.StdDev = math.Sqrt(ss / (n - 1))
	}
	return st
}

func median(s []float64) float64 {
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
