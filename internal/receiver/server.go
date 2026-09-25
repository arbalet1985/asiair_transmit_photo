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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
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
	// free space kept on the disk with the photos
	reserve   config.Reserve
	minFree   string
	spaceNeed uint64 // size of the last upload refused for lack of space

	fingerprint string
	tlsCert     *tls.Certificate

	// Addresses actually bound (useful when configured with port 0).
	APIAddr, UIAddr net.Addr

	// Startup, when set, lets the web page manage starting with Windows.
	Startup Startup
	// Restart, when set, runs the program again after an update was
	// installed (svc.Restart). Without it the receiver does not update itself.
	Restart func() error
	// UpdateAPI is the GitHub API root; empty = api.github.com (tests change it).
	UpdateAPI string

	updater *updater

	previews *previewCache

	stopping chan struct{} // closed when Run ends: held sync requests answer at once

	// Firewall manages the Windows firewall rule (set by the program).
	Firewall Firewall
	fw       fwCache

	// the agent API on Listen, opened and closed from the page
	api    *http.Server
	netMu  sync.Mutex
	netLn  net.Listener
	netErr string

	// Tailscale: the agent API in the user's tailnet
	tsMu  sync.Mutex
	ts    *tsnode.Node
	tsSrv *http.Server
	tsErr string
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
	reserve, err := config.ParseReserve(cfg.MinFree)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: store, state: state, token: cfg.Token, previews: newPreviewCache(6),
		reserve: reserve, minFree: cfg.MinFree, stopping: make(chan struct{})}
	s.updater = newUpdater(s)
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
	s.api = &http.Server{Handler: s.apiHandler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	uiLn, err := net.Listen("tcp", s.cfg.UIListen)
	if err != nil {
		return fmt.Errorf("веб-страница %s: %w", s.cfg.UIListen, err)
	}
	s.UIAddr = uiLn.Addr()
	if s.cfg.InternetOn() {
		if err := s.startInternet(); err != nil {
			uiLn.Close()
			return err
		}
	} else {
		log.Printf("Приёмник: приём агента через интернет выключен (включается на веб-странице)")
	}
	defer s.stopInternet()
	api := s.api
	ui := &http.Server{Handler: s.uiHandler(), ReadHeaderTimeout: 30 * time.Second}
	log.Printf("Приёмник: веб-страница http://%s, фото сохраняются в %s", uiLn.Addr(), s.getStore().root)

	log.Printf("Приёмник: версия %s", protocol.Version)
	go s.updater.run(ctx)
	if s.cfg.TailscaleOn {
		if err := s.startTailscale(); err != nil {
			log.Printf("Приёмник: Tailscale не запущен: %v", err)
		}
	}
	defer s.stopTailscale()

	errc := make(chan error, 1)
	go func() { errc <- ui.Serve(uiLn) }()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	close(s.stopping)
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
	mux.HandleFunc("/api/update/agent", s.handleUpdateAgent)
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
		} else if msg := s.checkSpace(size - off); msg != "" {
			// refuse before the agent starts sending the body
			http.Error(w, msg, http.StatusInsufficientStorage)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil || off < 0 || off > size {
			http.Error(w, "неверный offset", http.StatusBadRequest)
			return
		}
		if msg := s.checkSpace(size - off); msg != "" {
			http.Error(w, msg, http.StatusInsufficientStorage)
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
		s.state.FetchDone(p)
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

// maxHold limits how long a sync request is held open.
const maxHold = 60 * time.Second

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req protocol.SyncRequest
	if !readJSON(w, r, &req) {
		return
	}
	// long polling: answer when something for the agent changes
	if req.WaitMS > 0 && req.Rev != 0 {
		if ch := s.state.Changed(req.Rev); ch != nil {
			s.state.Sync(req) // the page sees the fresh status meanwhile
			t := time.NewTimer(min(time.Duration(req.WaitMS)*time.Millisecond, maxHold))
			defer t.Stop()
			select {
			case <-ch:
			case <-t.C:
			case <-r.Context().Done():
				return
			case <-s.stopping:
			}
		}
	}
	resp := s.state.Sync(req)
	resp.DiskFull = s.disk().Full
	resp.Update = s.updater.offer(req.Status)
	writeJSON(w, resp)
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
				remove := s.getStore().Remove
				if c.IsDir() {
					remove = s.getStore().RemoveDir
				}
				if err := remove(c.Path); err != nil {
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
	mux.HandleFunc("/ui/api/syncdir", s.handleUISyncDir)
	mux.HandleFunc("/ui/api/rate", s.handleUIRate)
	mux.HandleFunc("/ui/api/minfree", s.handleUIMinFree)
	mux.HandleFunc("/ui/api/update", s.handleUIUpdate)
	mux.HandleFunc("/ui/api/pause", s.handleUIPause)
	mux.HandleFunc("/ui/api/fetch", s.handleUIFetch)
	mux.HandleFunc("/ui/api/clear", s.handleUIClear)
	mux.HandleFunc("/ui/api/cancel", s.handleUICancel)
	mux.HandleFunc("/ui/file", s.handleUIFile)
	mux.HandleFunc("/ui/api/connect", s.handleUIConnect)
	mux.HandleFunc("/ui/api/tailscale", s.handleUITailscale)
	mux.HandleFunc("/ui/api/network", s.handleUINetwork)
	mux.HandleFunc("/ui/api/token", s.handleUINewToken)
	mux.HandleFunc("/ui/api/dirs", s.handleUIDirs)
	mux.HandleFunc("/ui/api/mkdir", s.handleUIMkdir)
	mux.HandleFunc("/ui/api/destdir", s.handleUIDestDir)
	mux.HandleFunc("/ui/api/startup", s.handleUIStartup)
	mux.HandleFunc("/ui/preview", s.handleUIPreview)
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

type uiState struct {
	Files         []uiFile             `json:"files"`
	Commands      []*Command           `json:"commands"`
	MaxRate       *int64               `json:"max_rate"`
	Paused        bool                 `json:"paused"`
	WaitStart     bool                 `json:"wait_start"`
	Fetch         []string             `json:"fetch"`
	SyncDirs      []string             `json:"sync_dirs"`
	Agent         protocol.AgentStatus `json:"agent"`
	AgentOnline   bool                 `json:"agent_online"`
	LastSeen      time.Time            `json:"last_seen"`
	InventoryTime time.Time            `json:"inventory_time"`
	Fingerprint   string               `json:"fingerprint"`
	DestDir       string               `json:"dest_dir"`
	Disk          diskInfo             `json:"disk"`
	Update        updateInfo           `json:"update"`
	Totals        uiTotals             `json:"totals"`
	Tailscale     tsInfo               `json:"tailscale"`
	Network       netInfo              `json:"network"`
}

func (s *Server) handleUIState(w http.ResponseWriter, r *http.Request) {
	snap := s.state.Snapshot()
	shares := []string{"EMMC Images", "Udisk"}
	for _, f := range snap.Inventory {
		if sh, _, ok := strings.Cut(f.Path, "/"); ok {
			shares = append(shares, sh)
		}
	}
	store := s.getStore()
	local, _ := store.List(shares)
	online := time.Since(snap.LastSeen) < 2*time.Minute
	files, totals := buildFiles(local, snap.Inventory, snap.Commands, snap.Status, online, store.Partials())
	out := uiState{
		Files:         files,
		Totals:        totals,
		Commands:      snap.Commands,
		MaxRate:       snap.MaxRate,
		Paused:        snap.Paused,
		WaitStart:     snap.WaitStart,
		Fetch:         snap.Fetch,
		SyncDirs:      snap.SyncDirs,
		Agent:         snap.Status,
		LastSeen:      snap.LastSeen,
		AgentOnline:   online,
		InventoryTime: snap.InvTime,
		Fingerprint:   s.fingerprint,
		DestDir:       store.root,
		Disk:          s.disk(),
		Update:        s.updater.info(),
		Tailscale:     s.tailscaleInfo(),
		Network:       s.netInfo(),
	}
	writeJSON(w, out)
}

func (s *Server) handleUIDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths     []string `json:"paths"`
		Dirs      []string `json:"dirs"` // folders deleted with everything in them
		AlsoLocal bool     `json:"also_local"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	clean := func(list []string) ([]string, bool) {
		var out []string
		for _, p := range list {
			cp, err := CleanPath(p)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return nil, false
			}
			out = append(out, cp)
		}
		return out, true
	}
	paths, ok := clean(req.Paths)
	if !ok {
		return
	}
	dirs, ok := clean(req.Dirs)
	if !ok {
		return
	}
	n, err := s.state.QueueDelete(paths, protocol.CmdDelete, req.AlsoLocal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nd, err := s.state.QueueDelete(dirs, protocol.CmdDeleteDir, req.AlsoLocal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n > 0 {
		log.Printf("Приёмник: поставлено в очередь на удаление: %d файл(ов)", n)
	}
	if nd > 0 {
		log.Printf("Приёмник: поставлено в очередь на удаление: %d папк(и) целиком", nd)
	}
	writeJSON(w, map[string]int{"queued": n + nd})
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

func (s *Server) handleUIPause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	waiting := s.state.Snapshot().WaitStart
	if err := s.state.SetPaused(req.Paused); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Paused {
		log.Printf("Приёмник: передача поставлена на паузу")
	} else if waiting {
		log.Printf("Приёмник: передача запущена")
	} else {
		log.Printf("Приёмник: пауза снята")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUISyncDir turns on or off the synchronisation of a folder while the
// transfer is paused.
func (s *Server) handleUISyncDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		On   bool   `json:"on"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	dir, err := CleanPath(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.state.SetSyncDir(dir, req.On); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.On {
		log.Printf("Приёмник: папка синхронизируется и на паузе: %s", dir)
	} else {
		log.Printf("Приёмник: синхронизация папки на паузе снята: %s", dir)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIFetch requests (or withdraws) files to download even while the
// transfer is paused or not started.
func (s *Server) handleUIFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths  []string `json:"paths"`
		Cancel bool     `json:"cancel"`
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
	n, err := s.state.RequestFetch(paths, !req.Cancel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Cancel {
		log.Printf("Приёмник: отменено скачивание: %d файл(ов)", n)
	} else {
		log.Printf("Приёмник: запрошено скачивание: %d файл(ов)", n)
	}
	writeJSON(w, map[string]int{"changed": n})
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
