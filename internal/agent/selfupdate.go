package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// maybeUpdate starts installing the agent version offered by the receiver,
// or, when the user confirms updates (ConfirmUpdate), keeps the offer until
// InstallUpdate is called.
func (a *Agent) maybeUpdate(ctx context.Context, offer *protocol.UpdateOffer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if offer != nil && offer.Build != "" && offer.Build == protocol.Build {
		offer = nil // a new number, the same code: nothing to install
	}
	if offer == nil || !update.Newer(offer.Version, protocol.Version) {
		if a.offer != nil && !a.updating {
			if a.status.Update == availableText(a.offer.Version) {
				a.status.Update = ""
			}
			a.offer = nil
		}
		return
	}
	a.offer = offer
	switch {
	case a.rolledBack != nil && a.rolledBack.Version == offer.Version:
		// this version did not work here: wait for the next one
		a.status.Update = rollbackText(a.rolledBack)
		return
	case a.updating:
		return
	case a.ConfirmUpdate:
		if a.failed != offer.Version { // keep the error until the next try
			a.status.Update = availableText(offer.Version)
		}
		return
	case !bool(a.cfg.AutoUpdate):
		a.status.Update = fmt.Sprintf("доступна версия %s (автообновление выключено в agent.yaml)", offer.Version)
		return
	case a.Restart == nil:
		return
	case time.Now().Before(a.updateRetry):
		return
	}
	a.updating = true
	go a.selfUpdate(ctx, offer)
}

// SetAutoUpdate switches between installing new versions at once (on) and
// waiting for InstallUpdate (off), while the agent runs: the agent window's
// «Устанавливать обновления автоматически».
func (a *Agent) SetAutoUpdate(on bool) {
	a.mu.Lock()
	a.ConfirmUpdate = !on
	if on {
		a.cfg.AutoUpdate = true
	}
	a.mu.Unlock()
	a.kickSync() // the offer is looked at again with the next answer
}

func availableText(version string) string {
	return fmt.Sprintf("доступна версия %s — нажмите «Обновить» в окне агента", version)
}

// UpdateAvailable returns the new version the user can install now with
// InstallUpdate, or "".
func (a *Agent) UpdateAvailable() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.offer == nil || a.updating || a.Restart == nil ||
		a.rolledBack != nil && a.rolledBack.Version == a.offer.Version {
		return ""
	}
	return a.offer.Version
}

// UpdateBuild is the build of the version offered ("" when unknown): a new
// number over the same build is not news.
func (a *Agent) UpdateBuild() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.offer == nil {
		return ""
	}
	return a.offer.Build
}

// InstallUpdate installs the offered new version and restarts the agent;
// the user asked for it (the "Обновить" button).
func (a *Agent) InstallUpdate(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.updating:
		return nil
	case a.offer == nil:
		return errors.New("новой версии нет")
	case a.Restart == nil:
		return errors.New("этот агент не может обновить себя")
	}
	a.updating = true
	a.failed = ""
	go a.selfUpdate(ctx, a.offer)
	return nil
}

// selfUpdate downloads the new agent from the receiver, replaces this
// program and restarts it.
func (a *Agent) selfUpdate(ctx context.Context, offer *protocol.UpdateOffer) {
	a.event("", "обновление до версии %s: скачивание с приёмника", offer.Version)
	a.setUpdate("скачивается версия %s", offer.Version)
	err := a.installUpdate(ctx, offer)
	if err == nil {
		a.setUpdate("установлена версия %s, перезапуск…", offer.Version)
		// the receiver learns it before the agent goes away (and before the
		// watchdog's deadline starts)
		a.eventWait(3*time.Second, "warn", "версия %s установлена, перезапуск агента", offer.Version)
		a.startWatch(offer.Version)
		err = a.Restart()
		if err == nil {
			return // a.updating stays set: the program is about to exit
		}
		if exe, xerr := a.exe(); xerr == nil {
			if uerr := update.Undo(exe); uerr == nil {
				err = fmt.Errorf("новая версия не запустилась (%w), оставлена прежняя", err)
			} else {
				err = fmt.Errorf("новая версия установлена, но перезапуск не удался: %w — перезапустите агент вручную", err)
			}
		}
	}
	a.mu.Lock()
	a.updating = false
	a.updateRetry = time.Now().Add(time.Hour)
	a.failed = offer.Version
	a.status.Update = "обновление до " + offer.Version + " не удалось: " + err.Error()
	retry := "повтор через час"
	if a.ConfirmUpdate {
		a.status.Update += " — можно повторить кнопкой «Обновить»"
		retry = "можно повторить кнопкой «Обновить»"
	}
	a.mu.Unlock()
	a.event("error", "обновление до %s не удалось: %v (%s)", offer.Version, err, retry)
}

