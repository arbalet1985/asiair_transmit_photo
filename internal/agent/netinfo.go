package agent

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// netInfoLoop watches the network of the computer: which Wi-Fi network it
// uses (shown on the receiver page; a change is an event: a laptop that
// jumped to another network explains a lost ASIAIR) and its addresses. When
// the network changes, the connections made over the old one are dropped at
// once (networkChanged) instead of hanging until their timeouts.
func (a *Agent) netInfoLoop(ctx context.Context) {
	first := true
	addrs := localAddrs()
	var lastWiFi time.Time
	for ctx.Err() == nil {
		changed := false
		if now := localAddrs(); now != addrs {
			if !first {
				a.event("warn", "сеть компьютера изменилась (адреса: %s)", now)
				changed = true
			}
			addrs = now
		}
		if first || changed || time.Since(lastWiFi) >= 30*time.Second {
			lastWiFi = time.Now()
			qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			w := currentWiFi(qctx)
			cancel()
			old := a.wifi.Load()
			a.wifi.Store(w)
			switch {
			case ctx.Err() != nil:
			case w != nil && (old == nil || old.SSID != w.SSID):
				if first {
					a.event("", "Wi-Fi: %s", w.SSID)
				} else {
					a.event("warn", "Wi-Fi: подключено к сети %s", w.SSID)
					changed = true
				}
			case w == nil && old != nil:
				a.event("warn", "Wi-Fi: нет подключения к сети %s", old.SSID)
				changed = true
			}
		}
		if changed {
			a.networkChanged()
		}
		first = false
		sleep(ctx, 10*time.Second)
	}
}

// localAddrs lists the IPv4 addresses of the computer (not loopback), as
// text. IPv6 is left out: its temporary addresses change by themselves.
func localAddrs() string {
	list, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var out []string
	for _, ad := range list {
		if ip, ok := ad.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() && !ip.IP.IsLinkLocalUnicast() {
			out = append(out, ip.IP.String())
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// networkChanged drops the connections made over the old network: the sync
// with the receiver starts again at once, the ASIAIR is connected anew.
// Files being sent go on from where the receiver has them.
func (a *Agent) networkChanged() {
	a.cl.closeIdle()
	if r, ok := a.source().(interface{ Reconnect() }); ok {
		r.Reconnect()
	}
	a.kickSync()
	a.rescan.Store(true)
	a.wakeUp()
}

// parseNetsh reads "netsh wlan show interfaces" (in any language: the
// SSID key is the same, the signal is the value with "%").
func parseNetsh(out string) *protocol.WiFiInfo {
	var cur, found *protocol.WiFiInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !utf8.ValidString(v) {
			v = strings.ToValidUTF8(v, "?")
		}
		switch {
		case k == "Name" || k == "Имя":
			cur = &protocol.WiFiInfo{Iface: v}
		case k == "SSID" && v != "":
			if cur == nil {
				cur = &protocol.WiFiInfo{}
			}
			cur.SSID = v
			if found == nil {
				found = cur
			}
		case strings.HasSuffix(v, "%") && cur != nil:
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(v, "%"))); err == nil {
				cur.Signal = n
			}
		}
	}
	return found
}
