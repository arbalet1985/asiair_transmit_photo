package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// The receiver page can restart the agent and switch its connection
// between the internet and Tailscale. A switch is tried before it is made
// (the receiver must answer the new way), and an agent that does not reach
// the receiver after the switch goes back to the old connection by itself.

// timings of the switch (tests shorten them)
var (
	switchConfirm   = 10 * time.Minute // the new connection must reach the receiver within
	switchTSWait    = 90 * time.Second // Tailscale must come up within
	switchCheckWait = 30 * time.Second // one try of a receiver address
)

// modes are the connections the agent is set up for.
func (a *Agent) modes() []string {
	c := a.cfg
	var m []string
	if c.Connection == config.ConnInternet || c.ReceiverURLInternet != "" {
		m = append(m, protocol.ConnInternet)
	}
	if c.Connection == config.ConnTailscale || c.TailscaleAuthKey != "" || tsnode.HasLogin(c.TailscaleDir) {
		m = append(m, protocol.ConnTailscale)
	}
	return m
}

// restartAsked restarts the program when the receiver page asks (once per
// value). Called from poll.
func (a *Agent) restartAsked(id int64) {
	if id == 0 || id == a.restartSeen {
		return
	}
	a.restartSeen = id
	if a.Restart == nil {
		a.event("warn", "приёмник просит перезапустить агент, но этот агент не умеет перезапускать себя")
		return
	}
	go func() {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("Агент: сбой перезапуска: %v", v)
			}
		}()
		a.eventWait(3*time.Second, "warn", "перезапуск агента по просьбе приёмника")
		if err := a.Restart(); err != nil {
			a.event("error", "перезапуск не удался: %v", err)
		}
	}()
}

// switchAsked starts a switch of the connection asked on the receiver page
// (once per ID). Called from poll.
func (a *Agent) switchAsked(ctx context.Context, sw *protocol.ConnSwitch) {
	if sw == nil || sw.ID == 0 || sw.ID == a.switchSeen {
		return
	}
	a.switchSeen = sw.ID
	done := func() {
		a.mu.Lock()
		a.status.SwitchDone = sw.ID
		a.mu.Unlock()
		a.kickSync()
	}
	switch {
	case sw.Connection != protocol.ConnInternet && sw.Connection != protocol.ConnTailscale:
		a.event("warn", "приёмник просит неизвестный способ связи %q", sw.Connection)
		done()
		return
	case sw.Connection == a.cfg.Connection:
		a.event("", "связь с приёмником уже идёт через %s", connName(sw.Connection))
		done()
		return
	case a.cfg.ConnectionFallback != "":
		a.event("warn", "прошлое переключение связи ещё не подтверждено — новое не выполняется")
		done()
		return
	case a.Restart == nil:
		a.event("warn", "приёмник просит переключить связь, но этот агент не умеет перезапускать себя")
		done()
		return
	case !a.switching.CompareAndSwap(false, true):
		return
	}
	go func() {
		defer a.switching.Store(false)
		defer func() {
			if v := recover(); v != nil {
				a.event("error", "сбой переключения связи: %v", v)
				done()
			}
		}()
		if err := a.switchConn(ctx, sw); err != nil {
			a.event("warn", "переключение связи на %s не удалось: %v — связь остаётся через %s",
				connName(sw.Connection), err, connName(a.cfg.Connection))
			done()
		}
	}()
}

