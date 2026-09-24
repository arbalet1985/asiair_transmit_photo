package agent

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// maybeUpdate starts installing the agent version offered by the receiver.
func (a *Agent) maybeUpdate(ctx context.Context, offer *protocol.UpdateOffer) {
	if offer == nil || !update.Newer(offer.Version, protocol.Version) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case !bool(a.cfg.AutoUpdate):
		a.status.Update = fmt.Sprintf("доступна версия %s (автообновление выключено в agent.yaml)", offer.Version)
		return
	case a.Restart == nil:
		return
	case a.updating:
		return
	case time.Now().Before(a.updateRetry):
		return
	}
	a.updating = true
	go a.selfUpdate(ctx, offer)
}

// selfUpdate downloads the new agent from the receiver, replaces this
// program and restarts it.
func (a *Agent) selfUpdate(ctx context.Context, offer *protocol.UpdateOffer) {
	a.setUpdate("скачивается версия %s", offer.Version)
	err := a.installUpdate(ctx, offer)
	if err == nil {
		a.setUpdate("установлена версия %s, перезапуск…", offer.Version)
		err = a.Restart()
		if err == nil {
			return // a.updating stays set: the program is about to exit
		}
		err = fmt.Errorf("новая версия установлена, но перезапуск не удался: %w — перезапустите агент вручную", err)
	}
	a.mu.Lock()
	a.updating = false
	a.updateRetry = time.Now().Add(time.Hour)
	a.status.Update = "обновление до " + offer.Version + " не удалось: " + err.Error()
	a.mu.Unlock()
	log.Printf("Агент: обновление до %s не удалось: %v (повтор через час)", offer.Version, err)
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
