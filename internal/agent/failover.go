package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// The agent does not stay cut off when the way to the receiver in use
// stops working while the other works: Tailscale asks for a login (the key
// of the device expired, it was removed from the tailnet), or the internet
// address of the receiver is gone. It checks that the receiver answers the
// other way and switches to it as a switch asked on the receiver page does
// (saved, restarted, and back unless confirmed). A receiver that does not
// answer either way changes nothing. The reason is kept for the next run
// (<state file>.switched) to tell the receiver, and a Tailscale that asks
// for a login is marked (<Tailscale folder>.needlogin) until a new login
// works: the receiver page then offers to send one.

// timings of the failover (tests shorten them)
var (
	failoverAfter = 15 * time.Minute // the receiver not answering this long
	failoverLogin = 2 * time.Minute  // Tailscale asking for a login this long
	failoverRetry = 30 * time.Minute // after a try that did not work
	failoverEvery = 30 * time.Second
)

func (a *Agent) switchedNote() string { return a.cfg.StateFile + ".switched" }
func tsNeedLogin(dir string) string   { return dir + ".needlogin" }

// failoverLoop watches the connection and switches to the other way when
// the one in use does not work.
func (a *Agent) failoverLoop(ctx context.Context) {
	start := time.Now()
	var loginSince, nextTry time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(failoverEvery):
		}
		if why, to := a.failoverNeeded(start, &loginSince); why != "" && time.Now().After(nextTry) {
			if err := a.failover(ctx, to, why); err != nil {
				a.event("warn", "%s, но и через %s приёмник не отвечает: %v — связь остаётся через %s, следующая попытка через %v",
					why, connName(to), err, connName(a.cfg.Connection), failoverRetry)
				nextTry = time.Now().Add(failoverRetry)
			}
		}
	}
}

// failoverNeeded tells why the other way is to be tried ("" not now) and
// which one.
func (a *Agent) failoverNeeded(start time.Time, loginSince *time.Time) (why, to string) {
	c := a.cfg
	if a.Restart == nil || c.ConnectionFallback != "" || exists(tsConfirm(c)) || a.switching.Load() {
		return "", ""
	}
	to = protocol.ConnTailscale
	if c.UseTailscale() {
		to = protocol.ConnInternet
	}
	if !slices.Contains(a.modes(), to) {
		return "", ""
	}
	last, errText := a.Receiver()
	if errText == "" && !last.IsZero() {
		*loginSince = time.Time{}
		return "", "" // the receiver answers
	}
	if last.IsZero() {
		last = start
	}
	// asking for a login, with its link or not yet (passing this state at a
	// start with an auth key takes seconds, not failoverLogin)
	if st := a.Tailscale(); st != nil && st.State == tsnode.StateLogin {
		if loginSince.IsZero() {
			*loginSince = time.Now()
		}
		if time.Since(*loginSince) >= failoverLogin {
			return "Tailscale на агенте требует входа (ключ устройства истёк или оно удалено из сети Tailscale)", to
		}
	} else {
		*loginSince = time.Time{}
	}
	if time.Since(last) >= failoverAfter {
		return fmt.Sprintf("приёмник не отвечает через %s уже %d мин", connName(c.Connection), int(time.Since(last).Minutes())), to
	}
	return "", ""
}

// failover checks the other way and switches to it.
func (a *Agent) failover(ctx context.Context, to, why string) error {
	if !a.switching.CompareAndSwap(false, true) {
		return nil
	}
	defer a.switching.Store(false)
	a.event("warn", "%s — проверяю связь через %s", why, connName(to))
	note := why + " — связь переключена на " + connName(to)
	if strings.Contains(why, "требует входа") {
		os.WriteFile(tsNeedLogin(a.cfg.TailscaleDir), nil, 0o600)
		note += "; отправьте агенту новый вход Tailscale с приёмника («Управление» → «Вход Tailscale агента»)"
	}
	// told by the next run: the events of this one are lost with it
	os.WriteFile(a.switchedNote(), []byte(note), 0o600)
	err := a.switchConn(ctx, &protocol.ConnSwitch{Connection: to}, "автоматически")
	if err != nil {
		os.Remove(a.switchedNote())
	}
	return err
}

// failoverNoted tells the receiver why the previous run switched.
func (a *Agent) failoverNoted() {
	b, err := os.ReadFile(a.switchedNote())
	if err != nil {
		return
	}
	os.Remove(a.switchedNote())
	if t := strings.TrimSpace(string(b)); t != "" {
		a.event("warn", "%s", t)
	}
}