// switchConn checks that the receiver answers the new way, saves it in
// agent.yaml and restarts the agent; an error leaves everything as it was.
func (a *Agent) switchConn(ctx context.Context, sw *protocol.ConnSwitch) error {
	c := a.cfg
	from, to := c.Connection, sw.Connection
	a.event("", "переключение связи с приёмником: %s → %s по просьбе приёмника, проверяю новый путь", connName(from), connName(to))
	var urls []string
	seen := map[string]bool{}
	for _, u := range append([]string{c.URLFor(to)}, sw.URLs...) {
		u = config.NormalizeReceiverURL(u)
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return fmt.Errorf("адрес приёмника для связи через %s неизвестен — задайте его в настройках агента", connName(to))
	}
	var dial DialFunc
	if to == config.ConnTailscale {
		node, err := tsnode.Start(tsnode.Config{Dir: c.TailscaleDir, Hostname: c.TailscaleHostname,
			AuthKey: c.TailscaleAuthKey, ControlURL: c.TailscaleControlURL, Logf: func(string, ...any) {}})
		if err != nil {
			return err
		}
		defer node.Close() // the restarted agent starts its own
		if err := waitTailscale(ctx, node); err != nil {
			return err
		}
		dial = node.Dial
	}
	var url string
	var errs []error
	for _, u := range urls {
		cctx, cancel := context.WithTimeout(ctx, switchCheckWait)
		err := CheckReceiver(cctx, u, c.Token, c.ReceiverFingerprint, dial)
		cancel()
		if err == nil {
			url = u
			break
		}
		errs = append(errs, fmt.Errorf("%s: %w", u, err))
	}
	if url == "" {
		return errors.Join(errs...)
	}
	values := map[string]string{
		"connection":          to,
		"receiver_url":        url,
		config.URLKey(to):     url,
		config.URLKey(from):   c.ReceiverURL,
		"connection_fallback": from,
	}
	if c.Path != "" {
		if err := config.UpdateAgentFile(c.Path, values); err != nil {
			return fmt.Errorf("не удалось сохранить %s: %w", c.Path, err)
		}
	}
	if f := a.OnConfigChanged; f != nil {
		f(values)
	}
	a.eventWait(3*time.Second, "warn", "приёмник отвечает через %s (%s): связь переключена, перезапуск агента", connName(to), url)
	if err := a.Restart(); err != nil {
		// still running the old way: put the settings back
		back := map[string]string{"connection": from, "receiver_url": c.ReceiverURL, "connection_fallback": ""}
		if c.Path != "" {
			config.UpdateAgentFile(c.Path, back)
		}
		return fmt.Errorf("перезапуск не удался: %w", err)
	}
	return nil
}

// waitTailscale waits until the node is in the tailnet.
func waitTailscale(ctx context.Context, n *tsnode.Node) error {
	deadline := time.Now().Add(switchTSWait)
	for {
		st := n.Status()
		switch st.State {
		case tsnode.StateRunning:
			return nil
		case tsnode.StateLogin:
			return errors.New("Tailscale на агенте требует входа — войдите в окне агента")
		case tsnode.StateError:
			return fmt.Errorf("Tailscale: %s", st.Error)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Tailscale: %s", st.Text())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// connLoop remembers the receiver address of the connection in use once
// the receiver answers, and after a switch from the receiver page confirms
// it or, when the receiver is not reached in time, goes back to the
// previous connection.
func (a *Agent) connLoop(ctx context.Context) {
	c := a.cfg
	fallback := c.ConnectionFallback
	deadline := time.Now().Add(switchConfirm)
	for !a.synced.Load() {
		if fallback != "" && time.Now().After(deadline) {
			a.switchBack(fallback)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	values := map[string]string{}
	stored := c.ReceiverURLInternet
	if c.UseTailscale() {
		stored = c.ReceiverURLTailscale
	}
	if stored != c.ReceiverURL {
		values[config.URLKey(c.Connection)] = c.ReceiverURL
	}
	if fallback != "" {
		values["connection_fallback"] = ""
		a.event("", "связь через %s работает — переключение подтверждено", connName(c.Connection))
		a.mu.Lock()
		a.switchBackTo = ""
		a.mu.Unlock()
		a.kickSync()
	}
	if len(values) > 0 && c.Path != "" {
		if err := config.UpdateAgentFile(c.Path, values); err != nil {
			log.Printf("Агент: не удалось сохранить %s: %v", c.Path, err)
		}
	}
}

// switchBack returns to the connection used before the switch.
func (a *Agent) switchBack(to string) {
	c := a.cfg
	url := c.URLFor(to)
	log.Printf("Агент: через %s приёмник не ответил за %v — возвращаю связь через %s (%s)",
		connName(c.Connection), switchConfirm, connName(to), url)
	values := map[string]string{"connection_fallback": ""}
	if url != "" {
		values["connection"], values["receiver_url"] = to, url
	}
	if c.Path != "" {
		if err := config.UpdateAgentFile(c.Path, values); err != nil {
			log.Printf("Агент: не удалось сохранить %s: %v", c.Path, err)
			return
		}
	}
	if f := a.OnConfigChanged; f != nil {
		f(values)
	}
	if url == "" || a.Restart == nil {
		return
	}
	a.event("warn", "связь через %s не заработала — возврат к связи через %s", connName(c.Connection), connName(to))
	if err := a.Restart(); err != nil {
		log.Printf("Агент: перезапуск не удался: %v", err)
	}
}

func connName(conn string) string {
	if conn == config.ConnTailscale {
		return "Tailscale"
	}
	return "интернет"
}
