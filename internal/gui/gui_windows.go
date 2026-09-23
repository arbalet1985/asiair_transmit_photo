package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"github.com/arbalet1985/asiair_transmit_photo/configs"
	"github.com/arbalet1985/asiair_transmit_photo/internal/agent"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

const (
	appName     = "asiair-agent"
	windowTitle = "ASIAIR Agent — передача снимков"
)

// runner starts and stops the agent inside the window process.
type runner struct {
	mu     sync.Mutex
	agent  *agent.Agent
	cancel context.CancelFunc
	done   chan struct{}
	err    error // configuration error when the agent could not start
}

func (r *runner) start(cfgPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agent, r.err = nil, nil
	cfg, err := config.LoadAgent(cfgPath)
	if err == nil {
		r.agent, err = agent.New(cfg)
	}
	if err != nil {
		r.err = err
		log.Printf("Агент не запущен: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.cancel, r.done = cancel, done
	a := r.agent
	go func() {
		defer close(done)
		a.Run(ctx)
	}()
}

func (r *runner) stop() {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
}

func (r *runner) state() (*agent.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agent, r.err
}

type window struct {
	cfgPath      string
	settingsPath string
	settings     Settings
	logs         *LogBuffer
	logLines     int
	run          runner

	mw        *walk.MainWindow
	ni        *walk.NotifyIcon
	icons     map[string]*walk.Icon
	iconState string

	lblASIAIR, lblReceiver, lblFile, lblSpeed, lblQueue, lblSent, lblErr *walk.Label
	progress                                                             *walk.ProgressBar
	cbAutostart, cbMinimized                                             *walk.CheckBox
	logView                                                              *walk.TextEdit

	ready    bool // set after the window is built; checkbox handlers ignore the initial state
	quitting bool
	hinted   bool
}

// Run shows the agent window (or only the tray icon when "start minimized"
// is on) and runs the agent until the user chooses "Выход".
func Run(cfgPath string) error {
	detachConsole()
	release, ok := svc.SingleInstance(appName)
	if !ok {
		// Another window is running (maybe hidden in the tray): show it.
		if title, err := syscall.UTF16PtrFromString(windowTitle); err == nil {
			if h := win.FindWindow(nil, title); h != 0 {
				win.ShowWindow(h, win.SW_SHOW)
				win.ShowWindow(h, win.SW_RESTORE)
				win.SetForegroundWindow(h)
				return nil
			}
		}
		walk.MsgBox(nil, "ASIAIR Agent",
			"Агент уже работает: как служба Windows или в другом окне (значок в трее).\n\n"+
				"Чтобы пользоваться окном вместо службы, удалите службу командой\n"+
				"asiair-agent uninstall (от имени администратора).",
			walk.MsgBoxIconInformation)
		return nil
	}
	defer release()

	w := &window{
		cfgPath:      cfgPath,
		settingsPath: SettingsPath(cfgPath),
		logs:         NewLogBuffer(500),
	}
	w.settings = LoadSettings(w.settingsPath)
	w.setupLog()
	refreshAutostart(cfgPath)
	created := w.ensureConfig()

	if err := w.create(); err != nil {
		return err
	}
	defer w.ni.Dispose()

	w.run.start(cfgPath)
	w.update()
	if created {
		walk.MsgBox(w.mw, "ASIAIR Agent",
			"Создан файл настроек:\n"+cfgPath+"\n\n"+
				"Укажите адрес ASIAIR, адрес приёмника и токен, сохраните файл "+
				"и нажмите «Перезапустить агент».", walk.MsgBoxIconInformation)
		w.openConfig()
	}

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				w.mw.Synchronize(w.update)
			}
		}
	}()
	w.mw.Run()
	close(stop)
	w.run.stop()
	return nil
}

