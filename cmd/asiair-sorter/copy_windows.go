package main

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// The right button on a cell of a table: a menu to copy the cell, the whole
// row and, for frames, the paths of the file and what can be done with it.
//
// The menu is shown by the table itself, not as the context menu of walk:
// a TableView of walk is made of list views walk does not know, and the
// WM_CONTEXTMENU they send names one of them, so walk never showed its menu.

// copyItem is a line of the menu: its label and what it copies, or, with
// run, what it does; a line without a label is a separator.
type copyItem struct {
	label, text string
	run         func()
}

// copyMenu gives the table the menu; extra adds the lines of a row (nil:
// none). Ctrl+C copies the rows selected.
func copyMenu(tv *walk.TableView, extra func(row int) []copyItem) {
	show := func(row, col int, at func() (int32, int32)) {
		m, ok := tv.Model().(walk.TableModel)
		if !ok || row < 0 || row >= m.RowCount() {
			return
		}
		var items []copyItem
		if t := cellText(m, row, col); t != "" {
			items = append(items, copyItem{label: "Копировать", text: t})
		}
		if r := rowText(tv, m, row); strings.Contains(r, "\t") {
			items = append(items, copyItem{label: "Копировать строку", text: r})
		}
		if t := selectedText(tv, m); strings.Contains(t, "\n") {
			items = append(items, copyItem{label: fmt.Sprintf("Копировать выделенные строки (%d)", strings.Count(t, "\n")+1), text: t})
		}
		if extra != nil {
			items = append(items, extra(row)...)
		}
		x, y := at()
		popup(tv.Form().Handle(), x, y, items)
	}
	cursor := func() (int32, int32) {
		var pt win.POINT
		win.GetCursorPos(&pt)
		return pt.X, pt.Y
	}
	// the menu is shown once the list view has selected the row clicked
	tv.MouseDown().Attach(func(x, y int, b walk.MouseButton) {
		if b == walk.RightButton {
			row, col := cellAt(tv)
			tv.Synchronize(func() { show(row, col, cursor) })
		}
	})
	tv.KeyDown().Attach(func(k walk.Key) {
		switch {
		case k == walk.KeyApps || k == walk.KeyF10 && walk.ShiftDown():
			row := tv.CurrentIndex()
			tv.Synchronize(func() {
				show(row, firstColumn(tv), func() (int32, int32) {
					p := tv.ContextMenuLocation()
					return int32(p.X), int32(p.Y)
				})
			})
		case k == walk.KeyC && walk.ControlDown():
			if m, ok := tv.Model().(walk.TableModel); ok {
				if t := selectedText(tv, m); t != "" {
					walk.Clipboard().SetText(t)
				}
			}
		}
	})
}

// popup shows the menu at the point of the screen and does what is chosen.
func popup(owner win.HWND, x, y int32, items []copyItem) {
	menu := win.CreatePopupMenu()
	if menu == 0 {
		return
	}
	defer win.DestroyMenu(menu)
	var actions []func()
	seen := map[string]bool{}
	sep := false // a separator is due before the next line
	for _, it := range items {
		if it.label == "" {
			sep = len(actions) > 0
			continue
		}
		label := menuText(it.label)
		run := it.run
		if run == nil {
			if it.text == "" || seen[it.text] {
				continue
			}
			seen[it.text] = true
			// the text after the tab is shown on the right, like a shortcut
			label += "\t" + menuText(shorten(it.text, 60))
			text := it.text
			run = func() { walk.Clipboard().SetText(text) }
		}
		if sep {
			mii := win.MENUITEMINFO{FMask: win.MIIM_FTYPE, FType: win.MFT_SEPARATOR}
			mii.CbSize = uint32(unsafe.Sizeof(mii))
			win.InsertMenuItem(menu, uint32(win.GetMenuItemCount(menu)), true, &mii)
			sep = false
		}
		actions = append(actions, run)
		u, err := syscall.UTF16PtrFromString(label)
		if err != nil {
			continue
		}
		mii := win.MENUITEMINFO{FMask: win.MIIM_FTYPE | win.MIIM_ID | win.MIIM_STRING, FType: win.MFT_STRING,
			WID: uint32(len(actions)), DwTypeData: u}
		mii.CbSize = uint32(unsafe.Sizeof(mii))
		win.InsertMenuItem(menu, uint32(win.GetMenuItemCount(menu)), true, &mii)
	}
	if len(actions) == 0 {
		return
	}
	cmd := int(win.TrackPopupMenuEx(menu, win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON|win.TPM_NONOTIFY, x, y, owner, nil))
	if cmd >= 1 && cmd <= len(actions) {
		actions[cmd-1]()
	}
}

