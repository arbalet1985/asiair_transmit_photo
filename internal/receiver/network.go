package receiver

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// processStart is when this program started.
var processStart = time.Now()

// timings of listen (tests shorten them)
var (
	listenGrace = 30 * time.Second
	listenRetry = 500 * time.Millisecond
)

// listen opens a port. Right after a restart (update, rollback, the tray
// command) the previous copy of the program may still hold it for a moment,
// so a busy port is tried again until listenGrace after the start.
func listen(addr string) (net.Listener, error) {
	restarted := os.Getenv(update.RestartEnv) != ""
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || !restarted || time.Since(processStart) > listenGrace {
			return ln, err
		}
		time.Sleep(listenRetry)
	}
}

// Firewall manages the Windows firewall rule of the receiver (set by the
// program; nil elsewhere).
type Firewall interface {
	Supported() bool
	Allowed() bool
	// Allow adds the rule; it may ask for administrator rights and return
	// before the rule exists.
	Allow() error
}

// netInfo is the connection part of the page state.
type netInfo struct {
	Internet  bool   `json:"internet"`  // the setting
	Listening bool   `json:"listening"` // the port is open now
	Port      string `json:"port"`
	Error     string `json:"error,omitempty"`
	FWSupport bool   `json:"fw_supported"`
	FWAllowed bool   `json:"fw_allowed"`
}

// fwCache avoids running netsh for every page refresh.
type fwCache struct {
	mu      sync.Mutex
	at      time.Time
	allowed bool
	ttl     time.Duration
}

func (s *Server) firewallAllowed() bool {
	if s.Firewall == nil || !s.Firewall.Supported() {
		return false
	}
	c := &s.fw
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl == 0 {
		c.ttl = time.Minute
	}
	if time.Since(c.at) > c.ttl {
		c.allowed, c.at = s.Firewall.Allowed(), time.Now()
		if c.allowed {
			c.ttl = time.Minute
		}
	}
	return c.allowed
}

// startInternet opens the agent API on Listen.
func (s *Server) startInternet() error {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netLn != nil {
		return nil
	}
	ln, err := listen(s.cfg.Listen)
	if err != nil {
		s.netErr = err.Error()
		return fmt.Errorf("API %s: %w", s.cfg.Listen, err)
	}
	s.netErr = ""
	s.APIAddr = ln.Addr()
	if s.tlsCert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}, MinVersion: tls.VersionTLS12})
		log.Printf("Приёмник: API https://%s, отпечаток сертификата: %s", ln.Addr(), s.fingerprint)
	} else {
		log.Printf("Приёмник: API http://%s (TLS отключён)", ln.Addr())
	}
	s.netLn = ln
	go func() {
		if err := s.api.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("Приёмник: API: %v", err)
		}
	}()
	return nil
}

// stopInternet closes the port; connections in progress end on their own.
func (s *Server) stopInternet() {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netLn != nil {
		s.netLn.Close()
		s.netLn = nil
	}
}

func (s *Server) netInfo() netInfo {
	s.netMu.Lock()
	info := netInfo{Internet: s.cfg.InternetOn(), Listening: s.netLn != nil, Error: s.netErr}
	s.netMu.Unlock()
	info.Port = s.apiPort()
	if s.Firewall != nil && s.Firewall.Supported() {
		info.FWSupport = true
		info.FWAllowed = s.firewallAllowed()
	}
	return info
}

// handleUINetwork: {"action": "internet-on"|"internet-off"|"firewall-allow"}
func (s *Server) handleUINetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	setInternet := func(on bool) bool {
		if s.cfg.Path != "" {
			if err := config.UpdateReceiverFile(s.cfg.Path, map[string]string{"internet": fmt.Sprint(on)}); err != nil {
				http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
				return false
			}
		}
		v := config.Bool(on)
		s.netMu.Lock()
		s.cfg.Internet = &v
		s.netMu.Unlock()
		return true
	}
	switch req.Action {
	case "internet-on":
		if !setInternet(true) {
			return
		}
		if err := s.startInternet(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("Приёмник: приём агента через интернет включён")
	case "internet-off":
		if !setInternet(false) {
			return
		}
		s.stopInternet()
		log.Printf("Приёмник: приём агента через интернет выключен (порт %s закрыт)", s.apiPort())
	case "firewall-allow":
		if s.Firewall == nil || !s.Firewall.Supported() {
			http.Error(w, "брандмауэр Windows есть только в Windows", http.StatusBadRequest)
			return
		}
		if err := s.Firewall.Allow(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// the rule may appear a bit later (after the administrator prompt)
		s.fw.mu.Lock()
		s.fw.at, s.fw.ttl = time.Time{}, 2*time.Second
		s.fw.mu.Unlock()
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.netInfo())
}
