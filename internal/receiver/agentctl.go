package receiver

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// The page restarts the agent and switches its connection between the
// internet and Tailscale (when both are set up here and on the agent).

type channelKey struct{}

// withChannel marks the requests that came through a channel
// (protocol.ConnInternet or ConnTailscale).
func withChannel(channel string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), channelKey{}, channel)))
	})
}

// noteAgentURL remembers the address the agent reached the receiver at.
func (s *Server) noteAgentURL(r *http.Request, req protocol.SyncRequest) {
	ch, _ := r.Context().Value(channelKey{}).(string)
	if ch == "" || req.Session == "" || r.Host == "" {
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	s.state.NoteAgentURL(ch, scheme+"://"+r.Host)
}

// agentCtl is what the page can ask of the agent.
type agentCtl struct {
	CanRestart bool      `json:"can_restart"`
	RestartAt  time.Time `json:"restart_at,omitempty"` // asked, the agent not back yet
	// Switch: the connection the agent can be switched to ("" none);
	// SwitchWhy: why there is none, when the agent is new enough to switch.
	Switch    string `json:"switch,omitempty"`
	SwitchWhy string `json:"switch_why,omitempty"`
	SwitchTo  string `json:"switch_to,omitempty"` // being switched to
	// SwitchBack: the agent was switched and is not yet confirmed.
	SwitchBack string `json:"switch_back,omitempty"`
	// the Tailscale login of the agent: whether it takes new credentials
	// (TSWhy: why not), its name and coordination server, credentials on
	// the way, a new login not yet confirmed
	CanTSLogin   bool   `json:"can_ts_login,omitempty"`
	TSWhy        string `json:"ts_why,omitempty"`
	TSHostname   string `json:"ts_hostname,omitempty"`
	TSControlURL string `json:"ts_control_url,omitempty"`
	TSLoginBusy  bool   `json:"ts_login_busy,omitempty"`
	TSLoginBack  bool   `json:"ts_login_back,omitempty"`
	TSNeedLogin  bool   `json:"ts_need_login,omitempty"`
}

func (s *Server) agentCtl(st protocol.AgentStatus, online bool) agentCtl {
	var c agentCtl
	c.CanRestart = online && st.CanRestart
	c.RestartAt, c.SwitchTo = s.state.AgentCtlPending()
	c.SwitchBack = st.SwitchBack
	if st.CanTSLogin {
		c.TSHostname, c.TSControlURL, c.TSLoginBack, c.TSNeedLogin = st.TSHostname, st.TSControlURL, st.TSLoginBack, st.TSNeedLogin
		c.TSLoginBusy = s.state.TSLoginPending()
		_, c.TSWhy = s.tsLoginURLs()
		if !online {
			c.TSWhy = "агент не на связи"
		}
		c.CanTSLogin = c.TSWhy == ""
	}
	if !st.CanRestart || st.Modes == nil {
		return c // an old agent
	}
	to, _, why := s.switchTarget(st)
	if !online {
		why = "агент не на связи"
	}
	if why != "" {
		c.SwitchWhy = why
		return c
	}
	c.Switch = to
	return c
}

// switchTarget is the other connection, the receiver addresses for it, or
// why the agent cannot be switched.
func (s *Server) switchTarget(st protocol.AgentStatus) (to string, urls []string, why string) {
	to = protocol.ConnTailscale
	if st.Connection == protocol.ConnTailscale {
		to = protocol.ConnInternet
	}
	if st.SwitchBack != "" {
		return to, nil, "прошлое переключение ещё не подтверждено"
	}
	if u := s.state.AgentURL(to); u != "" {
		urls = append(urls, u)
	}
	if to == protocol.ConnTailscale {
		ts := s.tailscaleInfo()
		if ts.State != tsnode.StateRunning {
			return to, nil, "Tailscale на приёмнике не включён"
		}
		if !slices.Contains(st.Modes, protocol.ConnTailscale) {
			return to, nil, "Tailscale на агенте не настроен"
		}
		scheme := "https"
		if s.tlsCert == nil {
			scheme = "http"
		}
		for _, h := range []string{ts.Name, ts.DNSName, ts.IP} {
			if u := scheme + "://" + h + ":" + s.apiPort(); h != "" && !slices.Contains(urls, u) {
				urls = append(urls, u)
			}
		}
		return to, urls, ""
	}
	if n := s.netInfo(); !n.Internet || !n.Listening {
		return to, nil, "приём агента через интернет на приёмнике выключен"
	}
	if len(urls) == 0 && !slices.Contains(st.Modes, protocol.ConnInternet) {
		return to, nil, "интернет-адрес приёмника агенту неизвестен — задайте его в настройках агента"
	}
	return to, urls, ""
}

// tsLoginURLs: the receiver in the tailnet, for the agent to check a new
// login, or why there is none.
func (s *Server) tsLoginURLs() ([]string, string) {
	ts := s.tailscaleInfo()
	if ts.State != tsnode.StateRunning {
		return nil, "Tailscale на приёмнике не включён — агенту не через что проверить новый вход"
	}
	scheme := "https"
	if s.tlsCert == nil {
		scheme = "http"
	}
	var urls []string
	if u := s.state.AgentURL(protocol.ConnTailscale); u != "" {
		urls = append(urls, u)
	}
	for _, h := range []string{ts.Name, ts.DNSName, ts.IP} {
		if u := scheme + "://" + h + ":" + s.apiPort(); h != "" && !slices.Contains(urls, u) {
			urls = append(urls, u)
		}
	}
	return urls, ""
}

// handleUIAgentCtl: {"action": "restart"}, {"action": "switch", "connection": "tailscale"}
// or {"action": "tslogin", "auth_key": …, "control_url": …, "hostname": …}
func (s *Server) handleUIAgentCtl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action     string `json:"action"`
		Connection string `json:"connection"`
		AuthKey    string `json:"auth_key"`
		ControlURL string `json:"control_url"`
		Hostname   string `json:"hostname"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	snap := s.state.Snapshot()
	if time.Since(snap.LastSeen) > 2*time.Minute {
		http.Error(w, "агент не на связи", http.StatusConflict)
		return
	}
	st := snap.Status
	switch req.Action {
	case "restart":
		if !st.CanRestart {
			http.Error(w, "этот агент не умеет перезапускаться по команде — обновите его", http.StatusConflict)
			return
		}
		s.state.AskRestart()
		log.Printf("Приёмник: агента попросили перезапуститься")
	case "switch":
		to, urls, why := s.switchTarget(st)
		switch {
		case !st.CanRestart || st.Modes == nil:
			why = "этот агент не умеет переключать связь — обновите его"
		case req.Connection != to:
			why = "агент уже на связи этим способом"
		}
		if why != "" {
			http.Error(w, why, http.StatusConflict)
			return
		}
		s.state.AskSwitch(to, urls)
		log.Printf("Приёмник: агента попросили переключить связь на %s (адреса: %v)", to, urls)
	case "tslogin":
		urls, why := s.tsLoginURLs()
		key, ctl, host := strings.TrimSpace(req.AuthKey), strings.TrimSpace(req.ControlURL), strings.TrimSpace(req.Hostname)
		switch {
		case !st.CanTSLogin:
			why = "этот агент не принимает вход Tailscale с приёмника — обновите его"
		case st.TSLoginBack:
			why = "прошлый новый вход ещё не подтверждён"
		case key == "":
			why = "введите ключ авторизации Tailscale (tskey-auth-…)"
		case strings.ContainsAny(key, " \t\r\n"):
			why = "в ключе не должно быть пробелов"
		case host != "" && !validHostname(host):
			why = "имя в Tailscale: латинские буквы, цифры и дефис"
		case ctl != "" && !strings.HasPrefix(ctl, "https://") && !strings.HasPrefix(ctl, "http://"):
			why = "сервер управления — адрес вида https://…"
		}
		if why != "" {
			http.Error(w, why, http.StatusConflict)
			return
		}
		s.state.AskTSLogin(protocol.TSLogin{AuthKey: key, ControlURL: ctl, Hostname: host, ReceiverURLs: urls})
		log.Printf("Приёмник: агенту отправлен новый вход Tailscale (имя %q, сервер %q)", host, ctl) // never the key
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validHostname(h string) bool { return hostnameRe.MatchString(h) }
