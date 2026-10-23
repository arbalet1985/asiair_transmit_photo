package receiver

import (
	"context"
	"log"
	"net/http"
	"slices"
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
}

func (s *Server) agentCtl(st protocol.AgentStatus, online bool) agentCtl {
	var c agentCtl
	c.CanRestart = online && st.CanRestart
	c.RestartAt, c.SwitchTo = s.state.AgentCtlPending()
	c.SwitchBack = st.SwitchBack
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

// handleUIAgentCtl: {"action": "restart"} or {"action": "switch", "connection": "tailscale"}
func (s *Server) handleUIAgentCtl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action     string `json:"action"`
		Connection string `json:"connection"`
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
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
