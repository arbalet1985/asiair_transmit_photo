package receiver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// How long a new receiver has to start before the watchdog puts the old
// one back (a service is started again by Windows only after 30 s).
var (
	healthDeadline        = 3 * time.Minute
	healthDeadlineService = 6 * time.Minute
)

// How often GitHub is asked for a new release.
const (
	updateFirstCheck = 30 * time.Second
	updateInterval   = 6 * time.Hour
)

// agentPlatform is the platform of the agent program in the releases.
var agentPlatform = "windows/amd64"

// agentFile is the agent program downloaded for the agent.
type agentFile struct {
	Version string
	Path    string
	Size    int64
	SHA256  string
	Build   string // protocol.Build of the program, from the release ("" = unknown)
}

// updater looks for new releases, updates the receiver itself and keeps
// the new agent program for the agent.
type updater struct {
	s   *Server
	dir string // where the agent program is kept

	mu         sync.Mutex
	auto       bool
	token      string
	repo       string
	manual     bool // "Обновить сейчас": hand out the agent even with auto off
	checking   bool
	checked    time.Time
	latest     *update.Release
	builds     map[string]string // of latest: the build of each program (nil = unknown)
	err        string
	status     string
	agent      *agentFile
	rolledBack *update.Rollback // the last update of the receiver that was undone
	wake       chan bool        // a check asked on the page; true = also install
}

func newUpdater(s *Server) *updater {
	u := &updater{
		s: s, dir: filepath.Join(s.cfg.DataDir, "update"),
		auto: bool(s.cfg.AutoUpdate), token: s.cfg.UpdateToken, repo: s.cfg.UpdateRepo,
		wake: make(chan bool, 1),
	}
	u.agent = u.findAgentFile()
	return u
}

// agentName is the file name of a downloaded agent version.
func agentName(version string) string { return "asiair-agent-" + version + ".exe" }

// findAgentFile picks up the newest agent program downloaded earlier.
func (u *updater) findAgentFile() *agentFile {
	matches, _ := filepath.Glob(filepath.Join(u.dir, "asiair-agent-*.exe"))
	var best *agentFile
	for _, m := range matches {
		v := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "asiair-agent-"), ".exe")
		if !update.Valid(v) || (best != nil && !update.Newer(v, best.Version)) {
			continue
		}
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		sum, err := update.FileSHA256(m)
		if err != nil {
			continue
		}
		b, _ := os.ReadFile(m + ".build")
		best = &agentFile{Version: v, Path: m, Size: st.Size(), SHA256: sum, Build: strings.TrimSpace(string(b))}
	}
	return best
}

func (u *updater) client() *update.Client {
	u.mu.Lock()
	defer u.mu.Unlock()
	return &update.Client{Repo: u.repo, Token: u.token, API: u.s.UpdateAPI}
}

// canSelfUpdate: release programs are Windows .exe files, and a hand-made
// build ("dev") is never replaced.
func (u *updater) canSelfUpdate() bool {
	return runtime.GOOS == "windows" && runtime.GOARCH == "amd64" && update.Valid(protocol.Version) && u.s.Restart != nil
}

func (u *updater) run(ctx context.Context) {
	if exe, err := update.Executable(); err == nil {
		if rb := update.ReadRollback(exe); rb != nil && rb.From == protocol.Version {
			msg := fmt.Sprintf("обновление приёмника до %s не удалось (%s), %s возвращена версия %s; эта версия больше не ставится автоматически",
				rb.Version, rb.Reason, rb.Time.Local().Format("02.01 15:04"), rb.From)
			u.mu.Lock()
			u.rolledBack, u.err = rb, msg
			u.mu.Unlock()
			log.Printf("Обновление: %s", msg)
		}
		// the server is up: tell the watchdog of an update that it worked
		if update.Started(exe, protocol.Version) {
			update.Healthy(exe, protocol.Version)
			log.Printf("Обновление: версия %s запущена, старая копия больше не нужна", protocol.Version)
		}
		update.Cleanup(exe)
		// the old copy may still be running for a moment after a restart
		time.AfterFunc(time.Minute, func() { update.Cleanup(exe) })
	}
	t := time.NewTimer(updateFirstCheck)
	defer t.Stop()
	for {
		asked, install := false, false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case install = <-u.wake:
			asked = true
		}
		u.mu.Lock()
		auto := u.auto
		u.mu.Unlock()
		if auto || asked {
			u.check(ctx, auto || install, install)
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(updateInterval)
	}
}

// trigger asks the loop for a check now; install also updates with the
// automatic updates turned off.
func (u *updater) trigger(install bool) {
	u.mu.Lock()
	u.checking = true
	if install {
		u.manual = true
	}
	u.mu.Unlock()
	select {
	case u.wake <- install:
	default:
	}
}

