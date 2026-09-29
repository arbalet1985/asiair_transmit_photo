// asiair-sorter lays out astro frames for processing in Siril: it reads the
// FITS headers of everything under the input — a folder (the ASIAIR's Plan
// and Autorun folders, a copy of them) or an asiair-receiver over the
// network — and puts
//
//   - the lights into Objects/<object>/<night>_<filter>_<exposure>_<gain>/lights,
//     next to empty darks/flats/biases/process folders, as Siril's scripts expect;
//   - darks, dark flats and biases into Library/… by camera, gain, exposure
//     and temperature (their date does not matter);
//   - flats into Library/Flats/<night>_<filter>_<gain>_<exposure>.
//
// From a receiver only the frames missing in the output are downloaded, and
// a download that stopped continues. Every session gets a note of the
// library folders that fit it, and can have them put in right away. The
// window shows a preview and the header of the selected frame.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The .syso resource files next to this file carry the application icon and
// the manifest the window needs (common controls v6, DPI awareness).
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ../../internal/gui/icon.png --manifest gui --product-name "ASIAIR Sorter" --file-description "ASIAIR Sorter"

const title = "ASIAIR Sorter — раскладка кадров для Siril"

// receiverLogin is what is kept of a receiver: its certificate and the
// login (the password itself is not kept).
type receiverLogin struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Session     string `json:"session,omitempty"`
}

type settings struct {
	In              string                   `json:"in"`
	Out             string                   `json:"out"`
	Mode            int                      `json:"mode"`
	FillCalibration bool                     `json:"fill_calibration"`
	Receivers       map[string]receiverLogin `json:"receivers,omitempty"`
}

func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "asiair-sorter")
}

func settingsPath() string {
	if d := configDir(); d != "" {
		return filepath.Join(d, "settings.json")
	}
	return ""
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.Receivers == nil {
		s.Receivers = map[string]receiverLogin{}
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
		os.WriteFile(p, b, 0o600) // holds the login to the receiver
	}
}

// receiverKey names a receiver in the settings: the address without the
// trailing slash, in lower case.
func receiverKey(addr string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(addr), "/"))
}

type window struct {
	mw                         *walk.MainWindow
	inEdit, outEdit            *walk.LineEdit
	passEdit, codeEdit         *walk.LineEdit
	btnForget, btnBrowseIn     *walk.PushButton
	loginRow                   *walk.Composite
	modeBox, kindBox           *walk.ComboBox
	fillBox                    *walk.CheckBox
	btnScan, btnApply, btnStop *walk.PushButton
	btnOpen                    *walk.PushButton
	progress                   *walk.ProgressBar
	status                     *walk.Label
	table                      *walk.TableView
	summary, header            *walk.TextEdit
	tabs                       *walk.TabWidget
	nightTable                 *walk.TableView
	nights                     *nightModel
	nightTotals, onlyInfo      *walk.Label
	btnAll                     *walk.PushButton
	preview                    *walk.ImageView
	previewInfo                *walk.Label
	model                      *frameModel
	layout                     *sorter.Layout
	scannedIn, scannedOut      string // the input and output the layout was made for
	busy                       bool
	cancel                     context.CancelFunc
	previewGen                 atomic.Int64
	bitmap                     *walk.Bitmap

	mu    sync.Mutex // guards saved (changed from the network goroutines)
	saved settings
}

var kindFilters = []struct{ label, kind string }{
	{"Все кадры", ""}, {"Light", sorter.Light}, {"Flat", sorter.Flat}, {"Dark", sorter.Dark},
	{"Bias", sorter.Bias}, {"Dark Flat", sorter.DarkFlat}, {"Тип не распознан", "?"},
}

