// asiair-sorter lays out astro frames for processing in Siril: it reads the
// FITS headers of everything under the input folder (the ASIAIR's Plan and
// Autorun folders, a copy of them made by asiair-receiver, …) and puts
//
//   - the lights into Objects/<object>/<night>_<filter>_<exposure>_<gain>/lights,
//     next to empty darks/flats/biases/process folders, as Siril's scripts expect;
//   - darks, dark flats and biases into Library/… by camera, gain, exposure
//     and temperature (their date does not matter);
//   - flats into Library/Flats/<night>_<filter>_<gain>_<exposure>.
//
// Every session gets a note of the library folders that fit it, and can have
// them put in right away. The window shows a preview and the header of the
// selected frame.
package main

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The .syso resource files next to this file carry the application icon and
// the manifest the window needs (common controls v6, DPI awareness).
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ../../internal/gui/icon.png --manifest gui --product-name "ASIAIR Sorter" --file-description "ASIAIR Sorter"

const title = "ASIAIR Sorter — раскладка кадров для Siril"

type settings struct {
	In              string `json:"in"`
	Out             string `json:"out"`
	Mode            int    `json:"mode"`
	FillCalibration bool   `json:"fill_calibration"`
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "asiair-sorter", "settings.json")
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (s settings) save() {
	p := settingsPath()
	if p == "" {
		return
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		os.WriteFile(p, b, 0o644)
	}
}

type window struct {
	mw                         *walk.MainWindow
	inEdit, outEdit            *walk.LineEdit
	modeBox, kindBox           *walk.ComboBox
	fillBox                    *walk.CheckBox
	btnScan, btnApply, btnStop *walk.PushButton
	btnOpen                    *walk.PushButton
	progress                   *walk.ProgressBar
	status                     *walk.Label
	table                      *walk.TableView
	summary, header            *walk.TextEdit
	preview                    *walk.ImageView
	previewInfo                *walk.Label
	model                      *frameModel
	layout                     *sorter.Layout
	scannedIn, scannedOut      string // the folders the layout was made for
	busy                       bool
	stop                       atomic.Bool
	previewGen                 atomic.Int64
	bitmap                     *walk.Bitmap
}

var kindFilters = []struct{ label, kind string }{
	{"Все кадры", ""}, {"Light", sorter.Light}, {"Flat", sorter.Flat}, {"Dark", sorter.Dark},
	{"Bias", sorter.Bias}, {"Dark Flat", sorter.DarkFlat}, {"Тип не распознан", "?"},
}