func (w *window) setupLog() {
	logFile := svc.DefaultLogFile(appName)
	if cfg, err := config.LoadAgent(w.cfgPath); err == nil && cfg.LogFile != "" {
		logFile = cfg.LogFile
	}
	out := io.Writer(w.logs)
	if f, err := svc.OpenLog(logFile); err == nil {
		out = io.MultiWriter(f, w.logs)
	}
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(out)
}

// ensureConfig writes the example configuration if there is none yet.
func (w *window) ensureConfig() bool {
	if _, err := os.Stat(w.cfgPath); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	os.MkdirAll(filepath.Dir(w.cfgPath), 0o755)
	if err := os.WriteFile(w.cfgPath, configs.AgentExample, 0o644); err != nil {
		log.Printf("Не удалось создать %s: %v", w.cfgPath, err)
		return false
	}
	return true
}

func (w *window) create() error {
	minimized := w.settings.StartMinimized
	err := MainWindow{
		AssignTo: &w.mw,
		Title:    windowTitle,
		MinSize:  Size{Width: 520, Height: 460},
		Size:     Size{Width: 700, Height: 560},
		Visible:  !minimized,
		Layout:   VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Состояние",
				Layout: Grid{Columns: 2, Spacing: 6},
				Children: []Widget{
					Label{Text: "ASIAIR:"}, Label{AssignTo: &w.lblASIAIR, Text: "—"},
					Label{Text: "Приёмник:"}, Label{AssignTo: &w.lblReceiver, Text: "—"},
					Label{Text: "Передаётся:"}, Label{AssignTo: &w.lblFile, Text: "—"},
					Label{Text: ""}, ProgressBar{AssignTo: &w.progress, MaxValue: 1000},
					Label{Text: "Скорость:"}, Label{AssignTo: &w.lblSpeed, Text: "—"},
					Label{Text: "Очередь:"}, Label{AssignTo: &w.lblQueue, Text: "—"},
					Label{Text: "Отправлено:"}, Label{AssignTo: &w.lblSent, Text: "—"},
					Label{Text: "Ошибка:"}, Label{AssignTo: &w.lblErr, Text: "—", TextColor: walk.RGB(200, 40, 40)},
				},
			},
			GroupBox{
				Title:  "Запуск",
				Layout: VBox{Alignment: AlignHNearVCenter},
				Children: []Widget{
					CheckBox{
						AssignTo:            &w.cbAutostart,
						Text:                "Запускать автоматически при входе в Windows (автозагрузка)",
						Checked:             AutostartEnabled(),
						OnCheckStateChanged: w.onAutostart,
					},
					CheckBox{
						AssignTo:            &w.cbMinimized,
						Text:                "Запускать свёрнутым в трей",
						Checked:             minimized,
						OnCheckStateChanged: w.onMinimized,
					},
					HSpacer{}, // let the box take the full width
				},
			},
			GroupBox{
				Title:         "Журнал",
				Layout:        VBox{},
				StretchFactor: 1,
				Children: []Widget{
					TextEdit{AssignTo: &w.logView, ReadOnly: true, VScroll: true, MinSize: Size{Height: 120}},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Настройки…", OnClicked: w.openConfig},
					PushButton{Text: "Перезапустить агент", OnClicked: w.restart},
					PushButton{Text: "Открыть журнал", OnClicked: w.openLog},
					HSpacer{},
					PushButton{Text: "Свернуть в трей", OnClicked: w.hide},
					PushButton{Text: "Выход", OnClicked: w.quit},
				},
			},
		},
	}.Create()
	if err != nil {
		return err
	}

	// window icon and tray icon
	w.icons = map[string]*walk.Icon{}
	dpi := w.mw.DPI()
	for _, st := range []string{StateStopped, StateError, StateIdle, StateUploading} {
		ic, err := walk.NewIconFromImageForDPI(IconImage(st, 16*dpi/96), dpi)
		if err == nil {
			w.icons[st] = ic
		}
	}
	if big, err := walk.NewIconFromImageForDPI(IconImage(StateIdle, 32*dpi/96), dpi); err == nil {
		w.mw.SetIcon(big)
	}

	if w.ni, err = walk.NewNotifyIcon(w.mw); err != nil {
		return err
	}
	w.setTray(StateStopped, "ASIAIR Agent")
	w.ni.SetVisible(true)
	w.ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			w.show()
		}
	})
	w.ni.MessageClicked().Attach(w.show)
	addAction := func(text string, fn func()) {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(fn)
		w.ni.ContextMenu().Actions().Add(a)
	}
	addAction("Открыть окно", w.show)
	addAction("Перезапустить агент", w.restart)
	w.ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
	addAction("Выход", w.quit)

	// minimize and close hide the window to the tray
	w.mw.SizeChanged().Attach(func() {
		if !w.quitting && win.IsIconic(w.mw.Handle()) {
			w.hide()
		}
	})
	w.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !w.quitting {
			*canceled = true
			w.hide()
		}
	})

	for _, l := range w.logs.Lines() {
		w.appendLog(l)
	}
	w.ready = true
	w.logs.OnLine(func(l string) {
		w.mw.Synchronize(func() { w.appendLog(l) })
	})
	return nil
}

