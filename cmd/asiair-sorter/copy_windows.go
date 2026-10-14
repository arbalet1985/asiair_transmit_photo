package main

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// The right button on a cell of a table: a menu to copy the cell, the whole
// row and, for frames, the paths of the file.

// copyItem is a line of the menu: its label and what it copies.
type copyItem struct{ label, text string }

// copyMenu gives the table the menu; extra adds the lines of a row (nil: none).
func copyMenu(tv *walk.TableView, extra func(row int) []copyItem) {
	menu, err := walk.NewMenu()
	if err != nil {
		return
	}
	tv.AddDisposable(menu)
	fill := func(row, col int) {
		menu.Actions().Clear()
		tv.SetContextMenu(nil)
		m, ok := tv.Model().(walk.TableModel)
		if !ok || row < 0 || row >= m.RowCount() {
			return
		}
		var items []copyItem
		if t := cellText(m, row, col); t != "" {
			items = append(items, copyItem{"Копировать", t})
		}
		if extra != nil {
			items = append(items, extra(row)...)
		}
		if r := rowText(tv, m, row); strings.Contains(r, "\t") {
			items = append(items, copyItem{"Копировать строку", r})
		}
		seen := map[string]bool{}
		for _, it := range items {
			if it.text == "" || seen[it.text] {
				continue
			}
			seen[it.text] = true
			a := walk.NewAction()
			// the text after the tab is shown on the right, like a shortcut
			a.SetText(menuText(it.label) + "\t" + menuText(shorten(it.text, 60)))
			text := it.text
			a.Triggered().Attach(func() { walk.Clipboard().SetText(text) })
			menu.Actions().Add(a)
		}
		if menu.Actions().Len() > 0 {
			tv.SetContextMenu(menu)
		}
	}
	// both come before the menu is shown
	tv.MouseDown().Attach(func(x, y int, b walk.MouseButton) {
		if b == walk.RightButton {
			fill(cellAt(tv))
		}
	})
	tv.KeyDown().Attach(func(k walk.Key) {
		if k == walk.KeyApps || k == walk.KeyF10 && walk.ShiftDown() {
			fill(tv.CurrentIndex(), firstColumn(tv))
		}
	})
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
		items = append(items, copyItem{"Копировать путь к файлу", local},
			copyItem{"Копировать путь к папке", filepath.Dir(local)})
	}
	if f.Src != nil {
		items = append(items, copyItem{"Копировать путь на приёмнике", strings.ReplaceAll(f.Path, "/", `\`)})
	}
	items = append(items, copyItem{"Копировать имя файла", path.Base(f.Rel)})
	if r.it != nil && !r.it.Dup && w.scannedOut != "" {
		items = append(items, copyItem{"Копировать путь «Куда»", filepath.Join(w.scannedOut, filepath.FromSlash(r.it.Dest))})
	}
	return items
}
