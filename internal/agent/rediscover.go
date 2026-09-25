package agent

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

// How the agent looks for an ASIAIR that stopped answering at its address.
const (
	rediscoverAfter    = 3                // failed scans in a row (the first scan counts at once)
	rediscoverInterval = 10 * time.Minute // not more often than this
)

// source returns the current source of the images.
func (a *Agent) source() source.Source {
	a.srcMu.RLock()
	defer a.srcMu.RUnlock()
	return a.src
}

// saveConfig writes values to agent.yaml and tells the window.
func (a *Agent) saveConfig(values map[string]string) {
	if a.cfg.Path != "" {
		if err := config.UpdateAgentFile(a.cfg.Path, values); err != nil {
			log.Printf("Агент: не удалось сохранить %s: %v", a.cfg.Path, err)
		}
	}
	if f := a.OnConfigChanged; f != nil {
		f(values)
	}
}

// pinFingerprint remembers the receiver certificate seen on first contact
// when the settings had none.
func (a *Agent) pinFingerprint(fp string) {
	log.Printf("Агент: принят сертификат приёмника с отпечатком %s — сверьте его со страницей приёмника", fp)
	a.saveConfig(map[string]string{"receiver_fingerprint": fp})
}

// scanFailed decides whether to look for the ASIAIR in the network: at
// start at once, later after several failed scans in a row.
func (a *Agent) scanFailed(ctx context.Context) {
	a.mu.Lock()
	a.scanErrors++
	n, ok := a.scanErrors, a.everScanned
	link := a.status.ASIAIR
	a.mu.Unlock()
	l, err := source.ParseLink(link)
	if err != nil || l.Kind != "smb" {
		return // folders and USB sticks are not searched for
	}
	if ok && n < rediscoverAfter {
		return
	}
	if time.Since(a.lastDiscover) < rediscoverInterval || !a.discovering.CompareAndSwap(false, true) {
		return
	}
	a.lastDiscover = time.Now()
	a.setSearching(true)
	go func() {
		defer a.discovering.Store(false)
		defer a.setSearching(false)
		a.rediscover(ctx)
	}()
}

func (a *Agent) setSearching(on bool) {
	a.mu.Lock()
	a.status.Searching = on
	a.mu.Unlock()
}

// wantKind guesses from the settings what kind of device the link is for.
func wantKind(link, user string) string {
	if strings.EqualFold(user, source.StellaVitaUser) || strings.Contains(strings.ToLower(link), strings.ToLower(source.StellaVitaShare)) {
		return source.KindStellaVita
	}
	return source.KindASIAIR
}

// rediscover searches the local network for the device; when one answers
// with the configured shares, the link gets its new address and is saved.
// Otherwise the link stays as it was.
func (a *Agent) rediscover(ctx context.Context) {
	a.mu.Lock()
	link := a.status.ASIAIR
	a.mu.Unlock()
	l, err := source.ParseLink(link)
	if err != nil {
		return
	}
	kind := wantKind(link, a.cfg.Username)
	log.Printf("Агент: %s не отвечает по адресу %s — ищу его в сети…", kind, l.Host)
	dctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	devs, err := a.discover(dctx, nil)
	cancel()
	if err != nil && len(devs) == 0 {
		log.Printf("Агент: поиск %s в сети не удался: %v; путь оставлен прежним: %s", kind, err, link)
		return
	}
	type candidate struct {
		src        source.Source
		link       string
		user, pass string
		ip         string
	}
	var found []candidate
	for _, d := range devs {
		if d.Kind != kind {
			continue
		}
		newLink, err := source.ReplaceHost(link, d.IP)
		if err != nil || newLink == link {
			continue
		}
		user, pass := a.cfg.Username, a.cfg.Password
		if user == "" && d.Username != "" {
			user, pass = d.Username, d.Password
		}
		src, err := a.newSource(newLink, user, pass)
		if err != nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = src.Probe(pctx)
		cancel()
		if err != nil {
			src.Close()
			log.Printf("Агент: найден %s %s, но подключиться не удалось: %v", d.Kind, d.IP, err)
			continue
		}
		found = append(found, candidate{src, newLink, user, pass, d.IP})
	}
	pick := -1
	switch {
	case len(found) == 1:
		pick = 0
	case len(found) > 1:
		// several devices (other people's in a shared observatory): take
		// only the one that has images this agent already sent
		known := a.knownPaths(20)
		for i, c := range found {
			if hasAny(c.src, known) {
				if pick >= 0 {
					pick = -1 // ambiguous
					break
				}
				pick = i
			}
		}
		if pick < 0 {
			var ips []string
			for _, c := range found {
				ips = append(ips, c.ip)
			}
			log.Printf("Агент: в сети несколько %s (%s), какой из них ваш — не понять; путь оставлен прежним: %s. Укажите адрес в настройках.",
				kind, strings.Join(ips, ", "), link)
		}
	}
	for i, c := range found {
		if i != pick {
			c.src.Close()
		}
	}
	if pick >= 0 {
		c := found[pick]
		a.useSource(c.src, c.link, c.user, c.pass)
		return
	}
	if len(found) == 0 {
		log.Printf("Агент: %s в сети не найден; путь оставлен прежним: %s", kind, link)
	}
}

// knownPaths returns up to n files this agent has sent.
func (a *Agent) knownPaths(n int) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for p := range a.sent {
		if len(out) == n {
			break
		}
		out = append(out, p)
	}
	return out
}

// hasAny reports whether one of the files exists on the source.
func hasAny(src source.Source, paths []string) bool {
	for _, p := range paths {
		if f, err := src.Open(p); err == nil {
			f.Close()
			return true
		}
	}
	return false
}

// useSource switches to the found device and saves its link.
func (a *Agent) useSource(src source.Source, link, user, pass string) {
	a.srcMu.Lock()
	old := a.src
	a.src = src
	a.srcMu.Unlock()
	old.Close()
	values := map[string]string{"asiair": link}
	if user != a.cfg.Username {
		values["username"], values["password"] = user, pass
		a.cfg.Username, a.cfg.Password = user, pass
	}
	a.mu.Lock()
	a.status.ASIAIR = link
	a.scanErrors = 0
	a.mu.Unlock()
	a.cfg.ASIAIR = link
	log.Printf("Агент: ASIAIR найден по новому адресу, путь обновлён: %s", link)
	a.saveConfig(values)
	a.rescan.Store(true)
	a.wakeUp()
}

// defaults for the search hooks (tests replace them)
func (a *Agent) initDiscovery() {
	a.discover = source.Discover
	a.newSource = func(link, user, pass string) (source.Source, error) {
		return source.New(link, a.cfg.Shares, user, pass, a.cfg.Domain)
	}
}
