package main

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// row of the table: a frame and where it goes (nil for frames of unknown kind).
type row struct {
	f    *sorter.Frame
	it   *sorter.Item
	moon float64 // lit fraction of the Moon when the frame was taken, -1 = unknown
	// fresh: not in the output yet (not downloaded, not laid out)
	fresh bool
}

// the columns of the table
const (
	colKind = iota
	colObject
	colNight
	colFilter
	colExposure
	colGain
	colTemp
	colCamera
	colHFR
	colStars
	colRound
	colSats
	colMoon
	colFile
	colFolder
	colDest
)

var columns = []struct {
	title string
	width int
	right bool
}{
	{"Тип", 70, false}, {"Объект", 110, false}, {"Ночь", 80, false}, {"Фильтр", 55, false},
	{"Выдержка", 70, true}, {"Гейн", 45, true}, {"Темп.", 45, true}, {"Камера", 120, false},
	{"HFR", 45, true}, {"Звёзд", 50, true}, {"Округл.", 55, true}, {"Спутники", 110, false}, {"Луна", 45, true},
	{"Файл", 280, false}, {"В папке", 110, true}, {"Куда", 360, false},
}

// moonOf: the lit fraction of the Moon at the moment of the frame.
func moonOf(f *sorter.Frame) float64 {
	if f.UTC.IsZero() {
		return -1
	}
	return moon.At(f.UTC, 0, 0, false).Illum
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
	tracks    string                // by the tracks (trackFilters), "" = all
	only      map[*sorter.Item]bool // the frames of a line of the summary, nil = all
	words     []string              // the filter: every word is somewhere in the row
	object    string                // the object chosen (normObject), "" = all
	wheel     string                // the filter of the filter wheel, "" = all, noFilter = frames without one
	folders   map[string]folderTotal
	shown     folderTotal // of the rows shown
	// gone: the frames of the output the receiver of the input has no more
	// (nil when the input is a folder); mark highlights them
	gone map[*sorter.Item]bool
	mark bool
	// onlyNew shows only the frames not in the output yet
	onlyNew bool
	fresh   int // how many of all are
}