func main() {
	w := &window{model: &frameModel{}}
	s := loadSettings()
	var kinds []string
	for _, k := range kindFilters {
		kinds = append(kinds, k.label)
	}
	var cols []TableViewColumn
	for _, c := range columns {
		col := TableViewColumn{Title: c.title, Width: c.width}
		if c.right {
			col.Alignment = AlignFar
		}
		cols = append(cols, col)
	}
	mono := Font{Family: "Consolas", PointSize: 9}

	err := MainWindow{
		AssignTo: &w.mw,
		Title:    title,
		Size:     Size{Width: 1280, Height: 820},
		MinSize:  Size{Width: 800, Height: 500},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 3, MarginsZero: true},
				Children: []Widget{
					Label{Text: "Входной каталог:"},
					LineEdit{AssignTo: &w.inEdit, Text: s.In, ToolTipText: "Где искать кадры: папка ASIAIR (Plan, Autorun), флешка или копия, сделанная приёмником. Обходятся все вложенные папки.",
						OnTextChanged: w.foldersChanged},
					PushButton{Text: "Обзор…", OnClicked: func() { w.browse(w.inEdit, "Входной каталог: где искать кадры FITS") }},
					Label{Text: "Выходной каталог:"},
					LineEdit{AssignTo: &w.outEdit, Text: s.Out, ToolTipText: "Куда разложить: Objects\\… (сессии для Siril) и Library\\… (дарки, флеты, биасы). Библиотека, собранная здесь раньше, тоже используется для подбора калибровки.",
						OnTextChanged: w.foldersChanged},
					PushButton{Text: "Обзор…", OnClicked: func() {
						w.browse(w.outEdit, "Выходной каталог: куда разложить кадры для Siril")
					}},
					Label{Text: "Как класть файлы:"},
					Composite{
						Layout:     HBox{MarginsZero: true},
						ColumnSpan: 2,
						Children: []Widget{
							ComboBox{AssignTo: &w.modeBox, Model: sorter.ModeNames, CurrentIndex: s.Mode, MinSize: Size{Width: 300}},
							CheckBox{AssignTo: &w.fillBox, Checked: s.FillCalibration,
								Text:        "Сразу положить подходящие дарки, флеты и биасы в папки объектов",
								ToolTipText: "Без этой галочки в каждой сессии появляется файл «калибровка.txt» со списком подходящих папок библиотеки, а darks/flats/biases остаются пустыми.\nС галочкой они заполняются копиями (в режиме жёстких ссылок — ссылками, без лишнего места)."},
							HSpacer{},
						},
					},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{AssignTo: &w.btnScan, Text: "1. Просмотреть кадры", OnClicked: w.scan,
						ToolTipText: "Прочитать заголовки FITS и показать, что куда попадёт. Файлы не меняются."},
					PushButton{AssignTo: &w.btnApply, Text: "2. Разложить", Enabled: false, OnClicked: w.apply},
					PushButton{AssignTo: &w.btnStop, Text: "Стоп", Enabled: false, OnClicked: func() { w.stop.Store(true) }},
					PushButton{AssignTo: &w.btnOpen, Text: "Открыть выходной каталог", OnClicked: w.openOut},
					HSpacer{},
					Label{Text: "Показать:"},
					ComboBox{AssignTo: &w.kindBox, Model: kinds, CurrentIndex: 0, OnCurrentIndexChanged: func() {
						w.model.kind = kindFilters[w.kindBox.CurrentIndex()].kind
						w.model.filter()
					}},
				},
			},
			ProgressBar{AssignTo: &w.progress, MaxValue: 100},
			Label{AssignTo: &w.status, Text: "Укажите каталоги и нажмите «Просмотреть кадры»."},
			HSplitter{
				StretchFactor: 1,
				Children: []Widget{
					TabWidget{
						StretchFactor: 3,
						Pages: []TabPage{
							{Title: "Кадры", Layout: VBox{}, Children: []Widget{
								TableView{AssignTo: &w.table, Model: w.model, Columns: cols, AlternatingRowBG: true,
									OnCurrentIndexChanged: w.showPreview},
							}},
							{Title: "Структура и калибровка", Layout: VBox{}, Children: []Widget{
								TextEdit{AssignTo: &w.summary, ReadOnly: true, VScroll: true, HScroll: true, Font: mono},
							}},
						},
					},
					Composite{
						StretchFactor: 2,
						Layout:        VBox{MarginsZero: true},
						Children: []Widget{
							ImageView{AssignTo: &w.preview, Mode: ImageViewModeZoom, StretchFactor: 3,
								Background: SolidColorBrush{Color: walk.RGB(16, 16, 16)}, MinSize: Size{Width: 200, Height: 150}},
							Label{AssignTo: &w.previewInfo, Text: "Выберите кадр — здесь будет предпросмотр (автоматическое растяжение) и заголовок FITS."},
							TextEdit{AssignTo: &w.header, ReadOnly: true, VScroll: true, Font: mono, StretchFactor: 2},
						},
					},
				},
			},
		},
	}.Create()
	if err != nil {
		walk.MsgBox(nil, "ASIAIR Sorter", err.Error(), walk.MsgBoxIconError)
		return
	}
	if ic, err := walk.NewIconFromResource("APP"); err == nil {
		w.mw.SetIcon(ic)
	}
	w.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		w.stop.Store(true)
		w.settings().save()
	})
	w.mw.Run()
}

func (w *window) settings() settings {
	return settings{In: strings.TrimSpace(w.inEdit.Text()), Out: strings.TrimSpace(w.outEdit.Text()),
		Mode: w.modeBox.CurrentIndex(), FillCalibration: w.fillBox.Checked()}
}

func (w *window) browse(e *walk.LineEdit, title string) {
	dlg := walk.FileDialog{Title: title, InitialDirPath: e.Text()}
	if ok, err := dlg.ShowBrowseFolder(w.mw); err == nil && ok {
		e.SetText(dlg.FilePath)
	}
}

// foldersChanged: a plan made for other folders must be made again.
func (w *window) foldersChanged() {
	if w.btnApply == nil || w.busy {
		return
	}
	s := w.settings()
	w.btnApply.SetEnabled(w.layout != nil && s.In == w.scannedIn && s.Out == w.scannedOut)
}

func (w *window) setBusy(b bool) {
	w.busy = b
	w.btnScan.SetEnabled(!b)
	w.btnStop.SetEnabled(b)
	w.inEdit.SetReadOnly(b)
	w.outEdit.SetReadOnly(b)
	w.modeBox.SetEnabled(!b)
	w.fillBox.SetEnabled(!b)
	if b {
		w.btnApply.SetEnabled(false)
	} else {
		w.foldersChanged()
	}
}

func (w *window) scan() {
	s := w.settings()
	if err := sorter.CheckDirs(s.In, s.Out); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Sorter", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	if st, err := os.Stat(s.In); err != nil || !st.IsDir() {
		walk.MsgBox(w.mw, "ASIAIR Sorter", "Нет такого каталога: "+s.In, walk.MsgBoxIconWarning)
		return
	}
	s.save()
	w.stop.Store(false)
	w.setBusy(true)
	w.progress.SetMarqueeMode(true)
	w.status.SetText("Читаю заголовки FITS…")
	go func() {
		frames, errs := sorter.ScanFor(s.In, s.Out, func(n int) {
			w.mw.Synchronize(func() { w.status.SetText(fmt.Sprintf("Читаю заголовки FITS… %d", n)) })
		})
		l := sorter.Plan(frames)
		w.mw.Synchronize(func() {
			w.progress.SetMarqueeMode(false)
			w.progress.SetValue(0)
			w.layout, w.scannedIn, w.scannedOut = l, s.In, s.Out
			w.model.set(l)
			w.summary.SetText(l.Summary())
			msg := fmt.Sprintf("Кадров: %d, сессий: %d, объём %s.", len(frames), len(l.Sessions), sorter.FormatBytes(l.TotalBytes))
			if len(errs) > 0 {
				msg += fmt.Sprintf(" Не прочитано файлов: %d (первый: %v).", len(errs), errs[0])
			}
			if len(frames) > 0 {
				msg += " Проверьте список и вкладку «Структура», затем «Разложить»."
			} else {
				msg = "Кадров FITS не найдено."
			}
			w.status.SetText(msg)
			w.setBusy(false)
		})
	}()
}