func (w *window) appendLog(line string) {
	if w.logLines >= 1000 {
		// keep the view short: reload the last lines
		lines := w.logs.Lines()
		if len(lines) > 300 {
			lines = lines[len(lines)-300:]
		}
		w.logView.SetText(strings.Join(lines, "\r\n") + "\r\n")
		w.logLines = len(lines)
		return
	}
	w.logView.AppendText(line + "\r\n")
	w.logLines++
}

func (w *window) show() {
	w.mw.Show()
	win.ShowWindow(w.mw.Handle(), win.SW_RESTORE)
	win.SetForegroundWindow(w.mw.Handle())
}

func (w *window) hide() {
	w.mw.Hide()
	if !w.hinted {
		w.hinted = true
		w.ni.ShowInfo("ASIAIR Agent", "Агент продолжает работать. Окно открывается щелчком по значку в трее.")
	}
}

func (w *window) quit() {
	if walk.MsgBox(w.mw, "ASIAIR Agent",
		"Остановить передачу снимков и закрыть программу?",
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	w.quitting = true
	w.ni.SetVisible(false)
	w.mw.Close()
}

func (w *window) restart() {
	log.Printf("Перезапуск агента...")
	go func() {
		w.run.stop()
		w.run.start(w.cfgPath)
		w.mw.Synchronize(w.update)
	}()
}

func (w *window) openConfig() {
	w.ensureConfig()
	if err := exec.Command("notepad.exe", w.cfgPath).Start(); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Не удалось открыть "+w.cfgPath+": "+err.Error(), walk.MsgBoxIconError)
	}
}

func (w *window) openLog() {
	logFile := svc.DefaultLogFile(appName)
	if cfg, err := config.LoadAgent(w.cfgPath); err == nil && cfg.LogFile != "" {
		logFile = cfg.LogFile
	}
	exec.Command("notepad.exe", logFile).Start()
}

func (w *window) onAutostart() {
	if !w.ready {
		return
	}
	on := w.cbAutostart.Checked()
	if err := SetAutostart(on, w.cfgPath); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Не удалось изменить автозагрузку: "+err.Error(), walk.MsgBoxIconError)
		w.cbAutostart.SetChecked(AutostartEnabled())
		return
	}
	if on {
		log.Printf("Автозагрузка включена")
	} else {
		log.Printf("Автозагрузка выключена")
	}
}

func (w *window) onMinimized() {
	if !w.ready {
		return
	}
	w.settings.StartMinimized = w.cbMinimized.Checked()
	if err := SaveSettings(w.settingsPath, w.settings); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Не удалось сохранить настройку: "+err.Error(), walk.MsgBoxIconError)
	}
}

