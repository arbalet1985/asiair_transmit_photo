package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// Bad frames are marked while looking at them: Del or X in the table marks
// the frames selected (again: takes the mark off) and goes on to the next
// one. The button «Помеченные» then shows only them, puts them aside into
// Rejected in the output, deletes them everywhere or takes the marks off.

// rejectDir is the folder of the output the frames put aside go to.
const rejectDir = "Rejected"

// rejectKey: a frame by its path (on disk or on the receiver), so the marks
// stay when the frames are looked at again.
func rejectKey(f *sorter.Frame) string { return f.Path }

// rejected: the row is marked.
func (m *frameModel) rejected(i int) bool {
	return i >= 0 && i < len(m.rows) && m.rejects[rejectKey(m.rows[i].f)]
}

// selectedRows: the rows selected, else the current one.
func (w *window) selectedRows() []int {
	sel := w.table.SelectedIndexes()
	if len(sel) == 0 && w.table.CurrentIndex() >= 0 {
		sel = []int{w.table.CurrentIndex()}
	}
	return sel
}

// toggleReject marks the frames selected, or takes the mark off when all of
// them have it; with one frame it goes on to the next.
func (w *window) toggleReject() {
	sel := w.selectedRows()
	if len(sel) == 0 {
		return
	}
	all := true
	for _, i := range sel {
		all = all && w.model.rejected(i)
	}
	if w.model.rejects == nil {
		w.model.rejects = map[string]bool{}
	}
	for _, i := range sel {
		if i < 0 || i >= len(w.model.rows) {
			continue
		}
		k := rejectKey(w.model.rows[i].f)
		if all {
			delete(w.model.rejects, k)
		} else {
			w.model.rejects[k] = true
		}
		w.model.PublishRowChanged(i)
	}
	w.rejectsChanged()
	if len(sel) == 1 && !all && !w.model.onlyRejects {
		w.step(1)
	}
}

// rejectRows: the rows of the marked frames among those looked at.
func (w *window) rejectRows() []row {
	var rows []row
	for _, r := range w.model.all {
		if w.model.rejects[rejectKey(r.f)] {
			rows = append(rows, r)
		}
	}
	return rows
}

// rejectsChanged shows the button of the marked frames with their number.
func (w *window) rejectsChanged() {
	n := len(w.rejectRows())
	w.btnRejects.SetText(fmt.Sprintf("Помеченные: %d…", n))
	w.btnRejects.SetVisible(n > 0 || w.model.onlyRejects)
	if n == 0 && w.model.onlyRejects {
		w.model.onlyRejects = false
		w.filterChanged()
	}
}

// rejectsMenu: what to do with the marked frames.
func (w *window) rejectsMenu() {
	rows := w.rejectRows()
	show := "Показать только помеченные"
	if w.model.onlyRejects {
		show = "Показать все кадры"
	}
	items := []copyItem{
		{label: show, run: func() {
			w.model.onlyRejects = !w.model.onlyRejects
			w.filterChanged()
		}},
		{},
		{label: fmt.Sprintf("Отложить в папку %s…", rejectDir), run: func() { w.putAside(rows) }},
		{label: fmt.Sprintf("Удалить везде (%d)…", len(rows)), run: func() { w.deleteRows(rows) }},
		{},
		{label: "Снять все пометки", run: func() {
			w.model.rejects = nil
			w.model.onlyRejects = false
			w.filterChanged()
			w.rejectsChanged()
		}},
	}
	if w.busy {
		// shown greyed out while a job goes on
		for _, k := range []int{2, 3} {
			items[k].label, items[k].off = strings.TrimSuffix(items[k].label, "…")+" — после текущей работы", true
		}
	}
	popupUnder(w, w.btnRejects, items)
}

// popupUnder shows a menu under the button.
func popupUnder(w *window, btn *walk.PushButton, items []copyItem) {
	b := btn.BoundsPixels()
	pt := win.POINT{X: int32(b.X), Y: int32(b.Y + b.Height)}
	win.ClientToScreen(win.GetParent(btn.Handle()), &pt)
	popup(w.mw.Handle(), pt.X, pt.Y, items)
}

// putAside moves the marked frames read from disk into Rejected in the
// output, keeping their folders; their copies in the output go to the
// Recycle Bin. A frame of a receiver would come again with the next
// download: it is deleted everywhere instead.
func (w *window) putAside(rows []row) {
	out := w.scannedOut
	if w.busy || len(rows) == 0 || out == "" {
		return
	}
	type move struct {
		key, from, to string
		extra         []string // the copies of the frame in the output
	}
	var moves []move
	remote := 0
	for _, r := range rows {
		if r.f.Src != nil {
			remote++
			continue
		}
		var extra []string
		for _, p := range w.localFiles(r) {
			if !strings.EqualFold(filepath.Clean(p), filepath.Clean(r.f.Path)) {
				extra = append(extra, p)
			}
		}
		moves = append(moves, move{key: rejectKey(r.f), from: r.f.Path, to: filepath.Join(out, rejectDir, filepath.FromSlash(r.f.Rel)), extra: extra})
	}
	if len(moves) == 0 {
		w.warn("Помечены только кадры приёмника: отложенные, они скачались бы снова. Их можно удалить везде — на этом компьютере, на приёмнике и на ASIAIR.")
		return
	}
	q := fmt.Sprintf("Отложить помеченные кадры (%d) в папку\n%s?\n\nФайлы переносятся с сохранением папок; их копии в выходном каталоге уходят в Корзину. "+
		"Отложенные кадры больше не попадут в раскладку.", len(moves), filepath.Join(out, rejectDir))
	if remote > 0 {
		q += fmt.Sprintf("\n\nПомеченные кадры приёмника (%d) не трогаются: их можно удалить везде.", remote)
	}
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	w.setPlay(false)
	w.setBusy(true)
	w.progress.SetMarqueeMode(true)
	w.setStatus("Откладываю помеченные кадры…")
	go func() {
		done := 0
		var errs []string
		var extra []string
		for _, m := range moves {
			if err := moveFile(m.from, m.to); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", m.from, err))
				continue
			}
			done++
			extra = append(extra, m.extra...)
		}
		if err := recycle(extra); err != nil {
			errs = append(errs, err.Error())
		}
		log.Printf("отложено в %s: %d из %d; %v", rejectDir, done, len(moves), errs)
		w.mw.Synchronize(func() {
			w.progress.SetMarqueeMode(false)
			w.setBusy(false)
			msg := fmt.Sprintf("Отложено в %s: %d из %d.", filepath.Join(out, rejectDir), done, len(moves))
			icon := walk.MsgBoxIconInformation
			if len(errs) > 0 {
				icon = walk.MsgBoxIconWarning
				msg += fmt.Sprintf("\n\nОшибок: %d\n%s", len(errs), strings.Join(errs[:min(len(errs), 5)], "\n"))
			}
			for _, m := range moves {
				if _, err := os.Stat(m.from); err != nil {
					delete(w.model.rejects, m.key)
				}
			}
			w.setStatus(strings.SplitN(msg, "\n", 2)[0])
			walk.MsgBox(w.mw, "ASIAIR Sorter", msg, icon)
			w.layout = nil
			w.scan()
		})
	}()
}

// moveFile moves a file, copying it when it is on another disk.
func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("в %s уже есть такой файл", to)
	}
	if os.Rename(from, to) == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	o, err := os.Create(to + ".part")
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(o, in)
	in.Close()
	if cerr := o.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(to+".part", to)
	}
	if err != nil {
		os.Remove(to + ".part")
		return err
	}
	return os.Remove(from)
}
