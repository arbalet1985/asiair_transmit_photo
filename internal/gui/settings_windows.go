package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/agent"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

// Placeholder values from the example configuration: shown as empty fields.
var placeholders = map[string]bool{
	"ВСТАВЬТЕ_ТОКЕН":      true,
	"my-home.example.com": true,
}

// settingsForm holds the controls of the "Настройки" tab.
type settingsForm struct {
	asiair, user, pass           *walk.LineEdit
	host, token, fingerprint     *walk.LineEdit
	port                         *walk.NumberEdit
	asiairResult, receiverResult *walk.Label
	testASIAIR, testReceiver     *walk.PushButton
	findASIAIR                   *walk.PushButton
	scheme                       string // "https" unless receiver_url says otherwise
}

var (
	colorOK  = walk.RGB(30, 130, 60)
	colorBad = walk.RGB(200, 40, 40)
	colorDim = walk.RGB(110, 110, 110)
)

func (w *window) settingsPage(minimized bool) []Widget {
	f := &w.form
	hint := func(text string) Widget {
		return Label{Text: text, TextColor: colorDim}
	}
	return []Widget{
		GroupBox{
			Title:  "ASIAIR (в сети обсерватории)",
			Layout: Grid{Columns: 3, Spacing: 4},
			Children: []Widget{
				Label{Text: "Сетевой путь:"},
				LineEdit{AssignTo: &f.asiair, ToolTipText: `Например \\192.168.1.50\EMMC Images или просто 192.168.1.50`},
				PushButton{AssignTo: &f.testASIAIR, Text: "Проверить", OnClicked: w.testASIAIR},
				Label{}, hint(`например \\192.168.1.50\EMMC Images или просто 192.168.1.50`), Label{},

				Label{},
				Composite{
					Layout: HBox{MarginsZero: true},
					Children: []Widget{
						PushButton{AssignTo: &f.findASIAIR, Text: "Найти в сети…",
							ToolTipText: "Просканировать локальную сеть и найти ASIAIR или StellaVita",
							OnClicked:   w.findDevices},
						hint("ищет ASIAIR или StellaVita в сети обсерватории"),
						HSpacer{},
					},
				},
				Label{},

				Label{Text: "Логин:"},
				LineEdit{AssignTo: &f.user},
				Label{},
				Label{}, hint("обычно не нужен: ASIAIR пускает гостя без пароля"), Label{},

				Label{Text: "Пароль:"},
				LineEdit{AssignTo: &f.pass, PasswordMode: true},
				Label{},

				Label{},
				Label{AssignTo: &f.asiairResult, ColumnSpan: 2, TextColor: colorDim,
					Text: "Нажмите «Проверить», чтобы убедиться, что ASIAIR доступен."},
			},
		},
		GroupBox{
			Title:  "Компьютер, куда скачиваются снимки (приёмник)",
			Layout: Grid{Columns: 3, Spacing: 4},
			Children: []Widget{
				Label{Text: "Адрес:"},
				LineEdit{AssignTo: &f.host, ToolTipText: "Внешний IP-адрес или имя компьютера с asiair-receiver"},
				PushButton{AssignTo: &f.testReceiver, Text: "Проверить", OnClicked: w.testReceiver},
				Label{}, hint("внешний IP или имя, например 93.184.216.34 или home.example.com"), Label{},

				Label{Text: "Порт:"},
				NumberEdit{AssignTo: &f.port, Decimals: 0, MinValue: 1, MaxValue: 65535,
					Value: float64(config.DefaultReceiverPort), MaxSize: Size{Width: 90}},
				Label{},

				Label{Text: "Токен:"},
				LineEdit{AssignTo: &f.token, ToolTipText: "Тот же, что token в receiver.yaml"},
				Label{},
				Label{}, hint("тот же, что token в receiver.yaml (команда: asiair-receiver token)"), Label{},

				Label{Text: "Отпечаток сертификата:"},
				LineEdit{AssignTo: &f.fingerprint, ToolTipText: "Заполняется автоматически при проверке"},
				Label{},
				Label{}, hint("заполняется автоматически при первой проверке"), Label{},

				Label{},
				Label{AssignTo: &f.receiverResult, ColumnSpan: 2, TextColor: colorDim,
					Text: "Нажмите «Проверить», чтобы убедиться, что приёмник доступен."},
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
				HSpacer{},
			},
		},
		VSpacer{},
		Composite{
			Layout: HBox{MarginsZero: true},
			Children: []Widget{
				PushButton{Text: "Файл настроек…", ToolTipText: "Остальные параметры: лимит скорости, фильтры, интервалы", OnClicked: w.openConfig},
				HSpacer{},
				PushButton{Text: "Сохранить и применить", OnClicked: w.saveSettings},
			},
		},
	}
}