func (w *window) setTray(state, tip string) {
	if state != w.iconState {
		if ic := w.icons[state]; ic != nil {
			w.ni.SetIcon(ic)
		}
		w.iconState = state
	}
	if r := []rune(tip); len(r) > 120 { // Windows limits tooltips to 127 chars
		tip = string(r[:120]) + "…"
	}
	w.ni.SetToolTip(tip)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с назад", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	}
	return t.Format("02.01 15:04")
}

// update refreshes the window and the tray icon from the agent state.
func (w *window) update() {
	a, cfgErr := w.run.state()
	if a == nil {
		msg := "агент не запущен"
		if cfgErr != nil {
			msg = "ошибка в настройках: " + cfgErr.Error()
		}
		for _, l := range []*walk.Label{w.lblASIAIR, w.lblReceiver, w.lblFile, w.lblSpeed, w.lblQueue, w.lblSent} {
			l.SetText("—")
		}
		w.lblErr.SetText(msg)
		w.progress.SetValue(0)
		w.setTray(StateStopped, "ASIAIR Agent — "+msg)
		return
	}
	st := a.Status()
	lastSync, syncErr := a.Receiver()

	switch {
	case st.LastScan.IsZero() && st.LastError == "":
		w.lblASIAIR.SetText(st.ASIAIR + " — подключение…")
	case st.Connected:
		w.lblASIAIR.SetText(fmt.Sprintf("%s — подключено, обход %s", st.ASIAIR, ago(st.LastScan)))
	default:
		w.lblASIAIR.SetText(st.ASIAIR + " — нет связи")
	}
	switch {
	case syncErr != "":
		w.lblReceiver.SetText("нет связи")
	case lastSync.IsZero():
		w.lblReceiver.SetText("подключение…")
	default:
		w.lblReceiver.SetText("на связи (" + ago(lastSync) + ")")
	}
	if st.Uploading != "" {
		w.lblFile.SetText(st.Uploading)
		if st.UploadSize > 0 {
			w.progress.SetValue(int(st.UploadDone * 1000 / st.UploadSize))
		}
		w.lblSpeed.SetText(fmt.Sprintf("%s/s (лимит: %s)", config.FormatBytes(int64(st.Speed)), config.FormatRate(st.Rate)))
	} else {
		w.lblFile.SetText("—")
		w.progress.SetValue(0)
		w.lblSpeed.SetText("— (лимит: " + config.FormatRate(st.Rate) + ")")
	}
	w.lblQueue.SetText(fmt.Sprintf("%d файл(ов), %s", st.QueueFiles, config.FormatBytes(st.QueueBytes)))
	w.lblSent.SetText(fmt.Sprintf("%d файл(ов), %s с момента запуска", st.SentFiles, config.FormatBytes(st.SentBytes)))
	errText := st.LastError
	if errText == "" && syncErr != "" {
		errText = syncErr
	}
	if errText == "" {
		errText = "—"
	}
	w.lblErr.SetText(errText)

	state, tip := trayState(st, syncErr)
	w.setTray(state, tip)
}

func trayState(st protocol.AgentStatus, syncErr string) (string, string) {
	switch {
	case !st.Connected && !st.LastScan.IsZero() || !st.Connected && st.LastError != "":
		return StateError, "ASIAIR Agent — нет связи с ASIAIR"
	case syncErr != "":
		return StateError, "ASIAIR Agent — нет связи с приёмником"
	case st.Uploading != "":
		pct := 0
		if st.UploadSize > 0 {
			pct = int(st.UploadDone * 100 / st.UploadSize)
		}
		return StateUploading, fmt.Sprintf("ASIAIR Agent — передаётся %s (%d%%), в очереди %d",
			filepath.Base(st.Uploading), pct, st.QueueFiles)
	}
	return StateIdle, fmt.Sprintf("ASIAIR Agent — на связи, отправлено %d", st.SentFiles)
}