func (w *window) apply() {
	s := w.settings()
	l := w.layout
	if l == nil || s.In != w.scannedIn || s.Out != w.scannedOut {
		return
	}
	opt := sorter.Options{Mode: sorter.Mode(s.Mode), FillCalibration: s.FillCalibration}
	verb := map[sorter.Mode]string{sorter.Copy: "скопированы", sorter.Move: "ПЕРЕМЕЩЕНЫ", sorter.Link: "связаны жёсткими ссылками"}[opt.Mode]
	q := fmt.Sprintf("Кадров: %d (%s), они будут %s в\n%s\n\nПродолжить?", len(l.Items), sorter.FormatBytes(l.TotalBytes), verb, s.Out)
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	s.save()
	w.stop.Store(false)
	w.setBusy(true)
	go func() {
		r := l.Apply(s.Out, opt, func(done, total int, name string) {
			if done%5 != 0 && done != total {
				return
			}
			w.mw.Synchronize(func() {
				w.progress.SetValue(done * 100 / max(total, 1))
				w.status.SetText(fmt.Sprintf("%d из %d: %s", done, total, name))
			})
		}, w.stop.Load)
		w.mw.Synchronize(func() {
			msg := fmt.Sprintf("Готово: разложено %d, уже были на месте %d", r.Placed, r.Skipped)
			if opt.FillCalibration {
				msg += fmt.Sprintf(", калибровочных в папках объектов %d", r.Calibration)
			}
			if w.stop.Load() {
				msg = "Остановлено. " + msg
			}
			icon := walk.MsgBoxIconInformation
			if len(r.Errors) > 0 {
				icon = walk.MsgBoxIconWarning
				msg += fmt.Sprintf(".\n\nОшибок: %d", len(r.Errors))
				for i, e := range r.Errors {
					if i == 10 {
						msg += "\n…"
						break
					}
					msg += "\n" + e.Error()
				}
				for _, e := range r.Errors {
					log.Print(e)
				}
			}
			w.status.SetText(strings.SplitN(msg, "\n", 2)[0])
			// files may have moved: the plan is to be made again
			w.layout = nil
			w.setBusy(false)
			walk.MsgBox(w.mw, "ASIAIR Sorter", msg, icon)
		})
	}()
}

func (w *window) openOut() {
	out := strings.TrimSpace(w.outEdit.Text())
	if out == "" {
		return
	}
	os.MkdirAll(out, 0o755)
	exec.Command("explorer.exe", out).Start()
}

// showPreview reads the selected frame in the background: a binned,
// auto-stretched picture and the header.
func (w *window) showPreview() {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.model.rows) {
		return
	}
	f := w.model.rows[i].f
	gen := w.previewGen.Add(1)
	w.previewInfo.SetText("Читаю " + filepath.Base(f.Path) + "…")
	go func() {
		var pic image.Image
		var info, head string
		file, err := os.Open(f.Path)
		if err == nil {
			var im *fits.Image
			im, err = fits.Preview(file, 1200)
			file.Close()
			if err == nil {
				pic = im.Stretched()
				w0, h0 := im.Width*im.Binning, im.Height*im.Binning
				info = fmt.Sprintf("%s — %d×%d", filepath.Base(f.Path), w0, h0)
				if im.Bayer != "" {
					info += ", " + im.Bayer
				}
				var b strings.Builder
				for _, k := range im.Header.Keys() {
					fmt.Fprintf(&b, "%-8s = %s\r\n", k, im.Header.Get(k))
				}
				head = b.String()
			}
		}
		if err != nil {
			info = fmt.Sprintf("%s: %v", filepath.Base(f.Path), err)
		}
		w.mw.Synchronize(func() {
			if w.previewGen.Load() != gen {
				return // another frame was selected meanwhile
			}
			var bmp *walk.Bitmap
			if pic != nil {
				bmp, _ = walk.NewBitmapFromImageForDPI(pic, 96)
			}
			if bmp != nil {
				w.preview.SetImage(bmp)
			} else {
				w.preview.SetImage(nil)
			}
			if w.bitmap != nil {
				w.bitmap.Dispose()
			}
			w.bitmap = bmp
			w.previewInfo.SetText(info)
			w.header.SetText(head)
		})
	}()
}