func (u *updater) setStatus(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	u.mu.Lock()
	u.status = msg
	u.mu.Unlock()
	if msg != "" {
		log.Printf("Обновление: %s", msg)
	}
}

// check asks GitHub for the latest release and downloads the agent program
// for the agent; with install, a newer receiver is installed and restarted.
func (u *updater) check(ctx context.Context, install, asked bool) {
	u.mu.Lock()
	u.checking, u.status = true, "" // the page shows "checking" until a new status
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = false
		u.checked = time.Now()
		u.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	c := u.client()
	rel, err := c.Latest(ctx)
	u.mu.Lock()
	if err != nil {
		u.err = err.Error()
	} else {
		u.err, u.latest = "", rel
	}
	u.mu.Unlock()
	if err != nil {
		log.Printf("Обновление: %v", err)
		return
	}

	builds, err := c.Builds(ctx, rel)
	if err != nil {
		log.Printf("Обновление: %v", err)
	}
	u.mu.Lock()
	u.builds = builds
	have := u.agent
	u.mu.Unlock()
	if a, ok := rel.Assets[update.AgentAsset]; ok && (have == nil || update.Newer(rel.Version, have.Version)) {
		u.setStatus("скачивается агент %s", rel.Version)
		c.Progress = u.progress("агент " + rel.Version)
		dst := filepath.Join(u.dir, agentName(rel.Version))
		sum, err := c.Download(ctx, a, dst)
		if err != nil {
			u.fail("агент %s не скачан: %v", rel.Version, err)
			return
		}
		st, _ := os.Stat(dst)
		u.mu.Lock()
		old := u.agent
		u.agent = &agentFile{Version: rel.Version, Path: dst, Size: st.Size(), SHA256: sum, Build: builds[update.AgentAsset]}
		u.mu.Unlock()
		if b := builds[update.AgentAsset]; b != "" {
			os.WriteFile(dst+".build", []byte(b), 0o644)
		}
		if old != nil && old.Path != dst {
			os.Remove(old.Path)
			os.Remove(old.Path + ".build")
		}
		u.setStatus("агент %s скачан, агент обновится при следующей связи", rel.Version)
	}

	if !update.Newer(rel.Version, protocol.Version) {
		return
	}
	if update.Unchanged(builds, update.ReceiverAsset, protocol.Build) {
		// the release changed other programs only: nothing to download or restart
		u.setStatus("вышла версия %s, но в приёмнике изменений нет — обновлять его не нужно", rel.Version)
		return
	}
	if !install {
		u.setStatus("вышла версия %s — нажмите «Обновить сейчас»", rel.Version)
		return
	}
	if !u.canSelfUpdate() {
		u.setStatus("вышла версия %s — установите приёмник вручную со страницы релиза", rel.Version)
		return
	}
	u.mu.Lock()
	rb := u.rolledBack
	u.mu.Unlock()
	if rb != nil && rb.Version == rel.Version && !asked {
		u.setStatus("версия %s уже не заработала и была откачена — установите её кнопкой «Обновить сейчас», если нужно", rel.Version)
		return
	}
	a, ok := rel.Assets[update.ReceiverAsset]
	if !ok {
		u.fail("в релизе %s нет файла %s", rel.Version, update.ReceiverAsset)
		return
	}
	exe, err := update.Executable()
	if err != nil {
		u.fail("%v", err)
		return
	}
	u.setStatus("скачивается приёмник %s", rel.Version)
	c.Progress = u.progress("приёмник " + rel.Version)
	if _, err := c.Download(ctx, a, exe+".new"); err != nil {
		u.fail("приёмник %s не скачан: %v", rel.Version, err)
		return
	}
	if err := update.Install(exe, exe+".new"); err != nil {
		os.Remove(exe + ".new")
		u.fail("%v", err)
		return
	}
	d := healthDeadline
	if svc.IsService() {
		d = healthDeadlineService
	}
	if err := update.StartWatch(exe, update.Pending{
		From: protocol.Version, To: rel.Version, Deadline: time.Now().Add(d),
		Service: svc.IsService(), Args: os.Args[1:],
	}); err != nil {
		log.Printf("Обновление: страховка (откат) не запущена: %v", err)
	}
	u.setStatus("установлен приёмник %s, перезапуск…", rel.Version)
	if err := u.s.Restart(); err != nil {
		u.fail("новая версия установлена, но перезапуск не удался: %v — перезапустите приёмник вручную", err)
	}
}