func main() {
	w := &window{model: &frameModel{}, nights: &nightModel{}}
	s := loadSettings()
	w.saved = s
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
	var ncols []TableViewColumn
	for _, c := range nightColumns {
		col := TableViewColumn{Title: c.title, Width: c.width}
		if c.right {
			col.Alignment = AlignFar
		}
		ncols = append(ncols, col)
	}

	err := MainWindow{
		AssignTo: &w.mw,
		Title:    title,
		Size:     Size{Width: 1280, Height: 840},
		MinSize:  Size{Width: 800, Height: 500},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 3, MarginsZero: true},
				Children: []Widget{
					Label{Text: "Откуда:"},
					LineEdit{AssignTo: &w.inEdit, Text: s.In, OnTextChanged: w.inputChanged,
						ToolTipText: "Папка с кадрами (папка ASIAIR, флешка, папка приёмника) — обходятся все вложенные папки.\n" +
							"Или адрес приёмника asiair-receiver, чтобы брать кадры по сети с другого компьютера:\n" +
							"https://<адрес>:8443/ или адрес Tailscale — с паролем «Доступ с телефона»;\n" +
							"http://127.0.0.1:8080/ — на компьютере с приёмником."},
					PushButton{AssignTo: &w.btnBrowseIn, Text: "Обзор…", OnClicked: func() { w.browse(w.inEdit, "Входной каталог: где искать кадры FITS") }},
					Label{Text: ""},
					Composite{
						AssignTo:   &w.loginRow,
						Layout:     HBox{MarginsZero: true},
						ColumnSpan: 2,
						Children: []Widget{
							Label{Text: "Пароль приёмника:"},
							LineEdit{AssignTo: &w.passEdit, PasswordMode: true, MinSize: Size{Width: 160}, MaxSize: Size{Width: 220},
								ToolTipText: "Пароль блока «Доступ с телефона» на странице приёмника. Нужен только при первом входе: вход запоминается на 90 дней, сам пароль не сохраняется."},
							Label{Text: "Код:"},
							LineEdit{AssignTo: &w.codeEdit, MaxLength: 8, MinSize: Size{Width: 70}, MaxSize: Size{Width: 80},
								ToolTipText: "Одноразовый код из приложения-аутентификатора, если он включён на приёмнике."},
							PushButton{AssignTo: &w.btnForget, Text: "Забыть вход и сертификат", OnClicked: w.forgetReceiver,
								ToolTipText: "Выйти и забыть сертификат приёмника (например, после переустановки приёмника). При следующем подключении нужен пароль."},
							HSpacer{},
						},
					},
					Label{Text: "Выходной каталог:"},
					LineEdit{AssignTo: &w.outEdit, Text: s.Out, OnTextChanged: w.foldersChanged,
						ToolTipText: "Куда разложить: Objects\\… (сессии для Siril) и Library\\… (дарки, флеты, биасы). Библиотека, собранная здесь раньше, тоже используется для подбора калибровки."},
					PushButton{Text: "Обзор…", OnClicked: func() {
						w.browse(w.outEdit, "Выходной каталог: куда разложить кадры для Siril")
					}},
					Label{Text: "Как класть файлы:"},
					Composite{
						Layout:     HBox{MarginsZero: true},
						ColumnSpan: 2,
						Children: []Widget{
							ComboBox{AssignTo: &w.modeBox, Model: sorter.ModeNames, CurrentIndex: s.Mode, MinSize: Size{Width: 300},
								ToolTipText: "Для папки: копировать, перемещать или делать жёсткие ссылки.\nС приёмника кадры всегда скачиваются (на приёмнике ничего не удаляется); режим жёстких ссылок тогда касается только калибровки в папках объектов."},
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
					PushButton{AssignTo: &w.btnStop, Text: "Стоп", Enabled: false, OnClicked: func() {
						if w.cancel != nil {
							w.cancel()
						}
					}},
					PushButton{AssignTo: &w.btnOpen, Text: "Открыть выходной каталог", OnClicked: w.openOut},
					HSpacer{},
					Label{AssignTo: &w.onlyInfo},
					PushButton{AssignTo: &w.btnAll, Text: "Показать все", Visible: false, OnClicked: w.showAll},
					Label{Text: "Показать:"},
					ComboBox{AssignTo: &w.kindBox, Model: kinds, CurrentIndex: 0, OnCurrentIndexChanged: func() {
						w.model.kind = kindFilters[w.kindBox.CurrentIndex()].kind
						w.model.filter()
					}},
				},
			},
			ProgressBar{AssignTo: &w.progress, MaxValue: 1000},
			Label{AssignTo: &w.status, Text: "Укажите, откуда брать кадры и куда их разложить, и нажмите «Просмотреть кадры»."},
			HSplitter{
				StretchFactor: 1,
				Children: []Widget{
					TabWidget{
						AssignTo:      &w.tabs,
						StretchFactor: 3,
						Pages: []TabPage{
							{Title: "Кадры", Layout: VBox{}, Children: []Widget{
								TableView{AssignTo: &w.table, Model: w.model, Columns: cols, AlternatingRowBG: true,
									OnCurrentIndexChanged: w.showPreview},
							}},
							{Title: "Сводка по ночам", Layout: VBox{}, Children: []Widget{
								TableView{AssignTo: &w.nightTable, Model: w.nights, Columns: ncols, AlternatingRowBG: true,
									NotSortableByHeaderClick: true, OnItemActivated: w.showNight},
								Label{AssignTo: &w.nightTotals, TextColor: walk.RGB(90, 90, 90)},
								Label{Text: "Ночь — с полудня до полудня: утренние флеты входят в ту же ночь. Двойной щелчок по строке показывает её кадры на вкладке «Кадры».",
									TextColor: walk.RGB(120, 120, 120)},
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
	w.inputChanged()
	w.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if w.cancel != nil {
			w.cancel()
		}
		w.settings().save()
	})
	w.mw.Run()
}

// settings are the saved ones with the fields of the window.
func (w *window) settings() settings {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.saved
	s.In, s.Out = strings.TrimSpace(w.inEdit.Text()), strings.TrimSpace(w.outEdit.Text())
	s.Mode, s.FillCalibration = w.modeBox.CurrentIndex(), w.fillBox.Checked()
	w.saved = s
	return s
}

// setLogin keeps what is known of a receiver (safe from any goroutine).
func (w *window) setLogin(addr string, change func(*receiverLogin)) {
	w.mu.Lock()
	k := receiverKey(addr)
	l := w.saved.Receivers[k]
	change(&l)
	if l == (receiverLogin{}) {
		delete(w.saved.Receivers, k)
	} else {
		w.saved.Receivers[k] = l
	}
	s := w.saved
	w.mu.Unlock()
	s.save()
}

func (w *window) login(addr string) receiverLogin {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.saved.Receivers[receiverKey(addr)]
}

func (w *window) browse(e *walk.LineEdit, title string) {
	dir := e.Text()
	if sorter.IsURL(dir) {
		dir = ""
	}
	dlg := walk.FileDialog{Title: title, InitialDirPath: dir}
	if ok, err := dlg.ShowBrowseFolder(w.mw); err == nil && ok {
		e.SetText(dlg.FilePath)
	}
}

// inputChanged shows the login fields for a receiver address.
func (w *window) inputChanged() {
	if w.loginRow == nil {
		return
	}
	w.loginRow.SetVisible(sorter.IsURL(w.inEdit.Text()))
	w.foldersChanged()
}

func (w *window) forgetReceiver() {
	addr := strings.TrimSpace(w.inEdit.Text())
	w.setLogin(addr, func(l *receiverLogin) { *l = receiverLogin{} })
	w.status.SetText("Вход и сертификат приёмника забыты: при следующем подключении введите пароль.")
}

// foldersChanged: a plan made for another input or output is made again.
func (w *window) foldersChanged() {
	if w.btnApply == nil || w.busy {
		return
	}
	in, out := strings.TrimSpace(w.inEdit.Text()), strings.TrimSpace(w.outEdit.Text())
	w.btnApply.SetEnabled(w.layout != nil && in == w.scannedIn && out == w.scannedOut)
}

func (w *window) setBusy(b bool) {
	w.busy = b
	w.btnScan.SetEnabled(!b)
	w.btnStop.SetEnabled(b)
	w.inEdit.SetReadOnly(b)
	w.outEdit.SetReadOnly(b)
	w.passEdit.SetReadOnly(b)
	w.codeEdit.SetReadOnly(b)
	w.btnForget.SetEnabled(!b)
	w.btnBrowseIn.SetEnabled(!b)
	w.modeBox.SetEnabled(!b)
	w.fillBox.SetEnabled(!b)
	if b {
		w.btnApply.SetEnabled(false)
	} else {
		w.foldersChanged()
	}
}

func (w *window) warn(msg string) {
	walk.MsgBox(w.mw, "ASIAIR Sorter", msg, walk.MsgBoxIconWarning)
}

// connect prepares the receiver of the input: with the certificate and the
// login remembered, logging in with the password when there is none yet.
func (w *window) connect(ctx context.Context, addr, password, code string) (*sorter.Receiver, error) {
	saved := w.login(addr)
	r, err := sorter.NewReceiver(addr, saved.Fingerprint, saved.Session)
	if err != nil {
		return nil, err
	}
	r.OnPin = func(fp string) {
		w.setLogin(addr, func(l *receiverLogin) { l.Fingerprint = fp })
		log.Printf("Сертификат приёмника %s: %s", addr, fp)
	}
	if saved.Session == "" && password != "" {
		if err := r.Login(ctx, password, code); err != nil {
			return nil, err
		}
		w.setLogin(addr, func(l *receiverLogin) { l.Session = r.Session() })
	}
	return r, nil
}

func (w *window) scan() {
	s := w.settings()
	remote := sorter.IsURL(s.In)
	if err := sorter.CheckDirs(s.In, s.Out); err != nil {
		w.warn(err.Error())
		return
	}
	if !remote {
		if st, err := os.Stat(s.In); err != nil || !st.IsDir() {
			w.warn("Нет такого каталога: " + s.In)
			return
		}
	}
	password, code := w.passEdit.Text(), w.codeEdit.Text()
	s.save()
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.setBusy(true)
	w.progress.SetMarqueeMode(true)
	w.status.SetText("Читаю заголовки FITS…")
	say := func(t string) { w.mw.Synchronize(func() { w.status.SetText(t) }) }
	go func() {
		defer cancel()
		var frames []sorter.Frame
		var errs []error
		var fatal error
		if remote {
			frames, errs, fatal = w.scanReceiver(ctx, s, password, code, say)
		} else {
			frames, errs = sorter.ScanFor(s.In, s.Out, func(n int) { say(fmt.Sprintf("Читаю заголовки FITS… %d", n)) })
		}
		l := sorter.Plan(frames)
		missing, missingBytes := l.Missing(s.Out)
		nights := l.Nights(s.Out)
		w.mw.Synchronize(func() {
			w.progress.SetMarqueeMode(false)
			w.progress.SetValue(0)
			w.setBusy(false)
			if fatal != nil {
				w.status.SetText(fatal.Error())
				if errors.Is(fatal, sorter.ErrLogin) {
					w.passEdit.SetFocus()
				}
				w.warn(fatal.Error())
				return
			}
			w.codeEdit.SetText("")
			w.passEdit.SetText("")
			w.layout, w.scannedIn, w.scannedOut = l, s.In, s.Out
			w.model.set(l)
			w.showAll()
			w.showNights(nights)
			w.summary.SetText(l.Summary())
			w.foldersChanged()
			msg := fmt.Sprintf("Кадров: %d, сессий: %d, объём %s.", l.Frames(), len(l.Sessions), sorter.FormatBytes(l.TotalBytes))
			if remote {
				msg += fmt.Sprintf(" Нет в выходном каталоге: %d (%s).", missing, sorter.FormatBytes(missingBytes))
			}
			if len(errs) > 0 {
				msg += fmt.Sprintf(" Не прочитано: %d (%v).", len(errs), errs[0])
			}
			if len(frames) == 0 {
				msg = "Кадров FITS не найдено."
				if remote {
					msg = "На приёмнике нет полученных кадров FITS."
				}
			} else {
				msg += " Проверьте список и вкладку «Структура», затем «Разложить»."
			}
			w.status.SetText(msg)
		})
	}()
}

// scanReceiver reads the frames of the receiver in the input, plus the
// library already in the output. fatal is set when nothing could be read.
func (w *window) scanReceiver(ctx context.Context, s settings, password, code string, say func(string)) (frames []sorter.Frame, errs []error, fatal error) {
	say("Подключаюсь к приёмнику…")
	r, err := w.connect(ctx, s.In, password, code)
	if err != nil {
		return nil, nil, err
	}
	cache := sorter.LoadHeaderCache(filepath.Join(configDir(), "headers.json"))
	progress := func(done, total int) {
		say(fmt.Sprintf("Читаю заголовки кадров на приёмнике… %d из %d", done, total))
	}
	frames, errs = sorter.ScanReceiver(ctx, r, cache, progress)
	if len(errs) == 1 && errors.Is(errs[0], sorter.ErrLogin) && len(frames) == 0 {
		// the login kept is over (or was never made)
		w.setLogin(s.In, func(l *receiverLogin) { l.Session = "" })
		if password == "" {
			return nil, nil, sorter.ErrLogin
		}
		if err := r.Login(ctx, password, code); err != nil {
			return nil, nil, err
		}
		w.setLogin(s.In, func(l *receiverLogin) { l.Session = r.Session() })
		frames, errs = sorter.ScanReceiver(ctx, r, cache, progress)
	}
	if len(frames) == 0 && len(errs) > 0 {
		return nil, nil, errs[0]
	}
	cache.Save(r.Base())
	say("Читаю библиотеку в выходном каталоге…")
	lib, e := sorter.ScanLibrary(s.Out, nil)
	return append(frames, lib...), append(errs, e...), nil
}

func (w *window) apply() {
	s := w.settings()
	l := w.layout
	if l == nil || s.In != w.scannedIn || s.Out != w.scannedOut {
		return
	}
	opt := sorter.Options{Mode: sorter.Mode(s.Mode), FillCalibration: s.FillCalibration}
	var q string
	if sorter.IsURL(s.In) {
		n, b := l.Missing(s.Out)
		if n == 0 && !opt.FillCalibration {
			q = "Все кадры приёмника уже есть в выходном каталоге. Обновить только файлы «калибровка.txt»?"
		} else {
			q = fmt.Sprintf("С приёмника будет скачано кадров: %d (%s), остальные уже на месте.\nКуда: %s\n\nПродолжить?", n, sorter.FormatBytes(b), s.Out)
		}
	} else {
		verb := map[sorter.Mode]string{sorter.Copy: "скопированы", sorter.Move: "ПЕРЕМЕЩЕНЫ", sorter.Link: "связаны жёсткими ссылками"}[opt.Mode]
		q = fmt.Sprintf("Кадров: %d (%s), они будут %s в\n%s\n\nПродолжить?", len(l.Items), sorter.FormatBytes(l.TotalBytes), verb, s.Out)
	}
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	s.save()
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.setBusy(true)
	go func() {
		defer cancel()
		start := time.Now()
		var lastShown time.Time
		r := l.Apply(ctx, s.Out, opt, func(p sorter.Progress) {
			if time.Since(lastShown) < 200*time.Millisecond && p.Done != p.Total {
				return
			}
			lastShown = time.Now()
			w.mw.Synchronize(func() { w.showProgress(p, start) })
		})
		stopped := ctx.Err() != nil
		nights := l.Nights(s.Out) // what is in the output now
		w.mw.Synchronize(func() {
			w.showNights(nights)
			msg := fmt.Sprintf("Готово: разложено %d, уже были на месте %d", r.Placed, r.Skipped)
			if r.Moved > 0 {
				msg += fmt.Sprintf(", перенесено в новые папки библиотеки %d", r.Moved)
			}
			if r.Downloaded > 0 {
				msg += fmt.Sprintf(", скачано %s", sorter.FormatBytes(r.Downloaded))
			}
			if opt.FillCalibration {
				msg += fmt.Sprintf(", калибровочных в папках объектов %d", r.Calibration)
			}
			if stopped {
				msg = "Остановлено. " + msg + ". Начатое скачивание продолжится со следующего запуска"
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

// showProgress: by bytes while downloading from a receiver, else by files.
func (w *window) showProgress(p sorter.Progress, start time.Time) {
	if p.TotalBytes > 0 {
		w.progress.SetValue(int(p.Bytes * 1000 / p.TotalBytes))
		t := fmt.Sprintf("Скачано %s из %s", sorter.FormatBytes(p.Bytes), sorter.FormatBytes(p.TotalBytes))
		if sec := time.Since(start).Seconds(); sec > 2 && p.Bytes > 0 {
			speed := float64(p.Bytes) / sec
			t += fmt.Sprintf(", %s/с, осталось %s", sorter.FormatBytes(int64(speed)), sorter.FormatDuration(float64(p.TotalBytes-p.Bytes)/speed))
		}
		w.status.SetText(fmt.Sprintf("%s — %d из %d: %s", t, p.Done, p.Total, p.Name))
		return
	}
	w.progress.SetValue(p.Done * 1000 / max(p.Total, 1))
	w.status.SetText(fmt.Sprintf("%d из %d: %s", p.Done, p.Total, p.Name))
}

// showNights fills the summary by night.
func (w *window) showNights(rows []*sorter.NightRow) {
	w.nights.set(rows)
	t := sorter.ObjectTotals(rows)
	if t != "" {
		t = "Всего по объектам — " + t
	}
	w.nightTotals.SetText(t)
}

// showNight shows the frames of the summary line double-clicked.
func (w *window) showNight() {
	i := w.nightTable.CurrentIndex()
	if i < 0 || i >= len(w.nights.rows) {
		return
	}
	r := w.nights.rows[i]
	w.model.only = map[*sorter.Item]bool{}
	for _, it := range r.Items {
		w.model.only[it] = true
	}
	w.kindBox.SetCurrentIndex(0) // the line is of one kind already
	w.model.kind = ""
	w.model.filter()
	what := sorter.KindLabel[r.Kind]
	for _, p := range []string{r.Object, r.Filter} {
		if p != "" {
			what += ", " + p
		}
	}
	w.onlyInfo.SetText(fmt.Sprintf("Ночь %s: %s", sorter.NightLabel(r.Night), what))
	w.btnAll.SetVisible(true)
	w.tabs.SetCurrentIndex(0)
}

// showAll ends showing one line of the summary.
func (w *window) showAll() {
	w.model.only = nil
	w.model.filter()
	w.onlyInfo.SetText("")
	w.btnAll.SetVisible(false)
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
// auto-stretched picture and the header. A frame of a receiver is binned
// there, so only the small preview comes over the network.
func (w *window) showPreview() {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.model.rows) {
		return
	}
	f := w.model.rows[i].f
	gen := w.previewGen.Add(1)
	name := filepath.Base(filepath.FromSlash(f.Path))
	w.previewInfo.SetText("Читаю " + name + "…")
	go func() {
		var pic image.Image
		var info, head string
		var im *fits.Image
		var err error
		if f.Src != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			im, err = f.Src.Preview(ctx, f.Path, 1200)
			cancel()
			var b strings.Builder
			for _, c := range f.Cards {
				fmt.Fprintf(&b, "%-8s = %s\r\n", c.Key, c.Value)
			}
			head = b.String()
		} else {
			var file *os.File
			if file, err = os.Open(f.Path); err == nil {
				im, err = fits.Preview(file, 1200)
				file.Close()
			}
			if err == nil {
				var b strings.Builder
				for _, k := range im.Header.Keys() {
					fmt.Fprintf(&b, "%-8s = %s\r\n", k, im.Header.Get(k))
				}
				head = b.String()
			}
		}
		if err == nil {
			pic = im.Stretched()
			info = fmt.Sprintf("%s — %d×%d", name, im.Width*im.Binning, im.Height*im.Binning)
			if im.Bayer != "" {
				info += ", " + im.Bayer
			}
			if f.Src != nil {
				info += " (с приёмника)"
			}
		} else {
			info = fmt.Sprintf("%s: %v", name, err)
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
