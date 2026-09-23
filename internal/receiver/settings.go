package receiver

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

// ---------- connection data for the agent ----------

type connectInfo struct {
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	Port        string `json:"port"`
	TLS         bool   `json:"tls"`
	ConfigPath  string `json:"config_path"`
}

func (s *Server) handleUIConnect(w http.ResponseWriter, r *http.Request) {
	_, port, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		port = strings.TrimPrefix(s.cfg.Listen, ":")
	}
	writeJSON(w, connectInfo{
		Token:       s.getToken(),
		Fingerprint: s.fingerprint,
		Port:        port,
		TLS:         s.tlsCert != nil,
		ConfigPath:  s.cfg.Path,
	})
}

// handleUINewToken replaces the token; the agent must be given the new one.
func (s *Server) handleUINewToken(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	tok := NewToken()
	if s.cfg.Path != "" {
		if err := config.UpdateReceiverFile(s.cfg.Path, map[string]string{"token": tok}); err != nil {
			http.Error(w, "не удалось сохранить токен: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.mu.Lock()
	s.token = tok
	s.mu.Unlock()
	log.Printf("Приёмник: создан новый токен — укажите его в настройках агента")
	writeJSON(w, map[string]string{"token": tok})
}

// ---------- destination folder ----------

type dirList struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent"`
	Dirs   []dirEntry `json:"dirs"`
	Roots  []string   `json:"roots"`
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// roots returns the drives (Windows) or "/" to start browsing from.
func roots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// hiddenDir hides folders that are of no use for photos.
func hiddenDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") ||
		strings.EqualFold(name, "System Volume Information") || name == partialDir
}

func (s *Server) handleUIDirs(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		p = s.getStore().root
	}
	if !filepath.IsAbs(p) {
		http.Error(w, "нужен полный путь, например D:\\Astro", http.StatusBadRequest)
		return
	}
	p = filepath.Clean(p)
	// a folder that does not exist yet: show its nearest existing parent
	for {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		http.Error(w, "не удалось открыть "+p+": "+err.Error(), http.StatusBadRequest)
		return
	}
	out := dirList{Path: p, Roots: roots(), Dirs: []dirEntry{}}
	if parent := filepath.Dir(p); parent != p {
		out.Parent = parent
	}
	for _, e := range entries {
		if e.IsDir() && !hiddenDir(e.Name()) {
			out.Dirs = append(out.Dirs, dirEntry{Name: e.Name(), Path: filepath.Join(p, e.Name())})
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i].Name) < strings.ToLower(out.Dirs[j].Name) })
	writeJSON(w, out)
}

func (s *Server) handleUIMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:*?"<>|`) {
		http.Error(w, "недопустимое имя папки", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(req.Parent) {
		http.Error(w, "нужен полный путь", http.StatusBadRequest)
		return
	}
	p := filepath.Join(filepath.Clean(req.Parent), name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		http.Error(w, "не удалось создать папку: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"path": p})
}

// checkWritable creates dir if needed and verifies files can be written there.
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".asiair-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// SetDestDir switches the destination folder and saves it in receiver.yaml.
// Files received earlier stay in the old folder.
func (s *Server) SetDestDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("нужен полный путь, например D:\\Astro\\ASIAIR")
	}
	dir = filepath.Clean(dir)
	if err := checkWritable(dir); err != nil {
		return fmt.Errorf("в папку %s нельзя записывать: %w", dir, err)
	}
	store, err := NewStore(dir)
	if err != nil {
		return err
	}
	if s.cfg.Path != "" {
		if err := config.UpdateReceiverFile(s.cfg.Path, map[string]string{"dest_dir": dir}); err != nil {
			return fmt.Errorf("не удалось сохранить настройки: %w", err)
		}
	}
	s.mu.Lock()
	old := s.store.root
	s.store = store
	s.mu.Unlock()
	log.Printf("Приёмник: папка для снимков изменена: %s → %s", old, dir)
	return nil
}

func (s *Server) handleUIDestDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.SetDestDir(strings.TrimSpace(req.Path)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"path": s.getStore().root})
}