// progress shows how much of a download has arrived on the page (not in
// the log).
func (u *updater) progress(what string) func(done, total int64) {
	return func(done, total int64) {
		msg := fmt.Sprintf("скачивается %s: %.1f МБ", what, float64(done)/(1<<20))
		if total > 0 {
			msg = fmt.Sprintf("скачивается %s: %d%% (%.1f из %.1f МБ)", what, done*100/total, float64(done)/(1<<20), float64(total)/(1<<20))
		}
		u.mu.Lock()
		u.status = msg
		u.mu.Unlock()
	}
}

func (u *updater) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	u.mu.Lock()
	u.err, u.status = msg, ""
	u.mu.Unlock()
	log.Printf("Обновление: %s", msg)
}

// offer returns the agent program to hand out to an agent of the given
// version and platform, or nil.
func (u *updater) offer(st protocol.AgentStatus) *protocol.UpdateOffer {
	u.mu.Lock()
	defer u.mu.Unlock()
	a := u.agent
	if a == nil || !(u.auto || u.manual) || st.OS != agentPlatform || !update.Newer(a.Version, st.Version) {
		return nil
	}
	if a.Build != "" && a.Build == st.Build {
		return nil // the same code under a new number: the agent stays as it is
	}
	return &protocol.UpdateOffer{Version: a.Version, Size: a.Size, SHA256: a.SHA256, Build: a.Build}
}

// ---------- API for the agent ----------

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	s.updater.mu.Lock()
	a := s.updater.agent
	s.updater.mu.Unlock()
	if a == nil {
		http.Error(w, "обновления нет", http.StatusNotFound)
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Обновление: агент скачивает версию %s", a.Version)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, update.AgentAsset, st.ModTime(), f)
}

// ---------- web page ----------

type updateInfo struct {
	Version       string    `json:"version"` // this receiver
	Latest        string    `json:"latest"`
	LatestURL     string    `json:"latest_url"`
	Checked       time.Time `json:"checked"`
	Checking      bool      `json:"checking"`
	Error         string    `json:"error"`
	Status        string    `json:"status"`
	Auto          bool      `json:"auto"`
	TokenSet      bool      `json:"token_set"`
	Agent         string    `json:"agent"` // agent version kept for the agent
	CanSelfUpdate bool      `json:"can_self_update"`
	// ReceiverSame, AgentSame: the latest release has no changes for the
	// receiver (for the agent that reported its build).
	ReceiverSame bool `json:"receiver_same,omitempty"`
	AgentSame    bool `json:"agent_same,omitempty"`
}

func (u *updater) info() updateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	in := updateInfo{
		Version: protocol.Version, Checked: u.checked, Checking: u.checking, Error: u.err, Status: u.status,
		Auto: u.auto, TokenSet: u.token != "", CanSelfUpdate: u.canSelfUpdate(),
	}
	if u.latest != nil {
		in.Latest, in.LatestURL = u.latest.Version, u.latest.URL
	}
	if u.agent != nil {
		in.Agent = u.agent.Version
	}
	in.ReceiverSame = update.Unchanged(u.builds, update.ReceiverAsset, protocol.Build)
	in.AgentSame = update.Unchanged(u.builds, update.AgentAsset, u.s.state.AgentBuild())
	return in
}

func (s *Server) handleUIUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string  `json:"action"` // "check", "install" or "settings"
		Auto   *bool   `json:"auto"`
		Token  *string `json:"token"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	u := s.updater
	switch req.Action {
	case "check":
		u.trigger(false)
	case "install":
		u.trigger(true)
	case "settings":
		if isRemote(r) {
			http.Error(w, "доступно только на компьютере с приёмником", http.StatusForbidden)
			return
		}
		values := map[string]string{}
		if req.Auto != nil {
			values["auto_update"] = fmt.Sprint(*req.Auto)
		}
		if req.Token != nil {
			values["update_token"] = strings.TrimSpace(*req.Token)
		}
		if s.cfg.Path != "" && len(values) > 0 {
			if err := config.UpdateReceiverFile(s.cfg.Path, values); err != nil {
				http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		u.mu.Lock()
		if req.Auto != nil {
			u.auto = *req.Auto
		}
		if req.Token != nil {
			u.token = strings.TrimSpace(*req.Token)
		}
		auto, tokenSet := u.auto, u.token != ""
		u.mu.Unlock()
		log.Printf("Обновление: автоматически — %v, токен GitHub %s", map[bool]string{true: "да", false: "нет"}[auto],
			map[bool]string{true: "задан", false: "не задан"}[tokenSet])
		if req.Token != nil || (req.Auto != nil && *req.Auto) {
			u.trigger(false)
		}
	default:
		http.Error(w, "неизвестное действие", http.StatusBadRequest)
		return
	}
	writeJSON(w, u.info())
}
