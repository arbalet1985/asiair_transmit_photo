package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// From the menu of a row: the file shown in Explorer, its folder in the
// output opened, its session opened in Siril. Folders and addresses are
// also dropped onto the window, and the last ones used are picked from a
// list.

// showInExplorer opens the folder of the file with the file selected.
func showInExplorer(file string) {
	exec.Command("explorer.exe", "/select,"+file).Start()
}

func openFolder(dir string) {
	exec.Command("explorer.exe", dir).Start()
}

// sirilExe: Siril where it is usually installed, "" when not found.
func sirilExe() string {
	if p, err := exec.LookPath("siril.exe"); err == nil {
		return p
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if d := os.Getenv(env); d != "" {
			if p := filepath.Join(d, "Siril", "bin", "siril.exe"); isFile(p) {
				return p
			}
		}
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		if p := filepath.Join(d, "Programs", "Siril", "bin", "siril.exe"); isFile(p) {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// openInSiril starts Siril with the session folder as its working folder.
func (w *window) openInSiril(dir string) {
	exe := sirilExe()
	if exe == "" {
		w.warn("Siril не найден (обычно он в C:\\Program Files\\Siril). Установите его с siril.org или откройте папку сессии вручную:\n" + dir)
		return
	}
	cmd := exec.Command(exe, "-d", dir)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		w.warn("Siril не запустился: " + err.Error())
	}
}

// sessionDir: the folder of the session of a light laid out (the one with
// lights, darks, flats), else the folder the frame goes to; "" when it is
// not in the output.
func (w *window) sessionDir(it *sorter.Item) string {
	if it == nil || it.Dup || w.scannedOut == "" {
		return ""
	}
	dir := filepath.Dir(filepath.Join(w.scannedOut, filepath.FromSlash(it.Dest)))
	if it.Frame.Kind == sorter.Light && strings.EqualFold(filepath.Base(dir), "lights") {
		dir = filepath.Dir(dir)
	}
	if !isDir(dir) {
		return ""
	}
	return dir
}

// openItems: the lines of the menu of a row that open things.
func (w *window) openItems(r row) []copyItem {
	var items []copyItem
	if files := w.localFiles(r); len(files) > 0 {
		f := files[0]
		items = append(items, copyItem{label: "Показать файл в Проводнике", run: func() { showInExplorer(f) }})
	}
	if dir := w.sessionDir(r.it); dir != "" {
		label := "Открыть папку «Куда»"
		if r.f.Kind == sorter.Light {
			label = "Открыть папку сессии"
		}
		items = append(items, copyItem{label: label, run: func() { openFolder(dir) }})
		if r.f.Kind == sorter.Light {
			items = append(items, copyItem{label: "Открыть сессию в Siril", run: func() { w.openInSiril(dir) }})
		}
	}
	return items
}

// nightOpens: the menu of a line of the summary by night: its session.
func (w *window) nightOpens(i int) []copyItem {
	if i < 0 || i >= len(w.nights.rows) {
		return nil
	}
	for _, it := range w.nights.rows[i].Items {
		if dir := w.sessionDir(it); dir != "" {
			items := []copyItem{{}, {label: "Открыть папку в выходном каталоге", run: func() { openFolder(dir) }}}
			if it.Frame.Kind == sorter.Light {
				items = append(items, copyItem{label: "Открыть сессию в Siril", run: func() { w.openInSiril(dir) }})
			}
			return items
		}
	}
	return nil
}

// recentMax: how many inputs and outputs are remembered.
const recentMax = 10

// remember puts v first in the list, once.
func remember(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	out := []string{v}
	for _, x := range list {
		if !strings.EqualFold(x, v) && len(out) < recentMax {
			out = append(out, x)
		}
	}
	return out
}

// recentMenu offers the inputs (or outputs) used before for the field.
func (w *window) recentMenu(e *walk.LineEdit, btn *walk.PushButton, out bool) {
	w.mu.Lock()
	list := w.saved.RecentIn
	if out {
		list = w.saved.RecentOut
	}
	list = append([]string(nil), list...)
	w.mu.Unlock()
	var items []copyItem
	for _, v := range list {
		items = append(items, copyItem{label: v, run: func() { e.SetText(v) }})
	}
	if len(items) == 0 {
		items = []copyItem{{label: "Пока пусто: здесь будут прежние места после «Просмотреть кадры»", run: func() {}}}
	}
	popupUnder(w, btn, items)
}

// dropped puts the first folder dropped onto the field into it (the folder
// of a file dropped).
func dropped(e *walk.LineEdit, files []string) {
	for _, f := range files {
		if isDir(f) {
			e.SetText(f)
			return
		}
		if isFile(f) {
			e.SetText(filepath.Dir(f))
			return
		}
	}
}
