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
	"github.com/arbalet1985/asiair_transmit_photo/internal/autorun"
	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/firewall"
	"github.com/arbalet1985/asiair_transmit_photo/internal/netcheck"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
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
	findASIAIR, chooseUSB        *walk.PushButton
	conn                         *walk.ComboBox
	tsKey                        *walk.LineEdit
	hostHint, tsHint             *walk.Label
	scheme                       string // "https" unless receiver_url says otherwise
}

// connection kinds in the order of the "Способ связи" list
var connKinds = []string{config.ConnInternet, config.ConnTailscale}

const (
	hostHintInternet  = "внешний IP или имя, например 93.184.216.34 или home.example.com"
	hostHintTailscale = "имя приёмника в Tailscale (обычно asiair-receiver) или его адрес 100.x.x.x"
)

func (f *settingsForm) connection() string {
	if i := f.conn.CurrentIndex(); i >= 0 && i < len(connKinds) {
		return connKinds[i]
	}
	return config.ConnInternet
}

// onConnChanged adapts the hints to the chosen connection.
func (w *window) onConnChanged() {
	f := &w.form
	ts := f.connection() == config.ConnTailscale
	if ts {
		f.hostHint.SetText(hostHintTailscale)
	} else {
		f.hostHint.SetText(hostHintInternet)
	}
	f.tsKey.SetEnabled(ts)
	f.tsHint.SetEnabled(ts)
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
			Title:  "Откуда брать снимки: ASIAIR, StellaVita или флешка",
			Layout: Grid{Columns: 3, Spacing: 4},
			Children: []Widget{
				Label{Text: "Путь:"},
				LineEdit{AssignTo: &f.asiair, ToolTipText: `Например \\192.168.1.50\EMMC Images, 192.168.1.50 или usb: (флешка из ASIAIR в этом компьютере)`},
				PushButton{AssignTo: &f.testASIAIR, Text: "Проверить", OnClicked: w.testASIAIR},
				Label{}, hint(`например \\192.168.1.50\EMMC Images или просто 192.168.1.50`), Label{},

				Label{},
				Composite{
					Layout: HBox{MarginsZero: true},
					Children: []Widget{
						PushButton{AssignTo: &f.findASIAIR, Text: "Найти в сети…",
							ToolTipText: "Просканировать локальную сеть и найти ASIAIR или StellaVita",
							OnClicked:   w.findDevices},
						PushButton{AssignTo: &f.chooseUSB, Text: "Флешка…",
							ToolTipText: "Брать снимки с флешки, вынутой из ASIAIR и вставленной в этот компьютер",
							OnClicked:   w.chooseUSBSource},
						hint("сеть обсерватории или флешка из ASIAIR"),
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
				Label{Text: "Способ связи:"},
				ComboBox{AssignTo: &f.conn, Model: []string{
					"Через интернет (порт открыт на роутере)",
					"Через Tailscale (без проброса портов, бесплатно)",
				}, CurrentIndex: 0, OnCurrentIndexChanged: w.onConnChanged},
				Label{},

				Label{Text: "Адрес:"},
				LineEdit{AssignTo: &f.host, ToolTipText: "Внешний IP-адрес или имя компьютера с asiair-receiver, либо его имя в Tailscale"},
				PushButton{AssignTo: &f.testReceiver, Text: "Проверить", OnClicked: w.testReceiver},
				Label{}, Label{AssignTo: &f.hostHint, Text: hostHintInternet, TextColor: colorDim}, Label{},

				Label{Text: "Порт:"},
				NumberEdit{AssignTo: &f.port, Decimals: 0, MinValue: 1, MaxValue: 65535,
					Value: float64(config.DefaultReceiverPort), MaxSize: Size{Width: 90}},
				Label{},

				Label{Text: "Токен:"},
				LineEdit{AssignTo: &f.token, ToolTipText: "Тот же, что token в receiver.yaml"},
				Label{},
				Label{}, hint("тот же, что token в receiver.yaml (команда: asiair-receiver token)"), Label{},

				Label{Text: "Ключ Tailscale:"},
				LineEdit{AssignTo: &f.tsKey, PasswordMode: true, ToolTipText: "Необязательно: tskey-auth-… из login.tailscale.com → Settings → Keys"},
				Label{},
				Label{}, Label{AssignTo: &f.tsHint, TextColor: colorDim,
					Text: "необязательно: без ключа агент покажет кнопку «Войти в Tailscale» на вкладке «Состояние»"}, Label{},

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
			Title:  "Запуск и обновления",
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
				CheckBox{
					AssignTo:            &w.cbAutoUpdate,
					Text:                "Устанавливать новые версии автоматически, без кнопки «Обновить»",
					ToolTipText:         "Новая версия ставится сразу, как приёмник её передаст; если она не заработает, через несколько минут вернётся прежняя",
					Checked:             w.settings.AutoUpdate,
					OnCheckStateChanged: w.onAutoUpdate,
				},
				HSpacer{},
			},
		},
		VSpacer{},
		Composite{
			Layout: HBox{MarginsZero: true},
			Children: []Widget{
				PushButton{Text: "Файл настроек…", ToolTipText: "Остальные параметры: фильтры, интервалы, журналы", OnClicked: w.openConfig},
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
	f.conn.SetCurrentIndex(0)
	if c.UseTailscale() {
		f.conn.SetCurrentIndex(1)
	}
	f.tsKey.SetText(c.TailscaleAuthKey)
	w.onConnChanged()
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
		setResult(f.asiairResult, false, "Укажите путь к ASIAIR или флешке.")
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
		// first: does the device answer on the file sharing port at all
		if l, err := source.ParseLink(link); err == nil && l.Kind == "smb" {
			hp := l.Host
			if _, _, err := net.SplitHostPort(hp); err != nil {
				hp = net.JoinHostPort(hp, "445")
			}
			if r := netcheck.Check(ctx, hp, 10*time.Second); !r.OK() {
				w.mw.Synchronize(func() {
					f.testASIAIR.SetEnabled(true)
					w.netFailed(f.asiairResult, "ASIAIR", r, r.Advice("asiair"))
				})
				return
			}
		}
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

// chooseUSBSource switches the source to the ASIAIR USB stick plugged into
// this computer, or to a folder picked by hand.
func (w *window) chooseUSBSource() {
	f := &w.form
	link := ""
	drives := source.RemovableDrives()
	if len(drives) > 0 {
		names := make([]string, len(drives))
		for i, d := range drives {
			names[i] = d.String()
		}
		switch walk.MsgBox(w.mw, "Флешка",
			"Найдены съёмные диски: "+strings.Join(names, ", ")+".\n\n"+
				"«Да» — брать снимки с флешки, какая бы буква диска ей ни досталась "+
				"(если вставлено несколько, берётся первая: "+drives[0].Root+").\n"+
				"«Нет» — выбрать диск или папку вручную.",
			walk.MsgBoxYesNoCancel|walk.MsgBoxIconQuestion) {
		case walk.DlgCmdYes:
			link = "usb:"
		case walk.DlgCmdCancel:
			return
		}
	}
	if link == "" {
		dlg := walk.FileDialog{Title: "Выберите флешку из ASIAIR (корень диска) или папку со снимками"}
		if len(drives) > 0 {
			dlg.InitialDirPath = drives[0].Root
		}
		ok, err := dlg.ShowBrowseFolder(w.mw)
		if err != nil {
			walk.MsgBox(w.mw, "ASIAIR Agent", err.Error(), walk.MsgBoxIconError)
			return
		}
		if !ok || dlg.FilePath == "" {
			return
		}
		link = source.FolderLink(dlg.FilePath)
	}
	f.asiair.SetText(link)
	f.user.SetText("")
	f.pass.SetText("")
	log.Printf("Источник снимков: %s", link)
	w.testASIAIR()
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

// netFailed shows where the network check stopped and what to look at.
func (w *window) netFailed(l *walk.Label, what string, r netcheck.Result, advice string) {
	setResult(l, false, r.Summary()+". "+advice)
	log.Printf("Проверка: %s: %s. %s", what, r.Summary(), advice)
	walk.MsgBox(w.mw, what+" недоступен", r.Summary()+".\n\n"+advice, walk.MsgBoxIconWarning)
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
	// Tailscale: the check goes through the connection of the running agent
	var dial agent.DialFunc
	if f.connection() == config.ConnTailscale {
		a, _ := w.run.state()
		var st *tsnode.Status
		if a != nil {
			st = a.Tailscale()
		}
		switch {
		case st == nil:
			setResult(f.receiverResult, false, "Tailscale ещё не включён: нажмите «Сохранить и применить», затем войдите в Tailscale на вкладке «Состояние».")
			return
		case st.State != tsnode.StateRunning:
			setResult(f.receiverResult, false, "Tailscale: "+st.Text()+". Войдите на вкладке «Состояние» и проверьте снова.")
			return
		}
		dial = a.Dial()
	}
	f.testReceiver.SetEnabled(false)
	f.receiverResult.SetTextColor(colorDim)
	f.receiverResult.SetText("Проверка " + u + "…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// first: does anything answer on the receiver port at all
		if dial == nil {
			r := netcheck.Check(ctx, config.ReceiverHostPort(u), 10*time.Second)
			if !r.OK() {
				w.mw.Synchronize(func() {
					f.testReceiver.SetEnabled(true)
					w.netFailed(f.receiverResult, "Приёмник", r, r.Advice("receiver"))
				})
				return
			}
			log.Printf("Проверка приёмника: %s", r.Summary())
		}
		var newFP string
		var err error
		if fp == "" && strings.HasPrefix(u, "https://") {
			newFP, err = agent.FetchFingerprint(ctx, u, dial)
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
				err := agent.CheckReceiver(ctx, u, token, fp, dial)
				w.mw.Synchronize(func() {
					f.testReceiver.SetEnabled(true)
					var fe *agent.FingerprintError
					if errors.As(err, &fe) {
						log.Printf("Проверка приёмника %s: сертификат изменился, отпечаток %s", u, fe.Got)
						if walk.MsgBox(w.mw, "Сертификат приёмника изменился",
							"Приёмник прислал другой сертификат.\n\nБыл:\n"+TwoLines(fp)+"\n\nСтал:\n"+TwoLines(fe.Got)+"\n\n"+
								"Так бывает после переустановки приёмника или удаления папки receiver-data. "+
								"Сверьте новый отпечаток со страницей приёмника (блок «Подключение агента»). "+
								"Если он не совпадает, не заменяйте: возможно, это чужой сервер.\n\nЗаменить отпечаток?",
							walk.MsgBoxYesNo|walk.MsgBoxIconWarning) != walk.DlgCmdYes {
							setResult(f.receiverResult, false, "Сертификат не подтверждён, отпечаток оставлен прежним.")
							return
						}
						f.fingerprint.SetText(fe.Got)
						w.testReceiver() // check the token with the new certificate
						return
					}
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
		"asiair":                      strings.TrimSpace(f.asiair.Text()),
		"username":                    strings.TrimSpace(f.user.Text()),
		"password":                    f.pass.Text(),
		"receiver_url":                u,
		"token":                       strings.TrimSpace(f.token.Text()),
		"receiver_fingerprint":        strings.TrimSpace(f.fingerprint.Text()),
		"connection":                  f.connection(),
		config.URLKey(f.connection()): u,
		"connection_fallback":         "",
		"tailscale_authkey":           strings.TrimSpace(f.tsKey.Text()),
	})
	if err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Настройки не сохранены: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	log.Printf("Настройки сохранены в %s", w.cfgPath)
	if f.connection() == config.ConnTailscale {
		w.offerFirewall()
	}
	w.tabs.SetCurrentIndex(0)
	w.restart()
}

// firewallRule is the name of the agent rule (the service display name).
const firewallRule = "ASIAIR Photo Agent"

// offerFirewall adds the agent to the Windows firewall before Tailscale
// opens its network port, so Windows does not ask by itself.
func (w *window) offerFirewall() {
	exe, err := update.Executable()
	if err != nil || !firewall.Supported() || firewall.Allowed(firewallRule, exe) {
		return
	}
	if walk.MsgBox(w.mw, "Брандмауэр Windows",
		"Для Tailscale агент открывает сетевой порт, и Windows может спросить, разрешить ли ему доступ к сети.\n\n"+
			"Добавить разрешение сейчас? Windows попросит права администратора.\n\n"+
			"«Нет» — ничего страшного: агент работает и без разрешения, Windows просто спросит сама.",
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	if err := autorun.RunElevated("-config", w.cfgPath, "firewall-allow"); err != nil {
		log.Printf("Брандмауэр: %v", err)
		return
	}
	// the elevated copy adds the rule; give it a moment before Tailscale starts
	for i := 0; i < 20 && !firewall.Allowed(firewallRule, exe); i++ {
		time.Sleep(500 * time.Millisecond)
	}
}

// applyAgentConfig shows in the form what the agent saved by itself, so a
// later «Сохранить» does not bring the old values back.
func (w *window) applyAgentConfig(values map[string]string) {
	f := &w.form
	for k, v := range values {
		switch k {
		case "asiair":
			f.asiair.SetText(v)
			setResult(f.asiairResult, true, "агент нашёл ASIAIR по новому адресу и сохранил путь")
		case "username":
			f.user.SetText(v)
		case "password":
			f.pass.SetText(v)
		case "receiver_fingerprint":
			f.fingerprint.SetText(v)
		case "tailscale_authkey": // a new Tailscale login sent by the receiver
			f.tsKey.SetText(v)
		}
	}
}

func (w *window) openConfig() {
	w.ensureConfig()
	if err := exec.Command("notepad.exe", w.cfgPath).Start(); err != nil {
		walk.MsgBox(w.mw, "ASIAIR Agent", "Не удалось открыть "+w.cfgPath+": "+err.Error(), walk.MsgBoxIconError)
	}
}
