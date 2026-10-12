package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/lxn/walk"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// The sorter updates itself from the receiver of the input: the receiver
// downloads the sorter of a new release along with the agent, and a sorter
// connected to it offers to install it and start again.

// sorterUpdate is a newer sorter the receiver of the input keeps.
type sorterUpdate struct {
	r *sorter.Receiver
	o protocol.UpdateOffer
}

// checkUpdate asks the receiver for a newer sorter (from a network
// goroutine); nil when there is none or the receiver does not say.
func checkUpdate(ctx context.Context, r *sorter.Receiver) *sorterUpdate {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	o, err := r.SorterUpdate(ctx)
	if err != nil || !sorter.NewSorter(o) {
		return nil
	}
	return &sorterUpdate{r: r, o: o}
}

// cleanupUpdate removes the old program an update left next to this one;
// the old copy may still be closing for a moment after the restart.
func cleanupUpdate() {
	exe, err := update.Executable()
	if err != nil {
		return
	}
	update.Cleanup(exe)
	time.AfterFunc(time.Minute, func() { update.Cleanup(exe) })
}

// offerUpdate asks, once a version, whether to update now.
func (w *window) offerUpdate(u *sorterUpdate) {
	if u == nil || w.busy || w.askedUpdate == u.o.Version {
		return
	}
	w.askedUpdate = u.o.Version
	q := fmt.Sprintf("На приёмнике есть новая версия сортировщика %s (у вас %s).\n\n"+
		"Обновить сейчас? Новая версия скачается с приёмника (%s), сортировщик закроется и запустится снова.",
		u.o.Version, protocol.Version, sorter.FormatBytes(u.o.Size))
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		w.setStatus(fmt.Sprintf("Доступна версия сортировщика %s — обновление предложится снова при следующем запуске.", u.o.Version))
		return
	}
	w.installUpdate(u)
}

// installUpdate downloads the new sorter from the receiver, puts it in
// place of this one (kept aside until the new one starts) and restarts.
func (w *window) installUpdate(u *sorterUpdate) {
	exe, err := update.Executable()
	if err != nil {
		w.warn("Обновление не удалось: " + err.Error())
		return
	}
	if w.measureCancel != nil {
		w.measureCancel()
		w.measureCancel = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	w.cancel = cancel
	w.setBusy(true)
	w.progress.SetValue(0)
	w.setStatus(fmt.Sprintf("Скачиваю сортировщик %s с приёмника…", u.o.Version))
	log.Printf("Обновление: скачиваю сортировщик %s с приёмника %s", u.o.Version, u.r.Base())
	shown := int64(-1)
	progress := func(done, total int64) {
		if total <= 0 {
			return
		}
		v := done * 1000 / total
		if v == shown {
			return
		}
		shown = v
		w.mw.Synchronize(func() {
			w.progress.SetValue(int(v))
			w.setStatus(fmt.Sprintf("Скачиваю сортировщик %s с приёмника… %d%%", u.o.Version, v/10))
		})
	}
	go func() {
		defer cancel()
		newFile := exe + ".new"
		err := u.r.DownloadSorter(ctx, u.o, newFile, progress)
		if err == nil {
			err = update.Install(exe, newFile)
		}
		if err != nil {
			os.Remove(newFile)
		}
		w.mw.Synchronize(func() {
			w.setBusy(false)
			w.progress.SetValue(0)
			if err != nil {
				if ctx.Err() == context.Canceled {
					w.setStatus("Обновление остановлено.")
					return
				}
				w.setStatus("Обновление не удалось: " + err.Error())
				w.warn("Обновление сортировщика не удалось: " + err.Error())
				return
			}
			log.Printf("Обновление: установлен сортировщик %s, перезапуск", u.o.Version)
			cmd := exec.Command(exe, os.Args[1:]...)
			if err := cmd.Start(); err != nil {
				w.setStatus("Новая версия установлена, но не запустилась — запустите сортировщик снова.")
				w.warn(fmt.Sprintf("Версия %s установлена, но не запустилась: %v. Закройте сортировщик и запустите его снова.", u.o.Version, err))
				return
			}
			w.mw.Close()
		})
	}()
}