// goneText is the column «Куда» of a frame deleted on the receiver.
const goneText = "— удалён или помечен на удаление на приёмнике, остался в выходном каталоге"

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
	case colKind:
		if f.Kind == "" {
			return "?"
		}
		if r.it != nil && r.it.Dup {
			return sorter.KindLabel[f.Kind] + " (повтор)"
		}
		return sorter.KindLabel[f.Kind]
	case colObject:
		return f.Object
	case colNight:
		return f.Night
	case colFilter:
		return f.Filter
	case colExposure:
		return sorter.FormatExposure(f.Exposure)
	case colGain:
		if f.Gain < 0 {
			return ""
		}
		return strconv.Itoa(f.Gain)
	case colTemp:
		return sorter.FormatTemp(f.Temp)
	case colCamera:
		return f.Camera
	case colHFR:
		if !f.Measured || f.Stars == 0 {
			return ""
		}
		return strings.Replace(strconv.FormatFloat(f.HFR, 'f', 2, 64), ".", ",", 1)
	case colStars:
		if !f.Measured {
			return ""
		}
		return strconv.Itoa(f.Stars)
	case colRound:
		if !f.Measured || f.Round <= 0 {
			return ""
		}
		return strings.Replace(strconv.FormatFloat(f.Round, 'f', 2, 64), ".", ",", 1)
	case colSats:
		return f.TracksText()
	case colMoon:
		if r.moon < 0 {
			return ""
		}
		return fmt.Sprintf("%.0f%%", r.moon*100)
	case colFile:
		return strings.ReplaceAll(f.Rel, "/", `\`)
	case colFolder:
		return m.folders[folderOf(f)].String()
	case colDest:
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
	case colExposure:
		return r.f.Exposure, true
	case colGain:
		return float64(r.f.Gain), true
	case colHFR:
		if !r.f.Measured || r.f.Stars == 0 {
			return 1e9, true // not measured: last
		}
		return r.f.HFR, true
	case colStars:
		if !r.f.Measured {
			return -1, true
		}
		return float64(r.f.Stars), true
	case colRound:
		if !r.f.Measured || r.f.Round <= 0 {
			return 2, true // not measured: last
		}
		return r.f.Round, true
	case colMoon:
		return r.moon, true
	case colTemp:
		if math.IsNaN(r.f.Temp) {
			return -1e9, true
		}
		return r.f.Temp, true
	case colFolder:
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

// notInOutput: a frame not laid out yet: neither at its place in the
// output nor in the output's library.
func notInOutput(it *sorter.Item, out string) bool {
	if it.Dup || it.Frame.InLibrary || out == "" {
		return false
	}
	if it.Existing != "" {
		if st, err := os.Stat(it.Existing); err == nil && st.Size() == it.Frame.Size {
			return false
		}
	}
	st, err := os.Stat(filepath.Join(out, filepath.FromSlash(it.Dest)))
	return err != nil || st.Size() != it.Frame.Size
}

// set replaces the frames shown; remote: they were read from a receiver;
// out is the output, to tell the frames not laid out yet.
func (m *frameModel) set(l *sorter.Layout, remote bool, out string) {
	m.all, m.only, m.gone, m.mark, m.fresh = nil, nil, nil, false, 0
	m.folders = map[string]folderTotal{}
	if l != nil {
		for _, it := range l.Items {
			if !it.InPlace() {
				r := row{f: it.Frame, it: it, moon: moonOf(it.Frame), fresh: notInOutput(it, out)}
				if r.fresh {
					m.fresh++
				}
				m.all = append(m.all, r)
			}
		}
		for _, f := range l.Unknown {
			m.all = append(m.all, row{f: f, moon: moonOf(f)})
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

// objects lists the objects of the frames, in natural order ("M 2" before
// "M 10"), each once however it is written.
func (m *frameModel) objects() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range m.all {
		o := strings.TrimSpace(r.f.Object)
		if o == "" || seen[sorter.NormObject(o)] {
			continue
		}
		seen[sorter.NormObject(o)] = true
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return sorter.NaturalLess(out[i], out[j]) })
	return out
}

// objectOfRow: the object a row belongs to: of the frame (lights), or of
// the object folder it goes to (calibration laid into the session folders).
func objectOfRow(r row) string {
	if r.f.Object != "" {
		return r.f.Object
	}
	if r.it != nil {
		if parts := strings.SplitN(r.it.Dest, "/", 3); len(parts) == 3 && parts[0] == sorter.ObjectsDir {
			return parts[1]
		}
	}
	return ""
}

// setObject takes the text of the object box.
func (m *frameModel) setObject(text string) {
	m.object = ""
	if t := strings.TrimSpace(text); t != "" && t != allObjects {
		m.object = t
	}
	m.filter()
}

// The lines of the filter box: all frames, the frames taken without a
// filter (the first line and the last one).
const (
	allFilters   = "все фильтры"
	noFilterText = "без фильтра"
	noFilter     = "\x00" // m.wheel for the frames without a filter
)

// filters lists the filters of the frames, each once whatever its case,
// and whether some frames have none.
func (m *frameModel) filters() (names []string, none bool) {
	seen := map[string]bool{}
	for _, r := range m.all {
		f := strings.TrimSpace(r.f.Filter)
		if f == "" {
			none = true
			continue
		}
		if !seen[strings.ToLower(f)] {
			seen[strings.ToLower(f)] = true
			names = append(names, f)
		}
	}
	sort.Slice(names, func(i, j int) bool { return sorter.NaturalLess(strings.ToLower(names[i]), strings.ToLower(names[j])) })
	return names, none
}

// wheelMatches: the frame was taken with the filter chosen.
func (m *frameModel) wheelMatches(f *sorter.Frame) bool {
	switch m.wheel {
	case "":
		return true
	case noFilter:
		return strings.TrimSpace(f.Filter) == ""
	}
	return strings.EqualFold(strings.TrimSpace(f.Filter), m.wheel)
}

// allObjects is the first line of the object box.
const allObjects = "все объекты"

// matches: every word of the filter is in some column of the row (an object
// written without spaces matches too: «m31» finds «M 31»).
func (m *frameModel) matches(r row) bool {
	var b strings.Builder
	for col := range columns {
		b.WriteString(strings.ToLower(m.cell(r, col)))
		b.WriteByte('\n')
	}
	b.WriteString(sorter.NormObject(r.f.Object))
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
	var objs map[string]bool
	if m.object != "" {
		objs = map[string]bool{}
		for _, o := range sorter.ObjectsMatching(m.objects(), m.object) {
			objs[sorter.NormObject(o)] = true
		}
	}
	for _, r := range m.all {
		if objs != nil && !objs[sorter.NormObject(objectOfRow(r))] {
			continue
		}
		if m.only != nil && !m.only[r.it] {
			continue
		}
		if !(m.kind == "" || m.kind == "?" && r.it == nil || r.f.Kind == m.kind && r.it != nil) {
			continue
		}
		if len(m.words) > 0 && !m.matches(r) {
			continue
		}
		if m.tracks != "" && !tracksMatch(r.f, m.tracks) {
			continue
		}
		if !m.wheelMatches(r.f) {
			continue
		}
		if m.onlyNew && !r.fresh {
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

// measured shows again the rows of the frames whose stars were measured.
func (m *frameModel) measured(frames map[*sorter.Frame]bool) {
	for i, r := range m.rows {
		if frames[r.f] {
			m.PublishRowChanged(i)
		}
	}
}

func (m *nightModel) set(rows []*sorter.NightRow) {
	m.rows = rows
	m.PublishRowsReset()
}
