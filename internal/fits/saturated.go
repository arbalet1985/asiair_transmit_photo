package fits

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// SaturatedSpot is one saturated area of a frame: saturated pixels that
// touch (on cells of 2×2 pixels, so that the colours of a Bayer matrix make
// one area), in pixels of the frame from the top left as shown.
type SaturatedSpot struct {
	X      float64 `json:"x"` // the middle of its saturated pixels
	Y      float64 `json:"y"`
	R      float64 `json:"r"` // the radius of a circle around it
	Pixels int     `json:"pixels"`
}

// SaturatedAreas are the saturated areas of a whole frame, the biggest
// first.
type SaturatedAreas struct {
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Saturation float64         `json:"saturation"`
	Pixels     int64           `json:"pixels"` // all saturated pixels
	Count      int             `json:"count"`  // all areas, Spots may be fewer
	Spots      []SaturatedSpot `json:"spots"`
}

// MaxSaturatedSpots: the areas told at most, the biggest.
const MaxSaturatedSpots = 500

// spot: an area being gathered (union-find over runs of cells).
type spot struct {
	parent                 int
	minX, maxX, minY, maxY int
	n                      int
	sx, sy                 float64
}

// run: saturated cells next to each other in a row of cells.
type run struct{ a, b, id int }

// ReadSaturated reads the whole frame row by row and finds its saturated
// areas: pixels at or above the level of saturation (as the statistics of
// the frame count them), in any colour.
func ReadSaturated(f io.ReaderAt, size int64) (*SaturatedAreas, error) {
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
		planes = min(h.int("NAXIS3", 1), 3)
	}
	if h.int("NAXIS", 0) < 2 || w <= 0 || ht <= 0 {
		return nil, errors.New("в файле нет изображения")
	}
	bpp := map[int]int{8: 1, 16: 2, 32: 4, -32: 4, -64: 8}[bitpix]
	if bpp == 0 {
		return nil, fmt.Errorf("неподдерживаемый BITPIX=%d", bitpix)
	}
	bscale, bzero := h.float("BSCALE", 1), h.float("BZERO", 0)
	sat := saturation(h, bitpix, bscale, bzero)
	if sat <= 0 {
		return nil, errors.New("уровень насыщения неизвестен (данные с плавающей точкой)")
	}
	bottomUp := strings.EqualFold(h.Get("ROWORDER"), "BOTTOM-UP")
	res := &SaturatedAreas{Width: w, Height: ht, Saturation: sat}

	cw := (w + 1) / 2
	cnt := make([]int, cw) // saturated pixels of each cell of the row of cells
	sx := make([]float64, cw)
	sy := make([]float64, cw)
	row := make([]byte, w*bpp)
	dec := make([]float64, w)
	mask := make([]bool, w)
	planeBytes := int64(w) * int64(ht) * int64(bpp)
	var spots []spot
	var prev, cur []run
	find := func(i int) int {
		for spots[i].parent != i {
			spots[i].parent = spots[spots[i].parent].parent
			i = spots[i].parent
		}
		return i
	}
	union := func(a, b int) int {
		a, b = find(a), find(b)
		if a == b {
			return a
		}
		if spots[a].n < spots[b].n {
			a, b = b, a
		}
		s, t := &spots[a], &spots[b]
		t.parent = a
		s.minX, s.maxX = min(s.minX, t.minX), max(s.maxX, t.maxX)
		s.minY, s.maxY = min(s.minY, t.minY), max(s.maxY, t.maxY)
		s.n += t.n
		s.sx += t.sx
		s.sy += t.sy
		return a
	}
	// endCells joins the saturated cells of the row of cells cy to the areas
	endCells := func(cy int) {
		cur = cur[:0]
		for x := 0; x < cw; {
			if cnt[x] == 0 {
				x++
				continue
			}
			a := x
			id := len(spots)
			sp := spot{parent: id, minX: 2 * x, minY: 2 * cy, maxY: min(2*cy+1, ht-1)}
			for ; x < cw && cnt[x] > 0; x++ {
				sp.n += cnt[x]
				sp.sx += sx[x]
				sp.sy += sy[x]
				cnt[x], sx[x], sy[x] = 0, 0, 0
			}
			sp.maxX = min(2*x-1, w-1)
			spots = append(spots, sp)
			cur = append(cur, run{a, x - 1, id})
		}
		// runs of the row above touching (diagonally too) are the same area
		j := 0
		for i := range cur {
			for j < len(prev) && prev[j].b < cur[i].a-1 {
				j++
			}
			for k := j; k < len(prev) && prev[k].a <= cur[i].b+1; k++ {
				cur[i].id = union(cur[i].id, prev[k].id)
			}
		}
		prev, cur = cur, prev
	}
	for y := 0; y < ht; y++ {
		fy := y
		if bottomUp {
			fy = ht - 1 - y
		}
		for p := 0; p < planes; p++ {
			off := start + int64(p)*planeBytes + int64(fy)*int64(w)*int64(bpp)
			if _, err := f.ReadAt(row, off); err != nil {
				return nil, fmt.Errorf("файл обрезан: %w", err)
			}
			decodeRow(row, bitpix, bscale, bzero, dec)
			for x, v := range dec {
				// a pixel saturated in several colours counts once
				mask[x] = mask[x] || v >= sat
			}
		}
		for x, m := range mask {
			if m {
				cnt[x/2]++
				sx[x/2] += float64(x)
				sy[x/2] += float64(y)
				mask[x] = false
			}
		}
		if y%2 == 1 || y == ht-1 {
			endCells(y / 2)
		}
	}
	for i := range spots {
		if find(i) != i {
			continue
		}
		s := spots[i]
		res.Pixels += int64(s.n)
		cx, cy := s.sx/float64(s.n), s.sy/float64(s.n)
		// around the whole area, a little out of it
		r := 0.0
		for _, c := range [][2]float64{{float64(s.minX), float64(s.minY)}, {float64(s.maxX + 1), float64(s.minY)},
			{float64(s.minX), float64(s.maxY + 1)}, {float64(s.maxX + 1), float64(s.maxY + 1)}} {
			r = math.Max(r, math.Hypot(c[0]-cx-0.5, c[1]-cy-0.5))
		}
		res.Spots = append(res.Spots, SaturatedSpot{X: cx, Y: cy, R: r + 2, Pixels: s.n})
	}
	sort.Slice(res.Spots, func(i, j int) bool { return res.Spots[i].Pixels > res.Spots[j].Pixels })
	res.Count = len(res.Spots)
	if len(res.Spots) > MaxSaturatedSpots {
		res.Spots = res.Spots[:MaxSaturatedSpots]
	}
	return res, nil
}
