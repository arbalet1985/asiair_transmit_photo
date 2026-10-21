package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/autorun"
	"github.com/arbalet1985/asiair_transmit_photo/internal/gui"
	"github.com/arbalet1985/asiair_transmit_photo/internal/receiver"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// The .syso resource files next to this file carry the application icon and
// the manifest the tray icon needs (common controls v6, DPI awareness).
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ../../internal/gui/icon.png --manifest gui --product-name "ASIAIR Receiver" --file-description "ASIAIR Receiver"

// guiMain runs the receiver with a tray icon instead of a console window
// (double-click, or start at logon).
func guiMain(cfgPath string) error {
	gui.DetachConsole()
	fromAutostart := len(os.Args) > 0 && os.Args[len(os.Args)-1] == autostartArg

	release, ok := svc.SingleInstance(app.Name)
	if !ok {
		// already running (as a service or another tray icon): show its page,
		// unless this is the automatic start at logon
		if !fromAutostart {
			if p, err := prepare(cfgPath, receiver.ModeTray); err == nil {
				svc.OpenBrowser(p.page)
			}
		}
		return nil
	}
	defer release()

	p, err := prepare(cfgPath, receiver.ModeTray)
	logFile := svc.DefaultLogFile(app.Name)
	if p != nil {
		if p.cfg.LogFile != "" {
			logFile = p.cfg.LogFile
		}
		svc.SetLogRetention(p.cfg.LogKeep.D(), p.cfg.LogCompressAfter.D())
	}
	if f, lerr := svc.OpenLog(logFile); lerr == nil {
		log.SetOutput(io.MultiWriter(f))
	}
	log.SetFlags(log.Ldate | log.Ltime)
	if err != nil {
		log.Printf("Приёмник не запущен: %v", err)
		walk.MsgBox(nil, "ASIAIR Receiver", "Приёмник не запущен: "+err.Error(), walk.MsgBoxIconError)
		return err
	}
	// keep the autostart entry pointing at this program and config
	if cmd, err := autorun.Command(cfgPath, "gui "+autostartArg); err == nil {
		autorun.Refresh(runName, cmd)
	}

	var ui func(func()) // runs on the window thread, once the tray is ready
	return gui.RunTray(gui.TrayOptions{
		Title: "ASIAIR Receiver",
		Page:  p.page,
		Run: func(ctx context.Context) error {
			if p.created {
				go func() {
					time.Sleep(time.Second)
					svc.OpenBrowser(p.page)
				}()
			}
			return p.run(ctx)
		},
		Tooltip: func() string {
			return "ASIAIR Receiver\n" + p.srv.TrayInfo()
		},
		State: func() string {
			if p.srv.AgentOnline() {
				return gui.StateIdle
			}
			return gui.StateError
		},
		Items: []gui.TrayItem{
			{
				Label: func() string {
					if p.srv.TransferPaused() {
						return "Продолжить передачу"
					}
					return "Пауза передачи"
				},
				Do: func() {
					if err := p.srv.SetTransferPaused(!p.srv.TransferPaused()); err != nil {
						walk.MsgBox(nil, "ASIAIR Receiver", "Ошибка: "+err.Error(), walk.MsgBoxIconError)
					}
				},
			},
			{Text: "Открыть папку со снимками", Do: func() {
				if err := p.srv.OpenDestDir(); err != nil {
					walk.MsgBox(nil, "ASIAIR Receiver", "Не удалось открыть папку: "+err.Error(), walk.MsgBoxIconError)
				}
			}},
			{Text: "Открыть журнал", Do: func() { gui.Open(logFile) }},
			{Text: "Проверить обновления", Do: func() { checkUpdates(p, ui) }},
			{},
			{Text: "Перезапустить приёмник", Do: func() {
				if walk.MsgBox(nil, "ASIAIR Receiver", "Перезапустить приёмник? Передача прервётся на несколько секунд и продолжится с того же места.",
					walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
					return
				}
				log.Printf("Приёмник: перезапуск из значка в трее")
				if err := svc.Restart(); err != nil {
					walk.MsgBox(nil, "ASIAIR Receiver", "Не удалось перезапустить: "+err.Error(), walk.MsgBoxIconError)
				}
			}},
		},
		OnReady: func(quit func(), sync func(func())) {
			ui = sync
			p.control.quit = quit
			svc.SetQuit(quit) // closes the tray when a new version restarts
		},
	})
}

// checking: a check from the tray is going on (the window thread only)
var checking bool

// checkUpdates asks GitHub for a new release now and tells what it found;
// a newer receiver is installed when the user agrees.
func checkUpdates(p *prepared, ui func(func())) {
	const title = "ASIAIR Receiver"
	if checking || ui == nil {
		return
	}
	checking = true
	asked := time.Now()
	p.srv.CheckUpdates(false)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Обновление: сбой проверки из трея: %v", r)
			}
		}()
		// the check is a request to GitHub; the agent and the sorter it
		// downloads may take longer, the answer does not wait for them
		deadline := time.Now().Add(2 * time.Minute)
		st := p.srv.UpdateState()
		for st.Answered.Before(asked) && time.Now().Before(deadline) {
			time.Sleep(time.Second)
			st = p.srv.UpdateState()
		}
		late := st.Answered.Before(asked)
		ui(func() {
			checking = false
			switch {
			case late:
				walk.MsgBox(nil, title, "GitHub пока не ответил — проверка продолжается, её итог будет на странице приёмника.", walk.MsgBoxIconInformation)
			case st.Error != "":
				walk.MsgBox(nil, title, "Не удалось проверить обновления: "+st.Error, walk.MsgBoxIconWarning)
			case st.Newer && st.CanInstall && st.Auto:
				// the check installs it itself (or tells why not)
				msg := fmt.Sprintf("Вышла версия %s (установлена %s) — включены автоматические обновления, приёмник обновится и перезапустится сам.", st.Latest, st.Version)
				if st.Status != "" {
					msg += "\n\nСейчас: " + st.Status + "."
				}
				walk.MsgBox(nil, title, msg, walk.MsgBoxIconInformation)
			case st.Newer && st.CanInstall:
				if walk.MsgBox(nil, title, fmt.Sprintf("Вышла версия %s (установлена %s).\n\nУстановить сейчас? Приёмник перезапустится, передача прервётся на несколько секунд и продолжится с того же места.", st.Latest, st.Version),
					walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
					log.Printf("Приёмник: обновление до %s из значка в трее", st.Latest)
					p.srv.CheckUpdates(true)
				}
			case st.Newer:
				walk.MsgBox(nil, title, fmt.Sprintf("Вышла версия %s (установлена %s). Сам приёмник обновиться не может — установите его со страницы релиза (ссылка на странице приёмника, раздел «Обновления»).", st.Latest, st.Version), walk.MsgBoxIconInformation)
			case st.Unchanged:
				walk.MsgBox(nil, title, fmt.Sprintf("Вышла версия %s, но в приёмнике изменений нет — обновлять его не нужно. Агент и сортировщик получат её через приёмник.", st.Latest), walk.MsgBoxIconInformation)
			default:
				walk.MsgBox(nil, title, fmt.Sprintf("Установлена последняя версия: %s.", st.Version), walk.MsgBoxIconInformation)
			}
		})
	}()
}
