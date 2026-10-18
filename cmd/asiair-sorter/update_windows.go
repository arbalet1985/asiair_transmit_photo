package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
// connected to it offers to install it and start again. A sorter not
// connected to a receiver (a folder as the input, or the receiver not
// reached) looks for a new release on GitHub itself.

// sorterUpdate is a newer sorter: on the receiver of the input or on GitHub.
type sorterUpdate struct {
	version string
	size    int64
	from    string // "с приёмника", "с GitHub"
	where   string // for the journal
	get     func(ctx context.Context, dst string, progress func(done, total int64)) error
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
	return &sorterUpdate{version: o.Version, size: o.Size, from: "с приёмника", where: "приёмник " + r.Base(),
		get: func(ctx context.Context, dst string, progress func(done, total int64)) error {
			return r.DownloadSorter(ctx, o, dst, progress)
		}}
}

// checkGitHub looks for a newer sorter among the releases on GitHub (from a
// network goroutine); nil when there is none or GitHub is not reached.
func checkGitHub(ctx context.Context) *sorterUpdate {
	if !update.Valid(protocol.Version) {
		return nil // built by hand
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c := &update.Client{HTTP: &http.Client{Timeout: time.Minute}}
	rel, err := c.Latest(ctx)
	if err != nil {
		log.Printf("Обновление: GitHub: %v", err)
		return nil
	}
	a, ok := rel.Assets[update.SorterAsset]
	if !ok || !update.Newer(rel.Version, protocol.Version) {
		return nil
	}
	if builds, err := c.Builds(ctx, rel); err == nil && update.Unchanged(builds, update.SorterAsset, protocol.Build) {
		return nil // the same code under a new number
	}
	return &sorterUpdate{version: rel.Version, size: a.Size, from: "с GitHub", where: "GitHub " + rel.URL,
		get: func(ctx context.Context, dst string, progress func(done, total int64)) error {
			dl := &update.Client{Progress: progress}
			_, err := dl.Download(ctx, a, dst)
			return err
		}}
}

// gitHubEvery: how often a sorter without a receiver looks on GitHub.
const gitHubEvery = 6 * time.Hour

// watchGitHub looks for a new sorter on GitHub soon after the start and
// then every few hours, while the sorter is not connected to a receiver.
func (w *window) watchGitHub() {
	go func() {
		time.Sleep(time.Minute) // the start is not slowed down
		for {
			w.mw.Synchronize(func() { w.maybeCheckGitHub(false) })
			time.Sleep(gitHubEvery)
		}
	}()
}

// noReceiver: the input is a folder, or the receiver of the input was not
// reached by the last look at the frames.
func (w *window) noReceiver() bool {
	return !sorter.IsURL(w.inEdit.Text()) || w.receiverDown
}

// maybeCheckGitHub looks on GitHub when there is no receiver to ask (now:
// right after the receiver was not reached, else at most once an interval).
func (w *window) maybeCheckGitHub(now bool) {
	if !w.noReceiver() || w.checkingGitHub || !now && time.Since(w.gitHubChecked) < gitHubEvery-time.Minute {
		return
	}
	w.checkingGitHub, w.gitHubChecked = true, time.Now()
	go func() {
		u := checkGitHub(context.Background())
		w.mw.Synchronize(func() {
			w.checkingGitHub = false
			if u != nil && w.noReceiver() {
				w.offerUpdate(u)
			}
		})
	}()
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
	if u == nil || w.busy || w.askedUpdate == u.version {
		return
	}
	w.askedUpdate = u.version
	where := "На приёмнике есть"
	if u.from == "с GitHub" {
		where = "На GitHub вышла"
	}
	q := fmt.Sprintf("%s новая версия сортировщика %s (у вас %s).\n\n"+
		"Обновить сейчас? Новая версия скачается %s (%s), сортировщик закроется и запустится снова.",
		where, u.version, protocol.Version, u.from, sorter.FormatBytes(u.size))
	if walk.MsgBox(w.mw, "ASIAIR Sorter", q, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		w.setStatus(fmt.Sprintf("Доступна версия сортировщика %s — обновление предложится снова при следующем запуске.", u.version))
		return
	}
	w.installUpdate(u)
}

// installUpdate downloads the new sorter (from the receiver or GitHub), puts it in
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
	w.setProgress(0)
	w.setStatus(fmt.Sprintf("Скачиваю сортировщик %s %s…", u.version, u.from))
	log.Printf("Обновление: скачиваю сортировщик %s, %s", u.version, u.where)
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
			w.setProgress(int(v))
			w.setStatus(fmt.Sprintf("Скачиваю сортировщик %s %s… %d%%", u.version, u.from, v/10))
		})
	}
	go func() {
		defer cancel()
		newFile := exe + ".new"
		err := u.get(ctx, newFile, progress)
		if err == nil {
			err = update.Install(exe, newFile)
		}
		if err != nil {
			os.Remove(newFile)
		}
		w.mw.Synchronize(func() {
			w.setBusy(false)
			w.setProgress(0)
			if err != nil {
				if ctx.Err() == context.Canceled {
					w.setStatus("Обновление остановлено.")
					return
				}
				w.setStatus("Обновление не удалось: " + err.Error())
				w.warn("Обновление сортировщика не удалось: " + err.Error())
				return
			}
			log.Printf("Обновление: установлен сортировщик %s, перезапуск", u.version)
			cmd := exec.Command(exe, os.Args[1:]...)
			if err := cmd.Start(); err != nil {
				w.setStatus("Новая версия установлена, но не запустилась — запустите сортировщик снова.")
				w.warn(fmt.Sprintf("Версия %s установлена, но не запустилась: %v. Закройте сортировщик и запустите его снова.", u.version, err))
				return
			}
			w.mw.Close()
		})
	}()
}
