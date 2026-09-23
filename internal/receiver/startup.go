package receiver

import (
	"log"
	"net/http"
	"time"
)

// Startup lets the web page manage how the receiver starts with Windows.
// It is provided by the program (see cmd/asiair-receiver); nil hides the
// block on the page.
type Startup interface {
	Info() StartupInfo
	SetAutostart(on bool) error
	// InstallService installs the Windows service (asking for administrator
	// rights) and hands over to it: the current process exits.
	InstallService() error
	// RemoveService removes the service; when called from the service
	// itself, the receiver stops.
	RemoveService() error
}

// StartupInfo describes how the receiver runs now.
type StartupInfo struct {
	Supported bool   `json:"supported"` // false outside Windows
	Mode      string `json:"mode"`      // ModeService, ModeTray or ModeConsole
	Autostart bool   `json:"autostart"` // started at logon (per-user Run key)
	Service   string `json:"service"`   // svc.ServiceNotInstalled / ServiceRunning / ServiceStopped
}

// Run modes.
const (
	ModeService = "service"
	ModeTray    = "tray"
	ModeConsole = "console"
)

func (s *Server) handleUIStartup(w http.ResponseWriter, r *http.Request) {
	if s.Startup == nil {
		writeJSON(w, StartupInfo{})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, s.Startup.Info())
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var err error
	switch req.Action {
	case "autostart-on":
		err = s.Startup.SetAutostart(true)
	case "autostart-off":
		err = s.Startup.SetAutostart(false)
	case "install-service":
		err = s.Startup.InstallService()
	case "remove-service":
		err = s.Startup.RemoveService()
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("Приёмник: запуск с Windows (%s): %v", req.Action, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.Startup.Info())
}

// AgentOnline reports whether the agent has been in touch recently.
func (s *Server) AgentOnline() bool {
	return time.Since(s.state.Snapshot().LastSeen) < 2*time.Minute
}

// AgentSummary is a short status line for the tray tooltip.
func (s *Server) AgentSummary() string {
	snap := s.state.Snapshot()
	if time.Since(snap.LastSeen) >= 2*time.Minute {
		return "агент не на связи"
	}
	if snap.WaitStart {
		return "передача ещё не запущена"
	}
	if snap.Paused || snap.Status.Paused {
		return "передача на паузе"
	}
	if snap.Status.Uploading != "" {
		return "идёт приём снимков"
	}
	return "агент на связи"
}
