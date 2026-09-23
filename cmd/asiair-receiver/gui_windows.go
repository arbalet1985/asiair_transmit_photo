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
	if p != nil && p.cfg.LogFile != "" {
		logFile = p.cfg.LogFile
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
			st := p.srv.AgentSummary()
			return fmt.Sprintf("ASIAIR Receiver — %s", st)
		},
		State: func() string {
			if p.srv.AgentOnline() {
				return gui.StateIdle
			}
			return gui.StateError
		},
		OnReady: func(quit func()) { p.control.quit = quit },
	})
}
