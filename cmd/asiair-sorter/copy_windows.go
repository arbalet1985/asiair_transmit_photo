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
// A TableView of walk is made of list views walk does not know, and the
// WM_CONTEXTMENU they send names one of them, so walk never shows a menu of
// its own. The list views are subclassed here and their WM_CONTEXTMENU (the
// right button, the menu key, Shift+F10) shows the menu right away.

// copyItem is a line of the menu: its label and what it copies, or, with
// run, what it does; a line without a label is a separator.
type copyItem struct {
	label, text string
	run         func()
}

// The subclassed list views (touched by the UI thread only): the window
// procedure they had and what their WM_CONTEXTMENU does.
var (
	lvOrigProc = map[win.HWND]uintptr{}
	lvMenu     = map[win.HWND]func(hwnd win.HWND, x, y int32){}
	lvFound    []win.HWND
	lvProcPtr  = syscall.NewCallback(lvMenuProc)
	lvEnumPtr  = syscall.NewCallback(func(h win.HWND, _ uintptr) uintptr {
		buf := make([]uint16, 64)
		if n, _ := win.GetClassName(h, &buf[0], len(buf)); n > 0 && syscall.UTF16ToString(buf[:n]) == "SysListView32" {
			lvFound = append(lvFound, h)
		}
		return 1
	})
)

// rclick: a right click on a list view is going on (on) and its menu has
// been shown already (shown). Should a list view send no WM_CONTEXTMENU,
// the menu is shown once the button is up: shown while the button is down,
// its release would pick a line or close the menu at once.
var rclick struct{ on, shown bool }

func lvMenuProc(hwnd win.HWND, msg uint32, wp, lp uintptr) uintptr {
	orig := lvOrigProc[hwnd]
	show := lvMenu[hwnd]
	switch msg {
	case win.WM_CONTEXTMENU:
		if show == nil {
			break
		}
		if rclick.on {
			if rclick.shown {
				return 0
			}
			rclick.shown = true
		}
		show(hwnd, win.GET_X_LPARAM(lp), win.GET_Y_LPARAM(lp))
		return 0
	case win.WM_RBUTTONDOWN:
		if show == nil {
			break
		}
		rclick.on, rclick.shown = true, false
		r := win.CallWindowProc(orig, hwnd, msg, wp, lp)
		// the list view may wait for the release itself
		if rclick.on && win.GetKeyState(win.VK_RBUTTON) >= 0 {
			rclick.on = false
			if !rclick.shown {
				var pt win.POINT
				win.GetCursorPos(&pt)
				show(hwnd, pt.X, pt.Y)
			}
		}
		return r
	case win.WM_RBUTTONUP:
		if show == nil || !rclick.on {
			break
		}
		r := win.CallWindowProc(orig, hwnd, msg, wp, lp)
		if rclick.on {
			rclick.on = false
			if !rclick.shown {
				pt := win.POINT{X: win.GET_X_LPARAM(lp), Y: win.GET_Y_LPARAM(lp)}
				win.ClientToScreen(hwnd, &pt)
				show(hwnd, pt.X, pt.Y)
			}
		}
		return r
	case win.WM_NCDESTROY:
		win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, orig)
		delete(lvOrigProc, hwnd)
		delete(lvMenu, hwnd)
	}
	return win.CallWindowProc(orig, hwnd, msg, wp, lp)
}

// copyMenu gives the table the menu; extra adds the lines of a row (nil:
// none). Ctrl+C copies the rows selected.
func copyMenu(tv *walk.TableView, extra func(row int) []copyItem) {
	show := func(hwnd win.HWND, x, y int32) {
		m, ok := tv.Model().(walk.TableModel)
		if !ok {
			return
		}
		row, col := -1, -1
		if x == -1 && y == -1 { // from the keyboard: the current row
			row, col = tv.CurrentIndex(), firstColumn(tv)
			x, y = rowCorner(tv, hwnd, row)
		} else {
			row, col = cellAt(tv, hwnd, x, y)
		}
		if row < 0 || row >= m.RowCount() {
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
		popup(tv.Form().Handle(), x, y, items)
	}
	lvFound = nil
	win.EnumChildWindows(tv.Handle(), lvEnumPtr, 0)
	for _, h := range lvFound {
		if _, done := lvOrigProc[h]; done {
			continue
		}
		if orig := win.SetWindowLongPtr(h, win.GWLP_WNDPROC, lvProcPtr); orig != 0 {
			lvOrigProc[h] = orig
			lvMenu[h] = show
		}
	}
	lvFound = nil
	tv.KeyDown().Attach(func(k walk.Key) {
		if k == walk.KeyC && walk.ControlDown() {
			if m, ok := tv.Model().(walk.TableModel); ok {
				if t := selectedText(tv, m); t != "" {
					walk.Clipboard().SetText(t)
				}
			}
		}
	})
}

// rowCorner: the point of the screen under the left end of the row (for
// the menu from the keyboard), the corner of the table if it is not seen.
func rowCorner(tv *walk.TableView, hwnd win.HWND, row int) (int32, int32) {
	rc := win.RECT{Left: win.LVIR_BOUNDS}
	pt := win.POINT{}
	if row >= 0 && win.SendMessage(hwnd, win.LVM_GETITEMRECT, uintptr(row), uintptr(unsafe.Pointer(&rc))) != 0 {
		var cl win.RECT
		win.GetClientRect(hwnd, &cl)
		if rc.Bottom > cl.Top && rc.Bottom <= cl.Bottom {
			pt = win.POINT{X: max(rc.Left, cl.Left), Y: rc.Bottom}
		}
	}
	win.ClientToScreen(hwnd, &pt)
	return pt.X, pt.Y
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

// cellAt: the row and the column of the model at the point of the screen
// in the list view, -1 if none.
func cellAt(tv *walk.TableView, hwnd win.HWND, x, y int32) (row, col int) {
	pt := win.POINT{X: x, Y: y}
	win.ScreenToClient(hwnd, &pt)
	hti := win.LVHITTESTINFO{Pt: pt}
	if int32(win.SendMessage(hwnd, win.LVM_SUBITEMHITTEST, 0, uintptr(unsafe.Pointer(&hti)))) < 0 {
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
	if sats := w.satInfoItems(i); len(sats) > 0 {
		items = append(items, copyItem{})
		items = append(items, sats...)
	}
	if !w.busy {
		items = append(items, copyItem{}, copyItem{label: w.deleteLabel(i), run: func() { w.deleteEverywhere(i) }})
	}
	return items
}