// loadForm fills the form from agent.yaml.
func (w *window) loadForm() {
	f := &w.form
	c := config.PeekAgent(w.cfgPath)
	clean := func(v string) string {
		if placeholders[v] {
			return ""
		}
		return v
	}
	f.asiair.SetText(c.ASIAIR)
	f.user.SetText(c.Username)
	f.pass.SetText(c.Password)
	scheme, host, port := config.SplitReceiverURL(c.ReceiverURL)
	f.scheme = scheme
	f.host.SetText(clean(host))
	f.port.SetValue(float64(port))
	f.token.SetText(clean(c.Token))
	f.fingerprint.SetText(c.ReceiverFingerprint)
}

func (w *window) receiverURL() (string, error) {
	f := &w.form
	return config.JoinReceiverURL(f.scheme, f.host.Text(), int(f.port.Value()))
}

func setResult(l *walk.Label, ok bool, text string) {
	if ok {
		l.SetTextColor(colorOK)
		text = "OK: " + text
	} else {
		l.SetTextColor(colorBad)
		text = "Ошибка: " + text
	}
	setShort(l, text, maxResult)
}

// explain turns common network errors into advice.
func explain(err error, what string) string {
	var ne net.Error
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return what + " не отвечает: проверьте адрес и сеть"
	case strings.Contains(msg, "refused"):
		return what + ": порт закрыт или программа не запущена"
	case strings.Contains(msg, "no such host"):
		return what + ": такое имя не найдено, проверьте адрес"
	case strings.Contains(msg, "отпечаток"):
		return "отпечаток сертификата не совпадает — очистите поле и проверьте снова"
	case strings.Contains(msg, "токен"):
		return "приёмник отклонил токен — сверьте с receiver.yaml"
	case strings.Contains(msg, "logon failure") || strings.Contains(msg, "LOGON_FAILURE") || strings.Contains(msg, "Logon failure"):
		return what + ": неверный логин или пароль"
	case strings.Contains(msg, "Network Name Not Found") || strings.Contains(msg, "share name cannot be found"):
		return what + ": шара с таким именем не найдена"
	}
	return msg
}

func (w *window) testASIAIR() {
	f := &w.form
	link := strings.TrimSpace(f.asiair.Text())
	if link == "" {
		setResult(f.asiairResult, false, "Укажите сетевой путь к ASIAIR.")
		return
	}
	shares := config.PeekAgent(w.cfgPath).Shares
	user, pass := strings.TrimSpace(f.user.Text()), f.pass.Text()
	f.testASIAIR.SetEnabled(false)
	f.asiairResult.SetTextColor(colorDim)
	f.asiairResult.SetText("Проверка…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var res *source.ProbeResult
		src, err := source.New(link, shares, user, pass, "")
		if err == nil {
			res, err = src.Probe(ctx)
			src.Close()
		}
		w.mw.Synchronize(func() {
			f.testASIAIR.SetEnabled(true)
			if err != nil {
				setResult(f.asiairResult, false, explain(err, "ASIAIR"))
				log.Printf("Проверка ASIAIR %s: %v", link, err)
				return
			}
			var names, details []string
			for _, sh := range res.Shares {
				if sh.Err != nil {
					names = append(names, sh.Name+" (нет)")
					details = append(details, fmt.Sprintf("%s: %v", sh.Name, sh.Err))
				} else {
					names = append(names, sh.Name)
					details = append(details, fmt.Sprintf("%s: файлов и папок в корне: %d", sh.Name, sh.Entries))
				}
			}
			setResult(f.asiairResult, true, "вход: "+res.Login+", шары: "+strings.Join(names, ", "))
			msg := "доступен, вход: " + res.Login + "; " + strings.Join(details, "; ")
			log.Printf("Проверка ASIAIR %s: %s", link, msg)
		})
	}()
}

