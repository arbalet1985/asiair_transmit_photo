package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// Only some folders of the input can be synchronised: the choice is kept
// for every input (folder or receiver) in the settings.

// chosenFolders is the choice of folders for the input in ("" = all).
func (w *window) chosenFolders(in string) sorter.Folders {
	w.mu.Lock()
	defer w.mu.Unlock()
	return sorter.Folders(w.saved.Folders[receiverKey(in)])
}

func (w *window) setChosenFolders(in string, dirs []string) {
	w.mu.Lock()
	if w.saved.Folders == nil {
		w.saved.Folders = map[string][]string{}
	}
	if len(dirs) == 0 {
		delete(w.saved.Folders, receiverKey(in))
	} else {
		w.saved.Folders[receiverKey(in)] = dirs
	}
	w.mu.Unlock()
	w.settings().save()
}

// showFolders writes the choice of folders of the input next to its button.
func (w *window) showFolders() {
	if w.foldersInfo == nil {
		return
	}
	in := strings.TrimSpace(w.inEdit.Text())
	only := w.chosenFolders(in)
	if len(only) == 0 {
		setLong(w.foldersInfo, "все")
		return
	}
	setLong(w.foldersInfo, fmt.Sprintf("только %d: %s", len(only), strings.Join(only, "; ")))
}

// chooseFolders lists the folders of the input with frames and lets the
// ones to synchronise be ticked.
func (w *window) chooseFolders() {
	s := w.settings()
	if s.In == "" {
		w.warn("Сначала укажите, откуда брать кадры.")
		return
	}
	remote := sorter.IsURL(s.In)
	if !remote {
		if st, err := os.Stat(s.In); err != nil || !st.IsDir() {
			w.warn("Нет такого каталога: " + s.In)
			return
		}
	}
	password, code := w.passEdit.Text(), w.codeEdit.Text()
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.setBusy(true)
	w.progress.SetMarqueeMode(true)
	w.setStatus("Составляю список папок с кадрами…")
	go func() {
		defer cancel()
		var dirs []sorter.Folder
		var err error
		if remote {
			dirs, err = w.receiverFolders(ctx, s.In, password, code)
		} else {
			dirs, err = sorter.LocalFolders(s.In, s.Out)
		}
		w.mw.Synchronize(func() {
			w.progress.SetMarqueeMode(false)
			w.setBusy(false)
			if err != nil {
				w.setStatus(err.Error())
				if errors.Is(err, sorter.ErrLogin) {
					w.passEdit.SetFocus()
				}
				w.warn(err.Error())
				return
			}
			w.loginWarned = false
			w.codeEdit.SetText("")
			w.passEdit.SetText("")
			w.setStatus("")
			w.folderDialog(s.In, dirs)
		})
	}()
}

// receiverFolders lists the folders of the receiver, logging in again when
// the login kept is over.
func (w *window) receiverFolders(ctx context.Context, addr, password, code string) ([]sorter.Folder, error) {
	r, err := w.connect(ctx, addr, password, code)
	if err != nil {
		return nil, err
	}
	dirs, err := sorter.ReceiverFolders(ctx, r)
	if errors.Is(err, sorter.ErrLogin) {
		w.setLogin(addr, func(l *receiverLogin) { l.Session = "" })
		if password == "" {
			return nil, err
		}
		if err := r.Login(ctx, password, code); err != nil {
			return nil, err
		}
		w.setLogin(addr, func(l *receiverLogin) { l.Session = r.Session() })
		dirs, err = sorter.ReceiverFolders(ctx, r)
	}
	return dirs, err
}

// folderRow is a line of the dialog.
type folderRow struct {
	sorter.Folder
	on      bool
	missing bool // chosen before, no frames there now
}

type folderModel struct {
	walk.TableModelBase
	rows []*folderRow
}

func (m *folderModel) RowCount() int { return len(m.rows) }

func (m *folderModel) Value(row, col int) interface{} {
	r := m.rows[row]
	switch col {
	case 0:
		return r.Dir
	case 1:
		if r.missing {
			return "сейчас нет"
		}
		return fmt.Sprint(r.Files)
	case 2:
		if r.missing {
			return ""
		}
		return sorter.FormatBytes(r.Bytes)
	}
	return ""
}

func (m *folderModel) Checked(row int) bool { return m.rows[row].on }

func (m *folderModel) SetChecked(row int, checked bool) error {
	m.rows[row].on = checked
	return nil
}

func (m *folderModel) setAll(on bool) {
	for _, r := range m.rows {
		r.on = on
	}
	m.PublishRowsReset()
}

// folderDialog shows the folders with a tick for the ones synchronised.
func (w *window) folderDialog(in string, dirs []sorter.Folder) {
	only := w.chosenFolders(in)
	m := &folderModel{}
	listed := map[string]bool{}
	for _, d := range dirs {
		listed[strings.ToLower(d.Dir)] = true
		m.rows = append(m.rows, &folderRow{Folder: d, on: len(only) == 0 || only.HasDir(d.Dir)})
	}
	// a folder chosen before that has no frames now stays chosen
	for _, d := range only {
		if !listed[strings.ToLower(d)] {
			m.rows = append(m.rows, &folderRow{Folder: sorter.Folder{Dir: d}, on: true, missing: true})
		}
	}
	sort.Slice(m.rows, func(i, j int) bool { return strings.ToLower(m.rows[i].Dir) < strings.ToLower(m.rows[j].Dir) })
	if len(m.rows) == 0 {
		w.warn("Папок с кадрами FITS не найдено.")
		return
	}

	var dlg *walk.Dialog
	var ok, cancel *walk.PushButton
	var tv *walk.TableView
	accept := func() {
		var on []string
		all := true
		for _, r := range m.rows {
			if r.on {
				on = append(on, r.Dir)
			} else if !r.missing {
				all = false
			}
		}
		if len(on) == 0 {
			w.warn("Отметьте хотя бы одну папку.")
			return
		}
		if all {
			on = nil // every folder: new ones are taken too
		}
		w.setChosenFolders(in, on)
		dlg.Accept()
	}
	err := Dialog{
		AssignTo:      &dlg,
		Title:         "Какие папки синхронизировать",
		DefaultButton: &ok,
		CancelButton:  &cancel,
		MinSize:       Size{Width: 620, Height: 460},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Кадры берутся только из отмеченных папок (и папок внутри них). Остальные не читаются и не скачиваются,\n" +
				"а их кадры, уже разложенные в выходном каталоге, не считаются удалёнными на приёмнике.\n" +
				"Если отмечены все папки, новые папки тоже будут браться; если только некоторые — новые придётся отметить."},
			TableView{AssignTo: &tv, Model: m, CheckBoxes: true, AlternatingRowBG: true, NotSortableByHeaderClick: true,
				Columns: []TableViewColumn{
					{Title: "Папка", Width: 400},
					{Title: "Кадров", Width: 70, Alignment: AlignFar},
					{Title: "Объём", Width: 90, Alignment: AlignFar},
				}},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Отметить все", OnClicked: func() { m.setAll(true) }},
					PushButton{Text: "Снять все", OnClicked: func() { m.setAll(false) }},
					HSpacer{},
					PushButton{AssignTo: &ok, Text: "OK", OnClicked: accept},
					PushButton{AssignTo: &cancel, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(w.mw)
	if err != nil {
		w.warn(err.Error())
		return
	}
	copyMenu(tv, nil)
	if dlg.Run() == walk.DlgCmdOK {
		w.showFolders()
		w.foldersChanged()
		w.setStatus("Выбор папок сохранён — нажмите «Просмотреть кадры».")
	}
}
