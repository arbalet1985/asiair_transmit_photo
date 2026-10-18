package main

import (
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// A long job (looking at the frames, laying them out, downloading) shows
// on the button of the taskbar too, and the button flashes when it ends
// while the sorter is not the window in front.

var flashWindowEx = syscall.NewLazyDLL("user32.dll").NewProc("FlashWindowEx")

type flashWInfo struct {
	size    uint32
	hwnd    win.HWND
	flags   uint32
	count   uint32
	timeout uint32
}

// flashLonger: a job shorter than this ends without flashing.
const flashLonger = 10 * time.Second

// setProgress shows the progress (0–1000) in the window and on the taskbar.
func (w *window) setProgress(v int) {
	w.progress.SetValue(v)
	if pi := w.mw.ProgressIndicator(); pi != nil && w.busy {
		if v > 0 {
			pi.SetState(walk.PINormal)
			pi.SetTotal(1000)
			pi.SetCompleted(uint32(min(max(v, 0), 1000)))
		} else {
			pi.SetState(walk.PIIndeterminate)
		}
	}
}

// taskbarPaused shows the pause on the taskbar button (yellow).
func (w *window) taskbarPaused(p bool) {
	if pi := w.mw.ProgressIndicator(); pi != nil && w.busy {
		if p {
			pi.SetState(walk.PIPaused)
		} else {
			pi.SetState(walk.PINormal)
		}
	}
}

// taskbarBusy follows setBusy: the taskbar shows a job going on; when it
// ends after a while with the window not in front, its button flashes.
func (w *window) taskbarBusy(b bool) {
	pi := w.mw.ProgressIndicator()
	if b {
		w.busySince = time.Now()
		if pi != nil {
			pi.SetState(walk.PIIndeterminate)
		}
		return
	}
	if pi != nil {
		pi.SetState(walk.PINoProgress)
	}
	if !w.busySince.IsZero() && time.Since(w.busySince) > flashLonger && win.GetForegroundWindow() != w.mw.Handle() {
		const flashAll, untilFront = 3, 12 // FLASHW_ALL, FLASHW_TIMERNOFG
		fi := flashWInfo{hwnd: w.mw.Handle(), flags: flashAll | untilFront}
		fi.size = uint32(unsafe.Sizeof(fi))
		flashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	}
	w.busySince = time.Time{}
}
