package fits

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ChannelStats are the statistics of one channel of the whole frame (not of
// the preview), as PixInsight's Statistics shows them, in the units of the
// file (ADU for the integer data of cameras). Bayer frames get one channel
// per colour of the matrix.
type ChannelStats struct {
	Name   string  `json:"name"` // "K" for mono, "R", "G", "B"
	Count  int64   `json:"count"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	AvgDev float64 `json:"avgdev"` // mean absolute deviation from the median
	MAD    float64 `json:"mad"`    // median absolute deviation from the median
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	// NoMedian: floating point data; Median, AvgDev and MAD are not known.
	NoMedian bool `json:"no_median,omitempty"`
	// Saturated: samples at or above the level Saturation, counted as
	// saturated as in the measurement of an area (0: the level is unknown)
	Saturation float64 `json:"saturation,omitempty"`
	Saturated  int64   `json:"saturated,omitempty"`
}

func channelNames(channels int) []string {
	if channels == 3 {
		return []string{"R", "G", "B"}
	}
	return []string{"K"}
}

// histStats: the statistics from a histogram of 16-bit samples u, whose
// value is bzero + bscale*(u-32768); sat: the level counted as saturated
// (0: unknown).
func histStats(name string, hist []uint32, bscale, bzero, sat float64) ChannelStats {
	st := ChannelStats{Name: name, Saturation: sat}
	var n int64
	var sum float64
	lo, hi := -1, -1
	for u, k := range hist {
		if k == 0 {
			continue
		}
		if lo < 0 {
			lo = u
		}
		hi = u
		n += int64(k)
		sum += float64(k) * float64(u)
	}
	if n == 0 {
		return st
	}
	mean := sum / float64(n)
	var ss, ad float64
	med := histMedian(hist, n)
	dev := make([]uint32, len(hist))
	for u := lo; u <= hi; u++ {
		k := hist[u]
		if k == 0 {
			continue
		}
		d := float64(u) - mean
		ss += float64(k) * d * d
		m := u - med
		if m < 0 {
			m = -m
		}
		dev[m] += k
		ad += float64(k) * float64(m)
	}
	val := func(u float64) float64 { return bzero + bscale*(u-32768) }
	scale := math.Abs(bscale)
	st.Count = n
	st.Mean = val(mean)
	st.Median = val(float64(med))
	st.AvgDev = scale * ad / float64(n)
	st.MAD = scale * float64(histMedian(dev, n))
	if n > 1 {
		st.StdDev = scale * math.Sqrt(ss/float64(n-1))
	}
	st.Min, st.Max = val(float64(lo)), val(float64(hi))
	if sat > 0 && bscale > 0 {
		for u := hi; u >= lo && val(float64(u)) >= sat; u-- {
			st.Saturated += int64(hist[u])
		}
	}
	if bscale < 0 {
		st.Min, st.Max = st.Max, st.Min
	}
	return st
}

func histMedian(hist []uint32, n int64) int {
	var acc int64
	for u, k := range hist {
		acc += int64(k)
		if 2*acc >= n {
			return u
		}
	}
	return len(hist) - 1
}

// momentAcc: count, mean, deviation and range of other data (one channel),
// and the samples at or above sat (0: unknown).
type momentAcc struct {
	n        int64
	mean, m2 float64 // Welford
	min, max float64
	sat      float64
	nsat     int64
}

func (a *momentAcc) add(v float64) {
	if a.sat > 0 && v >= a.sat {
		a.nsat++
	}
	if a.n == 0 {
		a.min, a.max = v, v
	}
	a.n++
	d := v - a.mean
	a.mean += d / float64(a.n)
	a.m2 += d * (v - a.mean)
	a.min = math.Min(a.min, v)
	a.max = math.Max(a.max, v)
}

func (a *momentAcc) stats(name string) ChannelStats {
	st := ChannelStats{Name: name, Count: a.n, Mean: a.mean, Min: a.min, Max: a.max, NoMedian: true,
		Saturation: a.sat, Saturated: a.nsat}
	if a.n > 1 {
		st.StdDev = math.Sqrt(a.m2 / float64(a.n-1))
	}
	return st
}

// StatsTable is the statistics as a text table, one column per channel,
// named as in PixInsight's Statistics; for a monospaced font.
func StatsTable(st []ChannelStats) string {
	if len(st) == 0 {
		return ""
	}
	num := func(v float64) string {
		switch a := math.Abs(v); {
		case v == math.Trunc(v) && a < 1e9:
			return strconv.FormatFloat(v, 'f', 0, 64)
		case a >= 100:
			return strconv.FormatFloat(v, 'f', 1, 64)
		case a >= 1:
			return strconv.FormatFloat(v, 'f', 3, 64)
		}
		return strconv.FormatFloat(v, 'f', 6, 64)
	}
	rows := []struct {
		name string
		get  func(ChannelStats) string
	}{
		{"", func(c ChannelStats) string { return c.Name }},
		{"count", func(c ChannelStats) string { return strconv.FormatInt(c.Count, 10) }},
		{"mean", func(c ChannelStats) string { return num(c.Mean) }},
		{"median", func(c ChannelStats) string { return medianOr(c, c.Median, num) }},
		{"avgDev", func(c ChannelStats) string { return medianOr(c, c.AvgDev, num) }},
		{"MAD", func(c ChannelStats) string { return medianOr(c, c.MAD, num) }},
		{"stdDev", func(c ChannelStats) string { return num(c.StdDev) }},
		{"minimum", func(c ChannelStats) string { return num(c.Min) }},
		{"maximum", func(c ChannelStats) string { return num(c.Max) }},
	}
	if st[0].Saturation > 0 {
		rows = append(rows, struct {
			name string
			get  func(ChannelStats) string
		}{"saturated", func(c ChannelStats) string { return SaturatedText(c) }})
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-8s", r.name)
		for _, c := range st {
			fmt.Fprintf(&b, " %12s", r.get(c))
		}
		b.WriteString("\r\n")
	}
	if st[0].Saturation > 0 {
		fmt.Fprintf(&b, "saturated — пикселей не ниже уровня насыщения %s (доля от count)\r\n", num(st[0].Saturation))
	}
	return b.String()
}

// SaturatedText: the samples counted as saturated and their share.
func SaturatedText(c ChannelStats) string {
	if c.Saturated == 0 || c.Count == 0 {
		return "0"
	}
	p := 100 * float64(c.Saturated) / float64(c.Count)
	switch {
	case p >= 1:
		return fmt.Sprintf("%d (%.1f%%)", c.Saturated, p)
	case p >= 0.01:
		return fmt.Sprintf("%d (%.2f%%)", c.Saturated, p)
	}
	return fmt.Sprintf("%d (<0.01%%)", c.Saturated)
}

func medianOr(c ChannelStats, v float64, num func(float64) string) string {
	if c.NoMedian {
		return "—"
	}
	return num(v)
}
