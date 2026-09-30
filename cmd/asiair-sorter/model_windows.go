package main

import (
	"fmt"
	"math"
	"path"
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
	{"Файл", 320, false}, {"В папке", 110, true}, {"Куда", 420, false},
}

// folderTotal: the frames of a folder of the input and their exposure.
type folderTotal struct {
	frames  int
	seconds float64
}

func (t folderTotal) String() string {
	if t.seconds <= 0 {
		return fmt.Sprintf("%d кадр.", t.frames)
	}
	return fmt.Sprintf("%d · %s", t.frames, sorter.FormatDuration(t.seconds))
}

type frameModel struct {
	walk.TableModelBase
	walk.SorterBase
	all, rows []row
	kind      string                // shown kind, "" = all, "?" = unknown
	only      map[*sorter.Item]bool // the frames of a line of the summary, nil = all
	words     []string              // the filter: every word is somewhere in the row
	folders   map[string]folderTotal
	shown     folderTotal // of the rows shown
	// gone: the frames of the output the receiver of the input has no more
	// (nil when the input is a folder); mark highlights them
	gone map[*sorter.Item]bool
	mark bool
}

// goneText is the column «Куда» of a frame deleted on the receiver.
const goneText = "— удалён на приёмнике, остался только в выходном каталоге"

// marked: the row is highlighted as deleted on the receiver.
func (m *frameModel) marked(i int) bool {
	return m.mark && i >= 0 && i < len(m.rows) && m.gone[m.rows[i].it]
}

// folderOf: the folder of the frame in the input.
func folderOf(f *sorter.Frame) string { return path.Dir(f.Rel) }

func (m *frameModel) RowCount() int { return len(m.rows) }

func (m *frameModel) Value(i, col int) interface{} { return m.cell(m.rows[i], col) }

func (m *frameModel) cell(r row, col int) string {
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
		return m.folders[folderOf(f)].String()
	case 10:
		switch {
		case r.it == nil:
			return "— тип не распознан, останется на месте"
		case r.it.Dup:
			return "— такой же файл уже есть в списке"
		case m.mark && m.gone[r.it]:
			return goneText
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
	case 9:
		return m.folders[folderOf(r.f)].seconds, true
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

// set replaces the frames shown; remote: they were read from a receiver.
func (m *frameModel) set(l *sorter.Layout, remote bool) {
	m.all, m.only, m.gone, m.mark = nil, nil, nil, false
	m.folders = map[string]folderTotal{}
	if l != nil {
		for _, it := range l.Items {
			if !it.InPlace() {
				m.all = append(m.all, row{f: it.Frame, it: it})
			}
		}
		for _, f := range l.Unknown {
			m.all = append(m.all, row{f: f})
		}
		if remote {
			m.gone = map[*sorter.Item]bool{}
			for _, it := range l.Gone() {
				m.gone[it] = true
			}
		}
	}
	for _, r := range m.all {
		if r.it == nil || r.it.Dup {
			continue // a copy of a frame counted already, or not a frame of a known kind
		}
		t := m.folders[folderOf(r.f)]
		t.frames++
		t.seconds += r.f.Exposure
		m.folders[folderOf(r.f)] = t
	}
	m.filter()
}

// setFilter takes the text of the filter field: words separated by spaces.
func (m *frameModel) setFilter(text string) {
	m.words = strings.Fields(strings.ToLower(text))
	m.filter()
}

// matches: every word of the filter is in some column of the row.
func (m *frameModel) matches(r row) bool {
	var b strings.Builder
	for col := range columns {
		b.WriteString(strings.ToLower(m.cell(r, col)))
		b.WriteByte('\n')
	}
	text := b.String()
	for _, w := range m.words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

func (m *frameModel) filter() {
	m.rows = m.rows[:0]
	m.shown = folderTotal{}
	for _, r := range m.all {
		if m.only != nil && !m.only[r.it] {
			continue
		}
		if !(m.kind == "" || m.kind == "?" && r.it == nil || r.f.Kind == m.kind && r.it != nil) {
			continue
		}
		if len(m.words) > 0 && !m.matches(r) {
			continue
		}
		m.rows = append(m.rows, r)
		if r.it != nil && !r.it.Dup {
			m.shown.frames++
			m.shown.seconds += r.f.Exposure
		}
	}
	m.Sort(m.SortedColumn(), m.SortOrder())
}

// nightModel is the summary by night.
type nightModel struct {
	walk.TableModelBase
	rows []*sorter.NightRow
}

var nightColumns = []struct {
	title string
	width int
	right bool
}{
	{"Ночь", 95, false}, {"Объект", 120, false}, {"Тип", 70, false}, {"Фильтр", 55, false},
	{"Кадров", 55, true}, {"Выдержка", 90, true}, {"Накопление", 90, true}, {"В выходном", 80, true},
}

func (m *nightModel) RowCount() int { return len(m.rows) }

func (m *nightModel) Value(i, col int) interface{} {
	r := m.rows[i]
	switch col {
	case 0:
		if i > 0 && m.rows[i-1].Night == r.Night {
			return "" // the night is written once
		}
		return sorter.NightLabel(r.Night)
	case 1:
		return r.Object
	case 2:
		return sorter.KindLabel[r.Kind]
	case 3:
		return r.Filter
	case 4:
		return strconv.Itoa(r.Frames)
	case 5:
		return r.ExposureText()
	case 6:
		return sorter.FormatDuration(r.Seconds)
	case 7:
		if r.Here == r.Frames {
			return "все"
		}
		return fmt.Sprintf("%d из %d", r.Here, r.Frames)
	}
	return ""
}

func (m *nightModel) set(rows []*sorter.NightRow) {
	m.rows = rows
	m.PublishRowsReset()
}