// findDevices scans the local network and fills the ASIAIR fields with
// the first ASIAIR or StellaVita found.
func (w *window) findDevices() {
	f := &w.form
	f.findASIAIR.SetEnabled(false)
	f.testASIAIR.SetEnabled(false)
	f.asiairResult.SetTextColor(colorDim)
	f.asiairResult.SetText("Поиск в сети…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		devs, err := source.Discover(ctx, func(s string) {
			log.Printf("Поиск ASIAIR: %s", s)
			w.mw.Synchronize(func() { setShort(f.asiairResult, s, maxResult) })
		})
		w.mw.Synchronize(func() {
			f.findASIAIR.SetEnabled(true)
			f.testASIAIR.SetEnabled(true)
			if err != nil && len(devs) == 0 {
				setResult(f.asiairResult, false, "поиск не удался: "+err.Error())
				log.Printf("Поиск ASIAIR: %v", err)
				return
			}
			if len(devs) == 0 {
				setResult(f.asiairResult, false, "ASIAIR и StellaVita в сети не найдены. Проверьте, что компьютер подключён к той же сети, и укажите адрес вручную.")
				log.Printf("Поиск ASIAIR: ничего не найдено")
				return
			}
			var list []string
			for _, d := range devs {
				s := d.Kind + " " + d.IP
				if d.Name != "" {
					s += " (" + d.Name + ")"
				}
				list = append(list, s)
				log.Printf("Поиск ASIAIR: найден %s, шары: %s", s, strings.Join(d.Shares, ", "))
			}
			d := devs[0]
			if len(devs) > 1 {
				items := make([]string, len(devs))
				for i, s := range list {
					items[i] = fmt.Sprintf("%d. %s", i+1, s)
				}
				if walk.MsgBox(w.mw, "Найдено несколько устройств",
					"Найдено:\n\n"+strings.Join(items, "\n")+"\n\nИспользовать первое ("+list[0]+")?",
					walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
					setResult(f.asiairResult, true, "найдено: "+strings.Join(list, "; ")+". Впишите нужный адрес вручную.")
					return
				}
			}
			f.asiair.SetText(d.Link())
			f.user.SetText(d.Username)
			f.pass.SetText(d.Password)
			msg := "найден " + list[0] + ", путь " + d.Link()
			if d.Username != "" {
				msg += ", логин " + d.Username
			}
			setResult(f.asiairResult, true, msg+". Нажмите «Проверить», затем «Сохранить».")
		})
	}()
}

func (w *window) testReceiver() {
	f := &w.form
	u, err := w.receiverURL()
	if err != nil {
		setResult(f.receiverResult, false, err.Error())
		return
	}
	token := strings.TrimSpace(f.token.Text())
	fp := strings.TrimSpace(f.fingerprint.Text())
	f.testReceiver.SetEnabled(false)
	f.receiverResult.SetTextColor(colorDim)
	f.receiverResult.SetText("Проверка " + u + "…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var newFP string
		var err error
		if fp == "" && strings.HasPrefix(u, "https://") {
			newFP, err = agent.FetchFingerprint(ctx, u)
		}
		w.mw.Synchronize(func() {
			if err != nil {
				f.testReceiver.SetEnabled(true)
				setResult(f.receiverResult, false, explain(err, "Приёмник"))
				log.Printf("Проверка приёмника %s: %v", u, err)
				return
			}
			if newFP != "" {
				// trust on first use, after the user compares it
				if walk.MsgBox(w.mw, "Сертификат приёмника",
					"Приёмник прислал сертификат с отпечатком:\n\n"+newFP+"\n\n"+
						"Сверьте его с отпечатком на веб-странице приёмника или в выводе "+
						"команды «asiair-receiver fingerprint». Совпадает?",
					walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
					f.testReceiver.SetEnabled(true)
					setResult(f.receiverResult, false, "Сертификат не подтверждён.")
					return
				}
				f.fingerprint.SetText(newFP)
			}
			fp := strings.TrimSpace(f.fingerprint.Text())
			if token == "" {
				f.testReceiver.SetEnabled(true)
				setResult(f.receiverResult, false, "Приёмник доступен, но не указан токен.")
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				err := agent.CheckReceiver(ctx, u, token, fp)
				w.mw.Synchronize(func() {
					f.testReceiver.SetEnabled(true)
					if err != nil {
						setResult(f.receiverResult, false, explain(err, "Приёмник"))
						log.Printf("Проверка приёмника %s: %v", u, err)
						return
					}
					setResult(f.receiverResult, true, "Приёмник доступен, токен и сертификат подходят.")
					log.Printf("Проверка приёмника %s: OK", u)
				})
			}()
		})
	}()
}

func (w *window) saveSettings() {
	f := &w.form
	u, err := w.receiverURL()
	if err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", err.Error(), walk.MsgBoxIconError)
		return
	}
	if _, err := source.ParseLink(f.asiair.Text()); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", err.Error(), walk.MsgBoxIconError)
		return
	}
	err = config.UpdateAgentFile(w.cfgPath, map[string]string{
		"asiair":               strings.TrimSpace(f.asiair.Text()),
		"username":             strings.TrimSpace(f.user.Text()),
		"password":             f.pass.Text(),
		"receiver_url":         u,
		"token":                strings.TrimSpace(f.token.Text()),
		"receiver_fingerprint": strings.TrimSpace(f.fingerprint.Text()),
	})
	if err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Настройки не сохранены: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	log.Printf("Настройки сохранены в %s", w.cfgPath)
	w.tabs.SetCurrentIndex(0)
	w.restart()
}

func (w *window) openConfig() {
	w.ensureConfig()
	if err := exec.Command("notepad.exe", w.cfgPath).Start(); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Не удалось открыть "+w.cfgPath+": "+err.Error(), walk.MsgBoxIconError)
	}
}
