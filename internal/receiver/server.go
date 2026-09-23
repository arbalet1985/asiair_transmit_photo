// Package receiver accepts files from the agent, stores them, and serves a
// web page from which files can be deleted on the ASIAIR.
package receiver

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

//go:embed web/index.html
var webFS embed.FS

// Server is the receiver service.
type Server struct {
	cfg   *config.Receiver
	state *State

	// store and token can be changed from the web page while running
	mu    sync.RWMutex
	store *Store
	token string

	fingerprint string
	tlsCert     *tls.Certificate

	// Addresses actually bound (useful when configured with port 0).
	APIAddr, UIAddr net.Addr
}

func New(cfg *config.Receiver) (*Server, error) {
	store, err := NewStore(cfg.DestDir)
	if err != nil {
		return nil, fmt.Errorf("папка для фото %s: %w", cfg.DestDir, err)
	}
	var def *int64
	if cfg.MaxRate != "" {
		v, _ := config.ParseRate(cfg.MaxRate)
		def = &v
	}
	state, err := LoadState(filepath.Join(cfg.DataDir, "state.json"), def)
	if err != nil {
		return nil, fmt.Errorf("состояние: %w", err)
	}
	s := &Server{cfg: cfg, store: store, state: state, token: cfg.Token}
	if !cfg.DisableTLS {
		cert, fp, err := loadOrCreateCert(cfg.TLSCert, cfg.TLSKey, cfg.DataDir)
		if err != nil {
			return nil, err
		}
		s.tlsCert, s.fingerprint = &cert, fp
	}
	return s, nil
}

// Fingerprint of the TLS certificate (empty when TLS is disabled).
func (s *Server) Fingerprint() string { return s.fingerprint }

func (s *Server) getStore() *Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

func (s *Server) getToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	apiLn, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("API %s: %w", s.cfg.Listen, err)
	}
	uiLn, err := net.Listen("tcp", s.cfg.UIListen)
	if err != nil {
		apiLn.Close()
		return fmt.Errorf("веб-страница %s: %w", s.cfg.UIListen, err)
	}
	s.APIAddr, s.UIAddr = apiLn.Addr(), uiLn.Addr()

	api := &http.Server{Handler: s.apiHandler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	ui := &http.Server{Handler: s.uiHandler(), ReadHeaderTimeout: 30 * time.Second}
	if s.tlsCert != nil {
		apiLn = tls.NewListener(apiLn, &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}, MinVersion: tls.VersionTLS12})
		log.Printf("Приёмник: API https://%s, отпечаток сертификата: %s", apiLn.Addr(), s.fingerprint)
	} else {
		log.Printf("Приёмник: API http://%s (TLS отключён)", apiLn.Addr())
	}
	log.Printf("Приёмник: веб-страница http://%s, фото сохраняются в %s", uiLn.Addr(), s.getStore().root)

	errc := make(chan error, 2)
	go func() { errc <- api.Serve(apiLn) }()
	go func() { errc <- ui.Serve(uiLn) }()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	// Listeners close at once; an upload in progress gets a moment to
	// finish and is then cut (the agent resumes it later).
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range []*http.Server{api, ui} {
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			if srv.Shutdown(sctx) != nil {
				srv.Close()
			}
		}(srv)
	}
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// ---------- agent API ----------