func (a *Agent) setUpdate(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.mu.Lock()
	a.status.Update = msg
	a.mu.Unlock()
	log.Printf("Агент: обновление: %s", msg)
}

func (a *Agent) installUpdate(ctx context.Context, offer *protocol.UpdateOffer) error {
	exe := a.Exe
	if exe == "" {
		var err error
		if exe, err = update.Executable(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	resp, err := a.cl.do(ctx, http.MethodGet, "/api/update/agent", nil, nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := httpErr(resp); err != nil {
		return err
	}
	if _, err := update.Save(resp.Body, exe+".new", offer.Size, offer.SHA256); err != nil {
		return err
	}
	if err := update.Install(exe, exe+".new"); err != nil {
		os.Remove(exe + ".new")
		return err
	}
	return nil
}

// Deadlines for the new version to report to the receiver, after which the
// watchdog puts the old version back. A service is started again by the
// service manager only after 30 s.
var (
	healthDeadline        = 3 * time.Minute
	healthDeadlineService = 6 * time.Minute
)

// startWatch starts the watchdog that rolls the update back if the new
// version does not work. Without it the update still goes on.
func (a *Agent) startWatch(version string) {
	exe, err := a.exe()
	if err != nil {
		return
	}
	d := healthDeadline
	if svc.IsService() {
		d = healthDeadlineService
	}
	if v, err := time.ParseDuration(os.Getenv("ASIAIR_UPDATE_TIMEOUT")); err == nil && v > 0 {
		d = v // tests
	}
	err = update.StartWatch(exe, update.Pending{
		From: protocol.Version, To: version, Deadline: time.Now().Add(d),
		Service: svc.IsService(), Args: os.Args[1:],
	})
	if err != nil {
		log.Printf("Агент: обновление: страховка (откат) не запущена: %v", err)
	}
}

func (a *Agent) exe() (string, error) {
	if a.Exe != "" {
		return a.Exe, nil
	}
	return update.Executable()
}

// startupUpdate runs when the agent starts: after an update it reports to
// the watchdog; otherwise it removes what earlier updates left behind.
func (a *Agent) startupUpdate() {
	exe, err := a.exe()
	if err != nil {
		return
	}
	if rb := update.ReadRollback(exe); rb != nil && rb.From == protocol.Version {
		a.mu.Lock()
		a.rolledBack = rb
		a.status.Update = rollbackText(rb)
		a.mu.Unlock()
		a.event("error", "%s", rollbackText(rb))
	}
	if update.Started(exe, protocol.Version) {
		a.checkUpdate = true
		log.Printf("Агент: новая версия %s запущена, проверка связи с приёмником…", protocol.Version)
		return
	}
	a.cleanupLater(exe)
}

// updateWorks is called after a successful exchange with the receiver.
func (a *Agent) updateWorks() {
	if !a.checkUpdate {
		return
	}
	a.checkUpdate = false
	exe, err := a.exe()
	if err != nil {
		return
	}
	update.Healthy(exe, protocol.Version)
	a.event("", "версия %s работает, связь с приёмником есть", protocol.Version)
	a.cleanupLater(exe)
}

// cleanupLater removes the old copy; the watchdog may still run from it for
// a moment, so it is tried a few times.
func (a *Agent) cleanupLater(exe string) {
	update.Cleanup(exe)
	for _, d := range []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute} {
		time.AfterFunc(d, func() {
			if update.ReadPending(exe) == nil {
				update.Cleanup(exe)
			}
		})
	}
}

func rollbackText(rb *update.Rollback) string {
	return fmt.Sprintf("обновление до %s не удалось (%s), %s возвращена версия %s; эта версия больше не ставится, ждём следующую",
		rb.Version, rb.Reason, rb.Time.Local().Format("02.01 15:04"), rb.From)
}