// selectedText: the rows selected, one per line, the cells with tabs between.
func selectedText(tv *walk.TableView, m walk.TableModel) string {
	rows := tv.SelectedIndexes()
	if len(rows) == 0 && tv.CurrentIndex() >= 0 {
		rows = []int{tv.CurrentIndex()}
	}
	slices.Sort(rows)
	var lines []string
	for _, i := range rows {
		if i >= 0 && i < m.RowCount() {
			lines = append(lines, rowText(tv, m, i))
		}
	}
	return strings.Join(lines, "\n")
}

// cellAt: the row and the column of the model under the mouse, -1 if none.
func cellAt(tv *walk.TableView) (row, col int) {
	var pt win.POINT
	if !win.GetCursorPos(&pt) {
		return -1, -1
	}
	h := win.WindowFromPoint(pt)
	if h == 0 || !win.IsChild(tv.Handle(), h) {
		return -1, -1
	}
	win.ScreenToClient(h, &pt)
	hti := win.LVHITTESTINFO{Pt: pt}
	if int32(win.SendMessage(h, win.LVM_SUBITEMHITTEST, 0, uintptr(unsafe.Pointer(&hti)))) < 0 {
		return -1, -1
	}
	// the list view has the visible columns only, in the order of the model
	cols := tv.Columns()
	for i, n := 0, 0; i < cols.Len(); i++ {
		if !cols.At(i).Visible() {
			continue
		}
		if n == int(hti.ISubItem) {
			return int(hti.IItem), i
		}
		n++
	}
	return int(hti.IItem), -1
}

// firstColumn: the column shown first (for the menu from the keyboard).
func firstColumn(tv *walk.TableView) int {
	if vis := tv.VisibleColumnsInDisplayOrder(); len(vis) > 0 {
		return tv.Columns().Index(vis[0])
	}
	return -1
}

func cellText(m walk.TableModel, row, col int) string {
	if col < 0 {
		return ""
	}
	switch v := m.Value(row, col).(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// rowText: the cells of the row as the table shows them, with tabs between
// (pasted into Excel they fall into columns).
func rowText(tv *walk.TableView, m walk.TableModel, row int) string {
	var cells []string
	for _, c := range tv.VisibleColumnsInDisplayOrder() {
		cells = append(cells, cellText(m, row, tv.Columns().Index(c)))
	}
	return strings.TrimRight(strings.Join(cells, "\t"), "\t")
}

// menuText: a text as it is, not taken for a shortcut or a mnemonic.
func menuText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\t", " "), "&", "&&")
}

// shorten cuts the middle out of a long text: both ends of a path matter.
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	h := (max - 1) / 2
	return string(r[:h]) + "…" + string(r[len(r)-(max-1-h):])
}

// frameCopies: the paths of the frame of a row of the table «Кадры».
func (w *window) frameCopies(i int) []copyItem {
	if i < 0 || i >= len(w.model.rows) {
		return nil
	}
	r := w.model.rows[i]
	f := r.f
	var items []copyItem
	local := f.Path
	if f.Src != nil {
		local = onDisk(w.localPaths(r), f.Size) // downloaded already
	}
	if local != "" {
		items = append(items, copyItem{label: "Копировать путь к файлу", text: local},
			copyItem{label: "Копировать путь к папке", text: filepath.Dir(local)})
	}
	if f.Src != nil {
		items = append(items, copyItem{label: "Копировать путь на приёмнике", text: strings.ReplaceAll(f.Path, "/", `\`)})
	}
	items = append(items, copyItem{label: "Копировать имя файла", text: path.Base(f.Rel)})
	if r.it != nil && !r.it.Dup && w.scannedOut != "" {
		items = append(items, copyItem{label: "Копировать путь «Куда»", text: filepath.Join(w.scannedOut, filepath.FromSlash(r.it.Dest))})
	}
	if !w.busy {
		items = append(items, copyItem{}, copyItem{label: w.deleteLabel(i), run: func() { w.deleteEverywhere(i) }})
	}
	return items
}
