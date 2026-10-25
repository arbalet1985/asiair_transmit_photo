package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// New Tailscale credentials from the receiver page: an auth key (with the
// coordination server and the name). The agent logs in with them in a
// folder of its own next to the one in use (<dir>.new) and checks that the
// receiver answers through that login; only then it writes them to
// agent.yaml and restarts. The folders are swapped at the start of the next
// run, while no node holds them (<dir>.swap.json: the settings to go back
// to). Through Tailscale the new login must reach the receiver within
// switchConfirm, else the agent goes back to the old one (<dir>.old,
// <dir>.restore) by itself.

// tsSaved are the settings of agent.yaml a new login changes.
type tsSaved struct {
	AuthKey     string `json:"auth_key"`
	ControlURL  string `json:"control_url"`
	Hostname    string `json:"hostname"`
	URL         string `json:"url"`          // receiver_url_tailscale
	ReceiverURL string `json:"receiver_url"` // receiver_url
}

func (s tsSaved) values() map[string]string {
	return map[string]string{"tailscale_authkey": s.AuthKey, "tailscale_control_url": s.ControlURL,
		"tailscale_hostname": s.Hostname, config.URLKey(config.ConnTailscale): s.URL, "receiver_url": s.ReceiverURL}
}

func tsSavedOf(c *config.Agent) tsSaved {
	return tsSaved{c.TailscaleAuthKey, c.TailscaleControlURL, c.TailscaleHostname, c.ReceiverURLTailscale, c.ReceiverURL}
}

// the files of a new login next to the Tailscale folder
func tsNew(c *config.Agent) string     { return c.TailscaleDir + ".new" }
func tsOld(c *config.Agent) string     { return c.TailscaleDir + ".old" }
func tsSwap(c *config.Agent) string    { return c.TailscaleDir + ".swap.json" }
func tsConfirm(c *config.Agent) string { return c.TailscaleDir + ".confirm.json" }
func tsRestore(c *config.Agent) string { return c.TailscaleDir + ".restore" }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// tsLoginAsked takes the credentials asked on the receiver page (once per
// ID). Called from poll.
func (a *Agent) tsLoginAsked(ctx context.Context, l *protocol.TSLogin) {
	if l == nil || l.ID == 0 || l.ID == a.tsLoginSeen {
		return
	}
	a.tsLoginSeen = l.ID
	done := func() {
		a.mu.Lock()
		a.status.TSLoginDone = l.ID
		a.mu.Unlock()
		a.kickSync()
	}
	switch {
	case a.Restart == nil:
		a.event("warn", "приёмник прислал новый вход Tailscale, но этот агент не умеет перезапускать себя")
		done()
		return
	case exists(tsConfirm(a.cfg)) || a.cfg.ConnectionFallback != "":
		a.event("warn", "прошлое изменение связи ещё не подтверждено — новый вход Tailscale не выполняется")
		done()
		return
	case !a.switching.CompareAndSwap(false, true):
		a.event("warn", "агент уже меняет связь — новый вход Tailscale не выполняется")
		done()
		return
	}
	go func() {
		defer a.switching.Store(false)
		defer func() {
			if v := recover(); v != nil {
				a.event("error", "сбой нового входа Tailscale: %v", v)
				done()
			}
		}()
		if err := a.tsLogin(ctx, l); err != nil {
			a.event("warn", "новый вход Tailscale не выполнен: %v — остаётся прежний", err)
			done()
		}
	}()
}

