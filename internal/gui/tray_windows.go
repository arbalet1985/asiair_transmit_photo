package gui

import (
	"context"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// TrayOptions describe a program that runs in the background with only a
// tray icon (used by the receiver, whose interface is its web page).
type TrayOptions struct {
	Title string
	// Page is opened by a click on the icon and by "Открыть страницу".
	Page string
	// Run does the work until ctx is cancelled.
	Run func(ctx context.Context) error
	// Tooltip and State are polled every few seconds (State: StateIdle, ...).
	Tooltip func() string
	State   func() string
	// OnReady receives a function that exits the program.
	OnReady func(quit func())
}

// RunTray shows the tray icon and runs opt.Run until "Выход" is chosen or
// Run returns.
func RunTray(opt TrayOptions) error {
	var mw *walk.MainWindow
	if err := (MainWindow{AssignTo: &mw, Title: opt.Title, Visible: false}).Create(); err != nil {
		return err
	}
	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		return err
	}
	defer ni.Dispose()

	dpi := mw.DPI()
	icons := map[string]*walk.Icon{}
	for _, st := range []string{StateStopped, StateError, StateIdle, StateUploading} {
		if ic, err := walk.NewIconFromImageForDPI(IconImage(st, 16*dpi/96), dpi); err == nil {
			icons[st] = ic
		}
	}
	current := ""
	setState := func(st, tip string) {
		if st != current && icons[st] != nil {
			ni.SetIcon(icons[st])
			current = st
		}
		if r := []rune(tip); len(r) > 120 {
			tip = string(r[:120]) + "…"
		}
		ni.SetToolTip(tip)
	}
	setState(StateIdle, opt.Title)
	ni.SetVisible(true)

	openPage := func() { openURL(opt.Page) }
	ni.MouseDown().Attach(func(x, y int, b walk.MouseButton) {
		if b == walk.LeftButton {
			openPage()
		}
	})
	ni.MessageClicked().Attach(openPage)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	quitting := false
	quit := func() {
		mw.Synchronize(func() {
			if quitting {
				return
			}
			quitting = true
			cancel()
			ni.SetVisible(false)
			walk.App().Exit(0)
		})
	}
	add := func(text string, fn func()) {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(fn)
		ni.ContextMenu().Actions().Add(a)
	}
	add("Открыть страницу", openPage)
	ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
	add("Выход", func() {
		if walk.MsgBox(nil, opt.Title, "Остановить приём снимков и закрыть программу?",
			walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
			quit()
		}
	})
	if opt.OnReady != nil {
		opt.OnReady(quit)
	}

	go func() {
		err := opt.Run(ctx)
		done <- err
		if err != nil {
			mw.Synchronize(func() {
				walk.MsgBox(nil, opt.Title, "Приёмник остановлен: "+err.Error(), walk.MsgBoxIconError)
			})
		}
		quit()
	}()
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tip, st := opt.Title, StateIdle
				if opt.Tooltip != nil {
					tip = opt.Tooltip()
				}
				if opt.State != nil {
					st = opt.State()
				}
				mw.Synchronize(func() { setState(st, tip) })
			}
		}
	}()

	mw.Run()
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		return nil
	}
}
