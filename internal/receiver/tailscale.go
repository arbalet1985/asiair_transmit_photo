package receiver

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
)

// tsInfo is the Tailscale part of the page state.
type tsInfo struct {
	Enabled  bool   `json:"enabled"`
	Hostname string `json:"hostname"` // wanted name
	Port     string `json:"port"`
	AuthKey  bool   `json:"authkey"` // an auth key is set
	tsnode.Status
}

// apiPort is the port of the agent API, also used in the tailnet.
func (s *Server) apiPort() string {
	s.netMu.Lock()
	addr := s.APIAddr
	s.netMu.Unlock()
	if addr != nil {
		if _, p, err := net.SplitHostPort(addr.String()); err == nil {
			return p
		}
	}
	if _, p, err := net.SplitHostPort(s.cfg.Listen); err == nil {
		return p
	}
	return "8443"
}

// startTailscale joins the tailnet and serves the agent API there.
func (s *Server) startTailscale() error {
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	if s.ts != nil {
		return nil
	}
	s.tsErr = ""
	n, err := tsnode.Start(tsnode.Config{
		Dir:        filepath.Join(s.cfg.DataDir, "tailscale"),
		Hostname:   s.cfg.TailscaleHostname,
		AuthKey:    s.cfg.TailscaleAuthKey,
		ControlURL: s.cfg.TailscaleControlURL,
	})
	if err != nil {
		s.tsErr = err.Error()
		return err
	}
	ln, err := n.Listen(":" + s.apiPort())
	if err != nil {
		n.Close()
		s.tsErr = err.Error()
		return err
	}
	if s.tlsCert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}, MinVersion: tls.VersionTLS12})
	}
	srv := &http.Server{Handler: s.apiHandler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("Приёмник: Tailscale: %v", err)
		}
	}()
	s.ts, s.tsSrv = n, srv
	log.Printf("Приёмник: Tailscale включён, имя %s, порт %s", s.cfg.TailscaleHostname, s.apiPort())
	return nil
}

// stopTailscale leaves the tailnet (the login is kept for the next start).
func (s *Server) stopTailscale() {
	s.tsMu.Lock()
	n, srv := s.ts, s.tsSrv
	s.ts, s.tsSrv = nil, nil
	s.tsMu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if srv.Shutdown(ctx) != nil {
			srv.Close()
		}
		cancel()
	}
	if n != nil {
		n.Close()
	}
}

func (s *Server) tailscaleInfo() tsInfo {
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	info := tsInfo{
		Enabled:  bool(s.cfg.TailscaleOn),
		Hostname: s.cfg.TailscaleHostname,
		Port:     s.apiPort(),
		AuthKey:  s.cfg.TailscaleAuthKey != "",
		Status:   tsnode.Status{State: tsnode.StateOff},
	}
	switch {
	case s.ts != nil:
		info.Status = s.ts.Status()
	case s.tsErr != "":
		info.Status = tsnode.Status{State: tsnode.StateError, Error: s.tsErr}
	}
	return info
}

// handleUITailscale: {"action": "enable"|"disable"|"login"|"logout", "authkey": "..."}
func (s *Server) handleUITailscale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string  `json:"action"`
		AuthKey *string `json:"authkey"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	save := func(values map[string]string) bool {
		if s.cfg.Path == "" {
			return true
		}
		if err := config.UpdateReceiverFile(s.cfg.Path, values); err != nil {
			http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
			return false
		}
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	switch req.Action {
	case "enable":
		values := map[string]string{"tailscale": "true"}
		if req.AuthKey != nil {
			values["tailscale_authkey"] = strings.TrimSpace(*req.AuthKey)
		}
		if !save(values) {
			return
		}
		s.tsMu.Lock()
		s.cfg.TailscaleOn = true
		if req.AuthKey != nil {
			s.cfg.TailscaleAuthKey = strings.TrimSpace(*req.AuthKey)
		}
		s.tsMu.Unlock()
		if err := s.startTailscale(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "disable":
		if !save(map[string]string{"tailscale": "false"}) {
			return
		}
		s.tsMu.Lock()
		s.cfg.TailscaleOn = false
		s.tsMu.Unlock()
		s.stopTailscale()
		log.Printf("Приёмник: Tailscale выключен")
	case "login", "logout":
		s.tsMu.Lock()
		n := s.ts
		s.tsMu.Unlock()
		if n == nil {
			http.Error(w, "Tailscale не включён", http.StatusBadRequest)
			return
		}
		var err error
		if req.Action == "login" {
			err = n.Login(ctx)
		} else {
			err = n.Logout(ctx)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.tailscaleInfo())
}