func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/commit", s.handleCommit)
	mux.HandleFunc("/api/sync", s.handleSync)
	mux.HandleFunc("/api/inventory", s.handleInventory)
	mux.HandleFunc("/api/result", s.handleResult)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := []byte("Bearer " + s.getToken())
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			log.Printf("Приёмник: неверный токен от %s", r.RemoteAddr)
			time.Sleep(time.Second) // slow down guessing
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fileParams(r *http.Request) (p string, size int64, mtime time.Time, err error) {
	q := r.URL.Query()
	if p, err = CleanPath(q.Get("path")); err != nil {
		return
	}
	if size, err = strconv.ParseInt(q.Get("size"), 10, 64); err != nil || size < 0 {
		err = errors.New("неверный size")
		return
	}
	ns, err := strconv.ParseInt(q.Get("mtime"), 10, 64)
	if err != nil {
		err = errors.New("неверный mtime")
		return
	}
	return p, size, time.Unix(0, ns), nil
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, size, mtime, err := fileParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodHead, http.MethodGet:
		off, done := s.getStore().Offset(p, size, mtime)
		w.Header().Set(protocol.HeaderOffset, strconv.FormatInt(off, 10))
		if done {
			w.Header().Set(protocol.HeaderComplete, "1")
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil || off < 0 || off > size {
			http.Error(w, "неверный offset", http.StatusBadRequest)
			return
		}
		got, sum, err := s.getStore().Write(p, size, mtime, off, r.Body)
		w.Header().Set(protocol.HeaderOffset, strconv.FormatInt(got, 10))
		var eo *ErrOffset
		switch {
		case errors.As(err, &eo):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			log.Printf("Приёмник: %s: передача прервана на %d из %d байт: %v", p, got, size, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, protocol.UploadResult{Offset: got, SHA256: sum})
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, size, mtime, err := fileParams(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = s.getStore().Commit(p, size, mtime, r.URL.Query().Get("sha256"))
	var eo *ErrOffset
	switch {
	case errors.Is(err, ErrChecksum):
		log.Printf("Приёмник: %s: %v", p, err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.As(err, &eo):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		log.Printf("Приёмник: %s: %v", p, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		log.Printf("Приёмник: получен %s (%s)", p, config.FormatBytes(size))
		w.WriteHeader(http.StatusOK)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req protocol.SyncRequest
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, s.state.Sync(req.Status))
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	var inv protocol.Inventory
	if !readJSON(w, r, &inv) {
		return
	}
	if err := s.state.SetInventory(inv.Files); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	var res protocol.CommandResult
	if !readJSON(w, r, &res) {
		return
	}
	c, err := s.state.Result(res)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c != nil {
		if res.OK {
			log.Printf("Приёмник: удалено на ASIAIR: %s", c.Path)
			if c.AlsoLocal {
				if err := s.getStore().Remove(c.Path); err != nil {
					log.Printf("Приёмник: не удалось удалить локальную копию %s: %v", c.Path, err)
				}
			}
		} else {
			log.Printf("Приёмник: ошибка удаления %s на ASIAIR: %s", c.Path, res.Error)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- web page ----------

func (s *Server) uiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(b)
	})
	mux.HandleFunc("/ui/api/state", s.handleUIState)
	mux.HandleFunc("/ui/api/delete", s.handleUIDelete)
	mux.HandleFunc("/ui/api/rate", s.handleUIRate)
	mux.HandleFunc("/ui/api/clear", s.handleUIClear)
	mux.HandleFunc("/ui/api/cancel", s.handleUICancel)
	mux.HandleFunc("/ui/file", s.handleUIFile)
	mux.HandleFunc("/ui/api/connect", s.handleUIConnect)
	mux.HandleFunc("/ui/api/token", s.handleUINewToken)
	mux.HandleFunc("/ui/api/dirs", s.handleUIDirs)
	mux.HandleFunc("/ui/api/mkdir", s.handleUIMkdir)
	mux.HandleFunc("/ui/api/destdir", s.handleUIDestDir)
	return s.uiAuth(mux)
}

func (s *Server) uiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.UIPassword != "" {
			u, p, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(s.cfg.UIUser)) != 1 ||
				subtle.ConstantTimeCompare([]byte(p), []byte(s.cfg.UIPassword)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="ASIAIR receiver", charset="UTF-8"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		// State-changing requests must be JSON: browsers cannot send that
		// cross-site without a CORS preflight, which we never allow.
		if r.Method == http.MethodPost {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" {
				http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type uiFile struct {
	Path       string    `json:"path"`
	Folder     string    `json:"folder"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	MTime      time.Time `json:"mtime"`
	Local      bool      `json:"local"`
	Remote     bool      `json:"remote"`
	RemoteSize int64     `json:"remote_size"`
	Pending    bool      `json:"pending"`
}

type uiState struct {
	Files         []uiFile             `json:"files"`
	Commands      []*Command           `json:"commands"`
	MaxRate       *int64               `json:"max_rate"`
	Agent         protocol.AgentStatus `json:"agent"`
	AgentOnline   bool                 `json:"agent_online"`
	LastSeen      time.Time            `json:"last_seen"`
	InventoryTime time.Time            `json:"inventory_time"`
	Fingerprint   string               `json:"fingerprint"`
	DestDir       string               `json:"dest_dir"`
}

func (s *Server) handleUIState(w http.ResponseWriter, r *http.Request) {
	snap := s.state.Snapshot()
	shares := []string{"EMMC Images", "Udisk"}
	for _, f := range snap.Inventory {
		if sh, _, ok := strings.Cut(f.Path, "/"); ok {
			shares = append(shares, sh)
		}
	}
	local, _ := s.getStore().List(shares)
	byPath := map[string]*uiFile{}
	var files []*uiFile
	get := func(p string) *uiFile {
		if f, ok := byPath[p]; ok {
			return f
		}
		f := &uiFile{Path: p, Folder: path.Dir(p), Name: path.Base(p)}
		byPath[p] = f
		files = append(files, f)
		return f
	}
	for _, l := range local {
		f := get(l.Path)
		f.Local, f.Size, f.MTime = true, l.Size, l.ModTime
	}
	for _, rf := range snap.Inventory {
		f := get(rf.Path)
		f.Remote, f.RemoteSize = true, rf.Size
		if !f.Local {
			f.Size, f.MTime = rf.Size, rf.ModTime
		}
	}
	for _, c := range snap.Commands {
		if c.Status == StatusPending {
			if f, ok := byPath[c.Path]; ok {
				f.Pending = true
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := uiState{
		Files:         make([]uiFile, len(files)),
		Commands:      snap.Commands,
		MaxRate:       snap.MaxRate,
		Agent:         snap.Status,
		LastSeen:      snap.LastSeen,
		AgentOnline:   time.Since(snap.LastSeen) < 2*time.Minute,
		InventoryTime: snap.InvTime,
		Fingerprint:   s.fingerprint,
		DestDir:       s.getStore().root,
	}
	for i, f := range files {
		out.Files[i] = *f
	}
	writeJSON(w, out)
}

func (s *Server) handleUIDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths     []string `json:"paths"`
		AlsoLocal bool     `json:"also_local"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var paths []string
	for _, p := range req.Paths {
		cp, err := CleanPath(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		paths = append(paths, cp)
	}
	n, err := s.state.QueueDelete(paths, req.AlsoLocal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Приёмник: поставлено в очередь на удаление: %d файл(ов)", n)
	writeJSON(w, map[string]int{"queued": n})
}

func (s *Server) handleUIRate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rate *string `json:"rate"` // null = agent's own setting
	}
	if !readJSON(w, r, &req) {
		return
	}
	var bps *int64
	if req.Rate != nil {
		v, err := config.ParseRate(*req.Rate)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bps = &v
	}
	if err := s.state.SetRate(bps); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if bps == nil {
		log.Printf("Приёмник: ограничение скорости — из настроек агента")
	} else {
		log.Printf("Приёмник: ограничение скорости: %s", config.FormatRate(*bps))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUIClear(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.state.ClearFinished(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUICancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.state.Cancel(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIFile serves a received file (for previewing JPG/PNG in the browser).
func (s *Server) handleUIFile(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(path.Ext(p))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, s.getStore().Local(p))
}
