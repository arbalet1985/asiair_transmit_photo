package stars

import (
	"math"
	"sort"
)

// TiltSummary is the tilt of a camera (one optical train) over many frames:
// each frame's quarters are taken relative to its own mean, so that seeing,
// focus and the filter, which change the size of all stars alike, drop out;
// frames whose tilt is far from the others (wind, a passing cloud, a bump)
// are left out; the corners are the medians of the rest.
type TiltSummary struct {
	Frames int `json:"frames"` // measured
	Used   int `json:"used"`   // after the outliers are left out
	// Corners: top left, top right, bottom left, bottom right, as shown;
	// HFR in pixels at the median HFR of the frames used
	Corners [4]float64 `json:"corners"`
	HFR     float64    `json:"hfr"`
	Tilt    float64    `json:"tilt"`    // the biggest corner less the smallest
	Percent float64    `json:"percent"` // of the mean of the corners
	// Spread: how much the tilt of single frames differs (median absolute
	// deviation of their percent): a small one means the tilt is real
	Spread  float64 `json:"spread"`
	OffAxis float64 `json:"off_axis"` // edge less centre at the median HFR
}

// minTiltFrames: fewer frames than this are no summary.
const minTiltFrames = 3

// SummarizeTilt makes the summary of the tilts of frames of one camera;
// false when there are too few of them.
func SummarizeTilt(ts []Tilt) (TiltSummary, bool) {
	var s TiltSummary
	s.Frames = len(ts)
	if len(ts) < minTiltFrames {
		return s, false
	}
	type rel struct {
		c   [4]float64 // corners over their mean
		pct float64
		off float64 // edge less centre over the mean
		hfr float64
	}
	rs := make([]rel, 0, len(ts))
	for _, t := range ts {
		mean := (t.Corners[0] + t.Corners[1] + t.Corners[2] + t.Corners[3]) / 4
		if mean <= 0 {
			continue
		}
		var r rel
		for q := range t.Corners {
			r.c[q] = t.Corners[q] / mean
		}
		r.pct, r.off, r.hfr = t.Percent, (t.Edge-t.Center)/mean, mean
		rs = append(rs, r)
	}
	if len(rs) < minTiltFrames {
		return s, false
	}
	// the outliers: frames whose corners lie far from the median ones
	var med [4]float64
	for q := range med {
		med[q] = median(func(i int) float64 { return rs[i].c[q] }, len(rs))
	}
	dist := make([]float64, len(rs))
	for i, r := range rs {
		for q := range r.c {
			dist[i] = math.Max(dist[i], math.Abs(r.c[q]-med[q]))
		}
	}
	mad := median(func(i int) float64 { return dist[i] }, len(dist))
	lim := math.Max(3*1.4826*mad, 0.02) // 2% of the HFR at least
	var used []rel
	for i, r := range rs {
		if dist[i] <= lim {
			used = append(used, r)
		}
	}
	if len(used) < minTiltFrames {
		used = rs
	}
	s.Used = len(used)
	s.HFR = median(func(i int) float64 { return used[i].hfr }, len(used))
	best, worst, sum := math.Inf(1), 0.0, 0.0
	for q := range s.Corners {
		v := s.HFR * median(func(i int) float64 { return used[i].c[q] }, len(used))
		s.Corners[q] = v
		best, worst, sum = math.Min(best, v), math.Max(worst, v), sum+v
	}
	s.Tilt = worst - best
	s.Percent = 100 * s.Tilt / (sum / 4)
	s.OffAxis = s.HFR * median(func(i int) float64 { return used[i].off }, len(used))
	mp := median(func(i int) float64 { return used[i].pct }, len(used))
	s.Spread = 1.4826 * median(func(i int) float64 { return math.Abs(used[i].pct - mp) }, len(used))
	return s, true
}

// Bigger tells in words where the stars are bigger ("" when they are much
// the same everywhere): a side, or a corner.
func (s TiltSummary) Bigger() string {
	c := s.Corners
	mean := (c[0] + c[1] + c[2] + c[3]) / 4
	if mean <= 0 || s.Percent < 5 {
		return ""
	}
	// the gradient across the frame: right less left, bottom less top
	gx := (c[1] + c[3] - c[0] - c[2]) / 2 / mean
	gy := (c[2] + c[3] - c[0] - c[1]) / 2 / mean
	const minor = 0.4 // a component under this share of the other is left out
	var v, h string
	if math.Abs(gy) >= minor*math.Abs(gx) {
		v = map[bool]string{true: "снизу", false: "сверху"}[gy > 0]
	}
	if math.Abs(gx) >= minor*math.Abs(gy) {
		h = map[bool]string{true: "справа", false: "слева"}[gx > 0]
	}
	switch {
	case v != "" && h != "":
		return h + " " + v
	case v != "":
		return v
	}
	return h
}

// median of n values got by index.
func median(get func(int) float64, n int) float64 {
	if n == 0 {
		return 0
	}
	v := make([]float64, n)
	for i := range v {
		v[i] = get(i)
	}
	sort.Float64s(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
