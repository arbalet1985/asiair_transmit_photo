package receiver

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// Logs of the agent, asked from the receiver page: the agent sends a zip,
// the receiver keeps the last few in <data>/agent-logs; the page downloads
// one together with the receiver's own logs of the same days.
const (
	agentLogsKeep = 5
	agentLogsMax  = 128 << 20
	logsDays      = 3 // days asked by default (today counts as one)
)

var agentLogsName = regexp.MustCompile(`^asiair-agent-logs-\d{8}-\d{6}\.zip$`)

func (s *Server) agentLogsDir() string { return filepath.Join(s.cfg.DataDir, "agent-logs") }

// StartLogs asks the agent for its logs of the last days.
func (st *State) StartLogs(days int) (*protocol.LogsRequest, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.logsReq != nil || (st.p.Status.Logs != nil && st.p.Status.Logs.Running) {
		return nil, fmt.Errorf("журналы уже запрошены — дождитесь, пока агент их пришлёт")
	}
	st.logsReq = &protocol.LogsRequest{ID: time.Now().UnixNano(), Days: days}
	st.bump()
	return st.logsReq, nil
}

// LogsPending is the logs request the agent has not answered yet.
func (st *State) LogsPending() *protocol.LogsRequest {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.logsReq
}

// logsForSync adds the pending logs request to the answer to the agent, or
// drops it once the agent has answered. Caller holds st.mu.
func (st *State) logsForSync(resp *protocol.SyncResponse) {
	r := st.logsReq
	if r == nil {
		return
	}
	if res := st.p.Status.Logs; res != nil && res.ID == r.ID && !res.Running {
		st.logsReq = nil
		return
	}
	resp.Logs = r
}

// handleAgentLogs stores the zip of logs sent by the agent: {"name": …}.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	dir := s.agentLogsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := "asiair-agent-logs-" + time.Now().Format("20060102-150405") + ".zip"
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := io.Copy(tmp, io.LimitReader(r.Body, agentLogsMax+1))
	tmp.Close()
	if err == nil && n > agentLogsMax {
		err = fmt.Errorf("архив больше %d МБ", agentLogsMax>>20)
	}
	if err == nil {
		// a whole zip, not a broken upload; closed at once, or Windows
		// refuses the rename below
		var zr *zip.ReadCloser
		if zr, err = zip.OpenReader(tmp.Name()); err == nil {
			zr.Close()
		}
	}
	if err != nil {
		os.Remove(tmp.Name())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// keep the last few
	if list := s.agentLogs(); len(list) > agentLogsKeep {
		for _, old := range list[agentLogsKeep:] {
			os.Remove(filepath.Join(dir, old.Name))
		}
	}
	log.Printf("Приёмник: получены журналы агента (%s)", name)
	writeJSON(w, map[string]string{"name": name})
}

type agentLogsFile struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// agentLogs lists the stored archives, newest first.
func (s *Server) agentLogs() []agentLogsFile {
	entries, _ := os.ReadDir(s.agentLogsDir())
	var out []agentLogsFile
	for _, e := range entries {
		if !agentLogsName.MatchString(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, agentLogsFile{Name: e.Name(), Time: info.ModTime(), Size: info.Size()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// handleUIAgentLogs: POST {"days": 3} asks the agent for its logs.
func (s *Server) handleUIAgentLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Days int `json:"days"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Days <= 0 {
		req.Days = logsDays
	}
	if _, err := s.state.StartLogs(min(req.Days, 14)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Приёмник: запрошены журналы агента")
	w.WriteHeader(http.StatusNoContent)
}

// handleUILogsZip downloads ?name=<agent archive> with the receiver logs of
// the same days: agent/… and receiver/… in one zip. Without a name only
// the receiver logs are in it.
func (s *Server) handleUILogsZip(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var agent *zip.ReadCloser
	since := svc.LogsSince(logsDays)
	if name != "" {
		if !agentLogsName.MatchString(name) {
			http.Error(w, "неверное имя", http.StatusBadRequest)
			return
		}
		var err error
		if agent, err = zip.OpenReader(filepath.Join(s.agentLogsDir(), name)); err != nil {
			http.Error(w, "нет такого архива", http.StatusNotFound)
			return
		}
		defer agent.Close()
		if info, err := os.Stat(filepath.Join(s.agentLogsDir(), name)); err == nil {
			since = svc.LogsSince(logsDays).Add(info.ModTime().Sub(time.Now())) // the days of the agent archive
		}
	}
	out := "asiair-logs-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+out+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	if agent != nil {
		for _, f := range agent.File {
			if err := copyZipEntry(zw, f, "agent/"); err != nil {
				log.Printf("Приёмник: журналы: %v", err)
				return
			}
		}
	}
	main := s.cfg.LogFile
	if main == "" {
		main = svc.DefaultLogFile("asiair-receiver")
	}
	for _, p := range svc.LogFiles(main, since) {
		if err := addZipFile(zw, p, "receiver/"); err != nil {
			log.Printf("Приёмник: журналы: %v", err)
			return
		}
	}
}

func copyZipEntry(zw *zip.Writer, f *zip.File, prefix string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	h := f.FileHeader
	h.Name = prefix + filepath.Base(f.Name)
	w, err := zw.CreateHeader(&h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

func addZipFile(zw *zip.Writer, p, prefix string) error {
	f, err := os.Open(p)
	if err != nil {
		return nil // rotated away meanwhile
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	h := &zip.FileHeader{Name: prefix + filepath.Base(p), Method: zip.Deflate, Modified: info.ModTime()}
	if strings.HasSuffix(p, ".gz") {
		h.Method = zip.Store
	}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(f, info.Size()))
	return err
}
