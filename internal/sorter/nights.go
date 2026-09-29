package sorter

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NightRow is one line of the summary by night: the frames of one kind
// (and object and filter) shot in one night.
type NightRow struct {
	Night, Kind, Object, Filter string
	Frames                      int
	Exposures                   []float64 // the distinct exposures, ascending
	Seconds                     float64   // the exposures summed
	Here                        int       // already in the output
	Items                       []*Item
}

// NightLabel: "26.09 – 27.09" for the night beginning on 2026-09-26.
func NightLabel(night string) string {
	t, err := time.Parse("2006-01-02", night)
	if err != nil {
		return night
	}
	return t.Format("02.01") + " – " + t.AddDate(0, 0, 1).Format("02.01")
}

var kindOrder = map[string]int{Light: 0, Flat: 1, DarkFlat: 2, Dark: 3, Bias: 4}

// Nights sums the frames up by night, kind, object and filter, newest
// night first. Here counts the frames already in place under out.
func (l *Layout) Nights(out string) []*NightRow {
	by := map[string]*NightRow{}
	var rows []*NightRow
	for _, it := range l.Items {
		if it.Dup {
			continue
		}
		f := it.Frame
		k := f.Night + "|" + f.Kind + "|" + f.Object + "|" + strings.ToLower(f.Filter)
		r := by[k]
		if r == nil {
			r = &NightRow{Night: f.Night, Kind: f.Kind, Object: f.Object, Filter: f.Filter}
			by[k] = r
			rows = append(rows, r)
		}
		r.Frames++
		r.Seconds += f.Exposure
		r.Items = append(r.Items, it)
		seen := false
		for _, e := range r.Exposures {
			if sameExposure(e, f.Exposure) {
				seen = true
			}
		}
		if !seen {
			r.Exposures = append(r.Exposures, f.Exposure)
		}
		if out != "" {
			if st, err := os.Stat(filepath.Join(out, filepath.FromSlash(it.Dest))); err == nil && st.Size() == f.Size {
				r.Here++
			}
		}
	}
	for _, r := range rows {
		sort.Float64s(r.Exposures)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Night != b.Night {
			return a.Night > b.Night
		}
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		return a.Filter < b.Filter
	})
	return rows
}

// ExposureText: "300s" or "120s, 300s".
func (r *NightRow) ExposureText() string {
	var s []string
	for _, e := range r.Exposures {
		s = append(s, FormatExposure(e))
	}
	return strings.Join(s, ", ")
}

// ObjectTotals: "M 31: 22 Light, 1 ч 50 мин за 2 ночи · NGC 7000: …",
// the objects with the most time first.
func ObjectTotals(rows []*NightRow) string {
	type total struct {
		n      int
		sec    float64
		nights map[string]bool
	}
	by := map[string]*total{}
	for _, r := range rows {
		if r.Kind != Light {
			continue
		}
		t := by[r.Object]
		if t == nil {
			t = &total{nights: map[string]bool{}}
			by[r.Object] = t
		}
		t.n += r.Frames
		t.sec += r.Seconds
		t.nights[r.Night] = true
	}
	names := make([]string, 0, len(by))
	for o := range by {
		names = append(names, o)
	}
	sort.Slice(names, func(i, j int) bool { return by[names[i]].sec > by[names[j]].sec })
	var parts []string
	for _, o := range names {
		t := by[o]
		parts = append(parts, fmt.Sprintf("%s: %d Light, %s за %d %s", orDash(o), t.n, FormatDuration(t.sec), len(t.nights), plural(len(t.nights), "ночь", "ночи", "ночей")))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, few, many string) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return few
	}
	return many
}