// tsLogin logs in aside with the new credentials, checks the receiver
// through that login, saves them and restarts; an error leaves everything
// as it was.
func (a *Agent) tsLogin(ctx context.Context, l *protocol.TSLogin) error {
	c := a.cfg
	if c.TailscaleDir == "" {
		return errors.New("папка Tailscale агента не задана")
	}
	host := l.Hostname
	if host == "" {
		host = c.TailscaleHostname
	}
	a.event("", "приёмник прислал новый вход Tailscale (имя %s): вхожу с ним отдельно и проверяю связь с приёмником", host)
	dir := tsNew(c)
	os.RemoveAll(dir)
	node, err := tsnode.Start(tsnode.Config{Dir: dir, Hostname: host, AuthKey: l.AuthKey, ControlURL: l.ControlURL, Logf: func(string, ...any) {}})
	if err != nil {
		return err
	}
	closed := false
	closeNode := func() {
		if !closed {
			closed = true
			node.Close()
		}
	}
	defer func() {
		closeNode()
		if exists(dir) && !exists(tsSwap(c)) {
			os.RemoveAll(dir) // not taken
		}
	}()
	if err := waitTailscale(ctx, node); err != nil {
		return fmt.Errorf("с этим ключом вход в Tailscale не выполнен (%v) — проверьте ключ (tskey-auth-…): не истёк ли он, не использован ли уже, тот ли сервер управления", err)
	}
	var urls []string
	seen := map[string]bool{}
	for _, u := range append([]string{c.ReceiverURLTailscale}, l.ReceiverURLs...) {
		if u = config.NormalizeReceiverURL(u); u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return errors.New("адрес приёмника в Tailscale неизвестен")
	}
	url := ""
	var errs []error
	for _, u := range urls {
		cctx, cancel := context.WithTimeout(ctx, switchCheckWait)
		err := CheckReceiver(cctx, u, c.Token, c.ReceiverFingerprint, node.Dial)
		cancel()
		if err == nil {
			url = u
			break
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
	}
	if url == "" {
		return fmt.Errorf("приёмник через новый вход не отвечает: %w", errors.Join(errs...))
	}
	closeNode()
	// the settings now, and the ones to go back to
	old := tsSavedOf(c)
	nw := tsSaved{AuthKey: l.AuthKey, ControlURL: l.ControlURL, Hostname: host, URL: url, ReceiverURL: c.ReceiverURL}
	if c.UseTailscale() {
		nw.ReceiverURL = url
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(tsSwap(c), b, 0o600); err != nil {
		return fmt.Errorf("не удалось сохранить: %w", err)
	}
	if c.Path != "" {
		if err := config.UpdateAgentFile(c.Path, nw.values()); err != nil {
			os.Remove(tsSwap(c))
			return fmt.Errorf("не удалось сохранить %s: %w", c.Path, err)
		}
	}
	if f := a.OnConfigChanged; f != nil {
		f(nw.values())
	}
	a.eventWait(3*time.Second, "warn", "новый вход Tailscale проверен: приёмник отвечает через него (%s) — перезапуск агента", url)
	if err := a.Restart(); err != nil {
		os.Remove(tsSwap(c))
		if c.Path != "" {
			config.UpdateAgentFile(c.Path, old.values())
		}
		return fmt.Errorf("перезапуск не удался: %w", err)
	}
	return nil
}

// tsStartup swaps the Tailscale folders at the start of a run, before the
// node is started: a new login taken (<dir>.swap.json) or a return to the
// old one (<dir>.restore). c is changed to the settings in effect.
func tsStartup(c *config.Agent) {
	if c.TailscaleDir == "" {
		return
	}
	dir, old := c.TailscaleDir, tsOld(c)
	if exists(tsRestore(c)) {
		os.Remove(tsRestore(c))
		if exists(old) {
			bad := dir + ".bad"
			os.RemoveAll(bad)
			if err := os.Rename(dir, bad); err != nil && exists(dir) {
				log.Printf("Агент: прежний вход Tailscale не возвращён: %v", err)
				return
			}
			if err := os.Rename(old, dir); err != nil {
				log.Printf("Агент: прежний вход Tailscale не возвращён: %v", err)
				os.Rename(bad, dir)
				return
			}
			os.RemoveAll(bad)
			log.Printf("Агент: возвращён прежний вход Tailscale")
		}
		return
	}
	b, err := os.ReadFile(tsSwap(c))
	if err != nil {
		return
	}
	var back tsSaved
	json.Unmarshal(b, &back)
	undo := func(why error) {
		log.Printf("Агент: новый вход Tailscale не взят: %v — остаётся прежний", why)
		if c.Path != "" {
			config.UpdateAgentFile(c.Path, back.values())
		}
		c.TailscaleAuthKey, c.TailscaleControlURL, c.TailscaleHostname, c.ReceiverURLTailscale = back.AuthKey, back.ControlURL, back.Hostname, back.URL
		if back.ReceiverURL != "" {
			c.ReceiverURL = back.ReceiverURL
		}
		os.Remove(tsSwap(c))
		os.RemoveAll(tsNew(c))
	}
	if !exists(tsNew(c)) {
		undo(errors.New("папки нового входа нет"))
		return
	}
	os.RemoveAll(old)
	if exists(dir) {
		if err := os.Rename(dir, old); err != nil {
			undo(err)
			return
		}
	}
	if err := os.Rename(tsNew(c), dir); err != nil {
		os.Rename(old, dir)
		undo(err)
		return
	}
	// kept until the receiver answers through the new login
	if err := os.Rename(tsSwap(c), tsConfirm(c)); err != nil {
		os.Remove(tsSwap(c))
	}
	log.Printf("Агент: взят новый вход Tailscale (имя %s)", c.TailscaleHostname)
}

// tsConfirmed: the receiver answered — the new login stays, the old goes.
func (a *Agent) tsConfirmed() {
	c := a.cfg
	if !exists(tsConfirm(c)) {
		return
	}
	os.Remove(tsConfirm(c))
	os.RemoveAll(tsOld(c))
	os.Remove(tsNeedLogin(c.TailscaleDir))
	a.mu.Lock()
	a.status.TSLoginBack = false
	a.mu.Unlock()
	a.event("", "новый вход Tailscale работает — подтверждён")
	a.kickSync()
}

// tsGoBack: the receiver did not answer through the new login in time —
// the old settings back in agent.yaml, the old folder back at the next run.
func (a *Agent) tsGoBack() {
	c := a.cfg
	b, err := os.ReadFile(tsConfirm(c))
	if err != nil {
		return
	}
	var back tsSaved
	json.Unmarshal(b, &back)
	log.Printf("Агент: через новый вход Tailscale приёмник не ответил за %v — возвращаю прежний", switchConfirm)
	if !exists(tsOld(c)) {
		os.Remove(tsConfirm(c)) // nothing to go back to
		return
	}
	if c.Path != "" {
		if err := config.UpdateAgentFile(c.Path, back.values()); err != nil {
			log.Printf("Агент: не удалось сохранить %s: %v", c.Path, err)
			return
		}
	}
	if f := a.OnConfigChanged; f != nil {
		f(back.values())
	}
	if err := os.WriteFile(tsRestore(c), nil, 0o600); err != nil {
		log.Printf("Агент: %v", err)
		return
	}
	os.Remove(tsConfirm(c))
	if a.Restart == nil {
		return
	}
	a.event("warn", "через новый вход Tailscale связи нет — возврат к прежнему входу")
	if err := a.Restart(); err != nil {
		log.Printf("Агент: перезапуск не удался: %v", err)
	}
}
