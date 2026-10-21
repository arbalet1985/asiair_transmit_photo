package gui

import (
	"context"
	"strings"
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
	// OnReady receives a function that exits the program and one that runs
	// a function on the window thread (for message boxes from goroutines).
	OnReady func(quit func(), ui func(func()))
	// Items are the commands of the menu between "Открыть страницу" and
	// "Выход".
	Items []TrayItem
}

// TrayItem is a command of the tray menu; one with no text and no Label is
// a separator.
type TrayItem struct {
	Text string
	// Label, if set, gives the current text; it is polled with the tooltip
	// and after every command.
	Label func() string
	// Do runs on the window thread, so it may show message boxes.
	Do func()
}

// Open opens a file, folder or address with its default program.
func Open(target string) { openURL(target) }

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
		// Windows keeps 127 characters: drop whole last lines first
		for len([]rune(tip)) > 120 && strings.Contains(tip, "\n") {
			tip = tip[:strings.LastIndex(tip, "\n")]
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
	type labelled struct {
		a     *walk.Action
		label func() string
	}
	var labels []labelled
	relabel := func() {
		for _, l := range labels {
			l.a.SetText(l.label())
		}
	}
	for _, it := range opt.Items {
		if it.Text == "" && it.Label == nil {
			ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
			continue
		}
		a := walk.NewAction()
		a.SetText(it.Text)
		do := it.Do
		a.Triggered().Attach(func() {
			if do != nil {
				do()
			}
			relabel()
		})
		if it.Label != nil {
			a.SetText(it.Label())
			labels = append(labels, labelled{a, it.Label})
		}
		ni.ContextMenu().Actions().Add(a)
	}
	ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
	add("Выход", func() {
		if walk.MsgBox(nil, opt.Title, "Остановить приём снимков и закрыть программу?",
			walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
			quit()
		}
	})
	if opt.OnReady != nil {
		opt.OnReady(quit, mw.Synchronize)
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
				mw.Synchronize(func() { setState(st, tip); relabel() })
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
