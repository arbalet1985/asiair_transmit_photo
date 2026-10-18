package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// «Удалить везде» in the menu of the table «Кадры»: the frames are deleted
// on this computer (the file read and its copies in the output), on the
// receiver and on the ASIAIR (the receiver has the agent delete them there).

// menuRows: the rows the menu of the row i acts on: the rows selected when
// i is one of them, else i alone.
func (w *window) menuRows(i int) []row {
	if i < 0 || i >= len(w.model.rows) {
		return nil
	}
	sel := w.table.SelectedIndexes()
	if !slices.Contains(sel, i) {
		sel = []int{i}
	}
	slices.Sort(sel)
	var rows []row
	for _, j := range sel {
		if j >= 0 && j < len(w.model.rows) {
			rows = append(rows, w.model.rows[j])
		}
	}
	return rows
}

func (w *window) deleteLabel(i int) string {
	if n := len(w.menuRows(i)); n > 1 {
		return fmt.Sprintf("Удалить везде выделенные кадры (%d)…", n)
	}
	return "Удалить везде…"
}

// localFiles: the files of the frame on this computer: the file itself
// when it was read from disk, and its copies in the output.
func (w *window) localFiles(r row) []string {
	var paths []string
	if r.f.Src == nil {
		paths = append(paths, r.f.Path)
	}
	if r.it != nil && w.scannedOut != "" {
		for _, p := range []string{r.it.Existing, filepath.Join(w.scannedOut, filepath.FromSlash(r.it.Dest))} {
			// a copy: the same name and size, inside the output
			if p != "" && sorter.Inside(p, w.scannedOut) && onDisk([]string{p}, r.f.Size) != "" {
				paths = append(paths, p)
			}
		}
	}
	var out []string
	for _, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if st, err := os.Stat(a); err == nil && st.Mode().IsRegular() && !slices.ContainsFunc(out, func(q string) bool { return strings.EqualFold(q, a) }) {
			out = append(out, a)
		}
	}
	return out
}

// deleteEverywhere deletes the frames of the menu of the row i on this
// computer, on the receiver and on the ASIAIR, then reads the frames again.
func (w *window) deleteEverywhere(i int) { w.deleteRows(w.menuRows(i)) }

// deleteRows deletes the frames of the rows everywhere, once asked.
func (w *window) deleteRows(rows []row) {
	if w.busy || len(rows) == 0 {
		return
	}
	var src *sorter.Receiver
	var remote, local []string
	localOf := map[string][]string{} // the remote path → its files here
	var bytes int64
	for _, r := range rows {
		bytes += r.f.Size
		files := w.localFiles(r)
		if r.f.Src != nil {
			src = r.f.Src
			remote = append(remote, r.f.Path)
			localOf[r.f.Path] = files
		} else {
			local = append(local, files...)
		}
	}
	nLocal := len(local)
	for _, f := range localOf {
		nLocal += len(f)
	}
	if nLocal == 0 && len(remote) == 0 {
		w.warn("Файлов этих кадров на этом компьютере нет, а приёмник их не отдаёт: удалять нечего.")
		return
	}
	what := "кадр " + filepath.Base(filepath.FromSlash(rows[0].f.Path))
	if len(rows) > 1 {
		what = fmt.Sprintf("кадры: %d", len(rows))
	}
	q := fmt.Sprintf("Удалить везде %s (%s)?\n", what, sorter.FormatBytes(bytes))
	if nLocal > 0 {
		q += fmt.Sprintf("\n• на этом компьютере — файлов: %d (в Корзину, если она есть на этом диске)", nLocal)
	}
	if len(remote) > 0 {
		q += fmt.Sprintf("\n• на приёмнике и на ASIAIR — кадров: %d (на ASIAIR — когда агент выйдет на связь, если удаление там не на паузе)", len(remote))
		q += "\n\nВосстановить их с приёмника и ASIAIR будет нельзя."
	} else if sorter.IsURL(w.scannedIn) {
		q += "\n\nНа приёмнике и ASIAIR этих кадров нет или приёмник сейчас недоступен — удаляются только файлы на этом компьютере."
	}
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconWarning|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	w.setPlay(false)
	w.setBusy(true)
	w.progress.SetMarqueeMode(true)
	w.setStatus("Удаляю везде: " + what + "…")
	out, in := w.scannedOut, w.scannedIn
	go func() {
		var res sorter.DeleteResult
		var rerr error
		if len(remote) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			res, rerr = src.DeleteEverywhere(ctx, remote)
			cancel()
			if rerr == nil {
				for _, p := range remote {
					// the copies here only when the receiver will not send the frame again
					if res.Done(p) {
						local = append(local, localOf[p]...)
					}
				}
			}
		}
		err := recycle(local)
		var done int
		var outDirs, inDirs, left []string
		for _, p := range local {
			if _, e := os.Stat(p); errors.Is(e, os.ErrNotExist) {
				done++
				if out != "" && sorter.Inside(p, out) {
					outDirs = append(outDirs, filepath.Dir(p))
				} else if !sorter.IsURL(in) && in != "" && sorter.Inside(p, in) {
					inDirs = append(inDirs, filepath.Dir(p))
				}
			} else {
				left = append(left, p)
			}
		}
		if out != "" {
			sorter.RemoveEmptyDirs(outDirs, out)
		}
		if len(inDirs) > 0 {
			sorter.RemoveEmptyDirs(inDirs, in)
		}
		log.Printf("удалить везде: на этом компьютере %d из %d, на приёмнике: %+v, %v", done, len(local), res, rerr)
		w.mw.Synchronize(func() {
			w.progress.SetMarqueeMode(false)
			w.setBusy(false)
			msg := fmt.Sprintf("Удалено на этом компьютере файлов: %d из %d.", done, len(local))
			icon := walk.MsgBoxIconInformation
			if len(remote) > 0 {
				if rerr != nil {
					icon = walk.MsgBoxIconWarning
					msg += fmt.Sprintf("\nПриёмник недоступен (%v): на приёмнике и ASIAIR ничего не удалено, копии этих кадров на этом компьютере оставлены.", rerr)
				} else {
					if res.Queued > 0 {
						msg += fmt.Sprintf("\nНа ASIAIR и приёмнике поставлено в очередь на удаление: %d.", res.Queued)
					}
					if res.Deleted > 0 {
						msg += fmt.Sprintf("\nНа приёмнике удалено (на ASIAIR их уже нет): %d.", res.Deleted)
					}
					if res.Absent > 0 {
						msg += fmt.Sprintf("\nНа приёмнике и ASIAIR уже не было: %d.", res.Absent)
					}
					if len(res.Failed) > 0 {
						icon = walk.MsgBoxIconWarning
						msg += fmt.Sprintf("\nНа приёмнике не удалено: %d (их копии здесь оставлены)", len(res.Failed))
						n := 0
						for p, why := range res.Failed {
							if n == 5 {
								msg += "\n…"
								break
							}
							msg += "\n" + p + ": " + why
							n++
						}
					}
				}
			}
			if len(left) > 0 {
				icon = walk.MsgBoxIconWarning
				msg += fmt.Sprintf("\n\nНе удалось удалить: %d, например\n%s", len(left), left[0])
				if err != nil {
					msg += "\n" + err.Error()
				}
			}
			w.setStatus(strings.SplitN(msg, "\n", 2)[0])
			walk.MsgBox(w.mw, "ASIAIR Sorter", msg, icon)
			w.layout = nil // the files are gone: the plan is made again
			w.scan()
		})
	}()
}
