package main

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// row of the table: a frame and where it goes (nil for frames of unknown kind).
type row struct {
	f  *sorter.Frame
	it *sorter.Item
}

var columns = []struct {
	title string
	width int
	right bool
}{
	{"Тип", 70, false}, {"Объект", 110, false}, {"Ночь", 80, false}, {"Фильтр", 55, false},
	{"Выдержка", 70, true}, {"Гейн", 45, true}, {"Темп.", 45, true}, {"Камера", 120, false},
	{"Файл", 320, false}, {"Куда", 420, false},
}

type frameModel struct {
	walk.TableModelBase
	walk.SorterBase
	all, rows []row
	kind      string // shown kind, "" = all, "?" = unknown
}

func (m *frameModel) RowCount() int { return len(m.rows) }

func (m *frameModel) Value(i, col int) interface{} {
	r := m.rows[i]
	f := r.f
	switch col {
	case 0:
		if f.Kind == "" {
			return "?"
		}
		if r.it != nil && r.it.Dup {
			return sorter.KindLabel[f.Kind] + " (повтор)"
		}
		return sorter.KindLabel[f.Kind]
	case 1:
		return f.Object
	case 2:
		return f.Night
	case 3:
		return f.Filter
	case 4:
		return sorter.FormatExposure(f.Exposure)
	case 5:
		if f.Gain < 0 {
			return ""
		}
		return strconv.Itoa(f.Gain)
	case 6:
		return sorter.FormatTemp(f.Temp)
	case 7:
		return f.Camera
	case 8:
		return strings.ReplaceAll(f.Rel, "/", `\`)
	case 9:
		switch {
		case r.it == nil:
			return "— тип не распознан, останется на месте"
		case r.it.Dup:
			return "— такой же файл уже есть в списке"
		}
		return strings.ReplaceAll(r.it.Dest, "/", `\`)
	}
	return ""
}

// num gives the numeric columns their order.
func (m *frameModel) num(r row, col int) (float64, bool) {
	switch col {
	case 4:
		return r.f.Exposure, true
	case 5:
		return float64(r.f.Gain), true
	case 6:
		if math.IsNaN(r.f.Temp) {
			return -1e9, true
		}
		return r.f.Temp, true
	}
	return 0, false
}

func (m *frameModel) Sort(col int, order walk.SortOrder) error {
	idx := make(map[*sorter.Frame]int, len(m.rows))
	for i, r := range m.rows {
		idx[r.f] = i
	}
	vals := make([]string, len(m.rows))
	for i := range m.rows {
		vals[i] = strings.ToLower(m.Value(i, col).(string))
	}
	sort.SliceStable(m.rows, func(i, j int) bool {
		a, b := m.rows[i], m.rows[j]
		var less, greater bool
		if x, ok := m.num(a, col); ok {
			y, _ := m.num(b, col)
			less, greater = x < y, x > y
		} else {
			va, vb := vals[idx[a.f]], vals[idx[b.f]]
			less, greater = va < vb, va > vb
		}
		if order == walk.SortDescending {
			return greater
		}
		return less
	})
	m.PublishRowsReset()
	return m.SorterBase.Sort(col, order)
}

// set replaces the frames shown.
func (m *frameModel) set(l *sorter.Layout) {
	m.all = nil
	if l != nil {
		for _, it := range l.Items {
			m.all = append(m.all, row{f: it.Frame, it: it})
		}
		for _, f := range l.Unknown {
			m.all = append(m.all, row{f: f})
		}
	}
	m.filter()
}

func (m *frameModel) filter() {
	m.rows = m.rows[:0]
	for _, r := range m.all {
		if m.kind == "" || m.kind == "?" && r.it == nil || r.f.Kind == m.kind && r.it != nil {
			m.rows = append(m.rows, r)
		}
	}
	m.Sort(m.SortedColumn(), m.SortOrder())
}
