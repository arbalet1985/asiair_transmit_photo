package receiver

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
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

// TrayInfo is the status shown when the pointer is over the tray icon: a
// few short lines (Windows shows at most 127 characters).
func (s *Server) TrayInfo() string {
	snap := s.state.Snapshot()
	st := snap.Status
	online := time.Since(snap.LastSeen) < 2*time.Minute
	line := s.AgentSummary()
	if online && len(st.Uploads) > 0 && st.Speed > 0 {
		line += " · " + config.FormatBytes(int64(st.Speed)) + "/s"
	}
	lines := []string{line}
	if online && st.QueueFiles > 0 {
		lines = append(lines, fmt.Sprintf("в очереди: %d файл(ов), %s", st.QueueFiles, config.FormatBytes(st.QueueBytes)))
	}
	if d := s.disk(); d.Error == "" && d.Total > 0 {
		t := "свободно на диске: " + config.FormatBytes(int64(d.Free))
		if d.Full != "" {
			t += " — мало!"
		}
		lines = append(lines, t)
	}
	return strings.Join(lines, "\n")
}

// TransferPaused reports whether the transfer is paused (or not started)
// from the receiver.
func (s *Server) TransferPaused() bool {
	snap := s.state.Snapshot()
	return snap.Paused || snap.WaitStart
}

// SetTransferPaused pauses or resumes (starts) the transfer, as the button
// on the page does.
func (s *Server) SetTransferPaused(p bool) error {
	waiting := s.state.Snapshot().WaitStart
	if err := s.state.SetPaused(p); err != nil {
		return err
	}
	switch {
	case p:
		log.Printf("Приёмник: передача поставлена на паузу (значок в трее)")
	case waiting:
		log.Printf("Приёмник: передача запущена (значок в трее)")
	default:
		log.Printf("Приёмник: пауза снята (значок в трее)")
	}
	return nil
}

// OpenDestDir shows the folder with the received photos in Explorer.
func (s *Server) OpenDestDir() error {
	return openInFolder(s.getStore().root, true)
}
