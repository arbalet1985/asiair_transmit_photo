// Package agent runs next to the ASIAIR: it finds new images, sends them to
// the receiver at a limited rate, and executes delete requests from the receiver.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/ratelimit"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
	"github.com/arbalet1985/asiair_transmit_photo/internal/tsnode"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// sentInfo records a file version that the receiver has confirmed.
type sentInfo struct {
	Size  int64     `json:"size"`
	MTime int64     `json:"mtime"`
	At    time.Time `json:"at"`
}

// seenInfo tracks when a file version was first observed, to detect files
// that are still being written.
type seenInfo struct {
	f     source.File
	since time.Time
}

type Agent struct {
	cfg        *config.Agent
	src        source.Source
	cl         *client
	ts         *tsnode.Node // Tailscale connection to the receiver, or nil
	lim        *ratelimit.Limiter
	localRate  atomic.Int64 // max_rate of agent.yaml, changed from the window
	remoteRate *int64       // guarded by mu; the limit set on the receiver page

	mu       sync.Mutex
	sent     map[string]sentInfo
	seen     map[string]seenInfo
	queue    []source.File
	deleted  map[string]bool // deleted during this run; never upload again
	retryAt  map[string]time.Time
	failures map[string]int
	failErr  map[string]string // last error per failing file
	partial  map[string]int64  // bytes the receiver holds of interrupted files
	fetch    map[string]bool   // requested on the receiver page: sent even while paused
	current  string            // file being uploaded now
	files    []source.File     // files to transfer found by the last scan
	status   protocol.AgentStatus

	// connection to the receiver, for the window
	lastSync time.Time
	syncErr  string

	syncRev     uint64             // state of the receiver seen last (poll loop only)
	syncSession string             // random per run: the receiver orders statuses by it and syncSeq
	syncSeq     uint64             // poll loop only
	uploadSeq   uint64             // guarded by mu
	order       string             // guarded by mu; which files first, set on the receiver
	cancelSync  context.CancelFunc // ends the held sync request; guarded by mu

	active  map[string]*upload // files being sent now; guarded by mu
	streams atomic.Int32       // files sent at once, set on the receiver
	diag    diag

	// pause: no uploads while set; scanning and commands from the receiver go on.
	// It is set from the agent window (local) or from the receiver page (remote).
	paused      atomic.Bool
	synced      atomic.Bool   // the receiver has answered once: its pause/start is known
	localPause  bool          // guarded by mu
	remotePause bool          // guarded by mu
	diskFull    string        // guarded by mu; the receiver has no room: why
	wake        chan struct{} // wakes the transfer loop on pause changes

	// Restart runs the program again after a new version was installed
	// (svc.Restart). Without it the agent does not update itself.
	Restart func() error
	// ConfirmUpdate: a new version is only shown (UpdateAvailable) and is
	// installed when the user asks (InstallUpdate), whatever auto_update says.
	ConfirmUpdate bool
	Exe           string // program to replace; the running one when empty (tests set it)

	// OnConfigChanged is told what the agent itself wrote to agent.yaml
	// (a new ASIAIR address, the receiver certificate), for the window.
	OnConfigChanged func(values map[string]string)

	srcMu        sync.RWMutex // guards src, which a search may replace
	scanErrors   int          // guarded by mu; failed scans in a row
	everScanned  bool         // guarded by mu; the ASIAIR answered at least once
	lastDiscover time.Time
	discovering  atomic.Bool
	rescan       atomic.Bool // scan now, e.g. after a new address was found
	discover     func(context.Context, func(string)) ([]source.Device, error)
	newSource    func(link, user, pass string) (source.Source, error)
	updating     bool                  // guarded by mu
	updateRetry  time.Time             // guarded by mu; after a failed update
	offer        *protocol.UpdateOffer // guarded by mu; newer version offered by the receiver
	failed       string                // guarded by mu; version whose install just failed
	rolledBack   *update.Rollback      // this version was put back after a failed update
	checkUpdate  bool                  // just updated: tell the watchdog once the receiver answers (poll loop)
}

func New(cfg *config.Agent) (*Agent, error) {
	src, err := source.New(cfg.ASIAIR, cfg.Shares, cfg.Username, cfg.Password, cfg.Domain)
	if err != nil {
		return nil, err
	}
	var node *tsnode.Node
	var dial DialFunc
	if cfg.UseTailscale() {
		node, err = tsnode.Start(tsnode.Config{
			Dir:        cfg.TailscaleDir,
			Hostname:   cfg.TailscaleHostname,
			AuthKey:    cfg.TailscaleAuthKey,
			ControlURL: cfg.TailscaleControlURL,
		})
		if err != nil {
			src.Close()
			return nil, err
		}
		dial = node.Dial
	}
	cl, err := newClient(cfg.ReceiverURL, cfg.Token, cfg.ReceiverFingerprint, dial)
	if err != nil {
		if node != nil {
			node.Close()
		}
		return nil, err
	}
	rate, _ := config.ParseRate(cfg.MaxRate)
	a := &Agent{
		cfg: cfg, src: src, cl: cl, ts: node,
		lim:  ratelimit.New(rate),
		sent: map[string]sentInfo{}, seen: map[string]seenInfo{},
		deleted: map[string]bool{}, retryAt: map[string]time.Time{}, failures: map[string]int{}, failErr: map[string]string{},
		partial: map[string]int64{}, fetch: map[string]bool{}, active: map[string]*upload{},
		wake:        make(chan struct{}, 1),
		syncSession: newSession(),
	}
	a.status.Version = protocol.Version
	a.status.OS = runtime.GOOS + "/" + runtime.GOARCH
	a.status.ASIAIR = cfg.ASIAIR
	a.status.Connection = cfg.Connection
	a.status.Rate = rate
	a.localRate.Store(rate)
	a.streams.Store(1) // until the receiver says
	a.initDiscovery()
	cl.onPin = a.pinFingerprint
	if err := a.loadState(); err != nil {
		a.Close()
		return nil, fmt.Errorf("файл состояния %s: %w", cfg.StateFile, err)
	}
	return a, nil
}

// Close releases what New started; Run calls it when it ends.
func (a *Agent) Close() {
	a.source().Close()
	if a.ts != nil {
		a.ts.Close()
	}
}

// Tailscale returns the state of the Tailscale connection (nil when the
// agent connects over the internet).
func (a *Agent) Tailscale() *tsnode.Status {
	if a.ts == nil {
		return nil
	}
	st := a.ts.Status()
	return &st
}

// TailscaleLogin asks for a fresh login link.
func (a *Agent) TailscaleLogin(ctx context.Context) error {
	if a.ts == nil {
		return errors.New("Tailscale не используется")
	}
	return a.ts.Login(ctx)
}

// Dial is the connection used for the receiver: Tailscale or nil (the usual network).
func (a *Agent) Dial() DialFunc {
	if a.ts == nil {
		return nil
	}
	return a.ts.Dial
}

func (a *Agent) loadState() error {
	b, err := os.ReadFile(a.cfg.StateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st struct {
		Sent map[string]sentInfo `json:"sent"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	if st.Sent != nil {
		a.sent = st.Sent
	}
	return nil
}

// saveState writes the list of sent files. Caller holds a.mu.
func (a *Agent) saveState() {
	b, err := json.Marshal(map[string]any{"sent": a.sent})
	if err != nil {
		return
	}
	if dir := filepath.Dir(a.cfg.StateFile); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	tmp := a.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("Агент: не удалось сохранить состояние: %v", err)
		return
	}
	os.Remove(a.cfg.StateFile)
	if err := os.Rename(tmp, a.cfg.StateFile); err != nil {
		log.Printf("Агент: не удалось сохранить состояние: %v", err)
	}
}

// Run works until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	log.Printf("Агент %s: ASIAIR %s → %s, ограничение скорости: %s",
		protocol.Version, a.cfg.ASIAIR, a.cfg.ReceiverURL, config.FormatRate(a.localRate.Load()))
	if a.Restart != nil {
		a.startupUpdate()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.pollLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		a.watchPath(ctx)
	}()
	a.transferLoop(ctx)
	wg.Wait()
	a.Close()
	return nil
}

func (a *Agent) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.status.LastError = ""
	} else {
		a.status.LastError = time.Now().Format("15:04:05 ") + err.Error()
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// SetPaused pauses or resumes the transfer from the agent window. Pausing
// interrupts the file being sent; the receiver keeps the part already
// received, so it is resumed from there later.
func (a *Agent) SetPaused(p bool) {
	a.mu.Lock()
	changed := a.localPause != p
	a.localPause = p
	a.mu.Unlock()
	if changed {
		a.applyPause(p, "в окне агента")
		a.kickSync()
	}
}

// setRemotePaused applies the pause set on the receiver page.
func (a *Agent) setRemotePaused(p, waitStart bool) {
	a.mu.Lock()
	changed := a.remotePause != p
	wasWaiting := a.status.WaitStart
	a.remotePause = p
	a.status.WaitStart = waitStart
	a.mu.Unlock()
	where := "на приёмнике"
	switch {
	case waitStart && !wasWaiting:
		log.Printf("Агент: передача ещё не запущена — ожидание «Старт» на странице приёмника")
		where = ""
	case wasWaiting && !waitStart:
		log.Printf("Агент: передача запущена на приёмнике")
		where = ""
	}
	if changed {
		a.applyPause(p, where)
	}
	// the first answer from the receiver allows uploads
	if !a.synced.Swap(true) {
		a.wakeUp()
	}
}

// applyPause recomputes the pause from both sources; where names the source
// of the change for the log ("" = already logged).
func (a *Agent) applyPause(p bool, where string) {
	a.mu.Lock()
	on := a.localPause || a.remotePause || a.diskFull != ""
	a.status.Paused, a.status.LocalPause, a.status.RemotePause = on, a.localPause, a.remotePause
	a.status.DiskFull = a.diskFull
	var cancels []context.CancelFunc
	for p, u := range a.active {
		if !a.fetch[p] { // requested files are sent even while paused
			cancels = append(cancels, u.cancel)
		}
	}
	a.mu.Unlock()
	switch {
	case where == "":
	case p:
		log.Printf("Агент: пауза %s", where)
	default:
		log.Printf("Агент: пауза %s снята", where)
	}
	if a.paused.Swap(on) == on {
		return
	}
	if on {
		log.Printf("Агент: передача приостановлена")
		for _, cancel := range cancels {
			cancel()
		}
	} else {
		log.Printf("Агент: передача продолжена")
	}
	a.wakeUp()
}

// setDiskFull pauses the transfer while the receiver is short of disk
// space (msg says why) and resumes it when msg is empty.
func (a *Agent) setDiskFull(msg string) {
	a.mu.Lock()
	changed := (a.diskFull == "") != (msg == "")
	a.diskFull = msg
	a.mu.Unlock()
	if !changed {
		return
	}
	if msg != "" {
		log.Printf("Агент: передача остановлена: %s", msg)
	} else {
		log.Printf("Агент: на приёмнике освободилось место")
	}
	a.applyPause(msg != "", "")
}

// setFetch applies the list of files requested on the receiver page.
func (a *Agent) setFetch(paths []string) {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	a.mu.Lock()
	changed := len(m) != len(a.fetch)
	for p := range m {
		if !a.fetch[p] {
			changed = true
			log.Printf("Агент: запрошено скачивание %s", p)
		}
	}
	a.fetch = m
	var cancels []context.CancelFunc
	if a.paused.Load() {
		for p, u := range a.active {
			if !m[p] { // request withdrawn while paused
				cancels = append(cancels, u.cancel)
			}
		}
	}
	a.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if changed {
		a.wakeUp()
	}
}

// wakeUp wakes the transfer loop.
func (a *Agent) wakeUp() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Paused reports whether the transfer is paused.
func (a *Agent) Paused() bool { return a.paused.Load() }

// wait sleeps for d or until the pause state changes.
func (a *Agent) wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-a.wake:
	case <-t.C:
	}
}

// ---------- scanning & uploading ----------

func (a *Agent) transferLoop(ctx context.Context) {
	var lastScan time.Time
	var wg sync.WaitGroup
	defer wg.Wait()
	for ctx.Err() == nil {
		if time.Since(lastScan) >= a.cfg.ScanInterval.D() || a.rescan.Swap(false) {
			if err := a.scan(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("Агент: обход ASIAIR: %v", err)
				a.setError(err)
				a.scanFailed(ctx)
			}
			lastScan = time.Now()
		}
		if a.synced.Load() {
			// start files until all streams are busy; while paused only
			// the files requested on the receiver page go
			for a.activeCount() < int(a.streams.Load()) {
				f, ok := a.next(a.paused.Load())
				if !ok {
					break
				}
				u := a.startUpload(ctx, f)
				wg.Add(1)
				go func() {
					defer wg.Done()
					a.runUpload(ctx, u)
					a.wakeUp() // a stream is free
				}()
			}
		}
		a.wait(ctx, time.Until(lastScan.Add(a.cfg.ScanInterval.D())))
	}
}

// upload is a file being sent.
type upload struct {
	f       source.File
	ctx     context.Context
	cancel  context.CancelFunc // the pause interrupts the upload
	done    atomic.Int64       // bytes the receiver has, with this upload
	started atomic.Bool        // the data is being sent (done is meaningful)
	seq     uint64             // start order
}

func (a *Agent) activeCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.active)
}

func (a *Agent) startUpload(ctx context.Context, f source.File) *upload {
	uctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.uploadSeq++
	u := &upload{f: f, ctx: uctx, cancel: cancel, seq: a.uploadSeq}
	a.active[f.Path] = u
	if a.paused.Load() && !a.fetch[f.Path] { // paused between next and now
		cancel()
	}
	return u
}

// runUpload sends one file and handles the outcome.
func (a *Agent) runUpload(ctx context.Context, u *upload) {
	f := u.f
	err := a.upload(u)
	a.mu.Lock()
	delete(a.active, f.Path)
	a.mu.Unlock()
	u.cancel()
	if err == nil || ctx.Err() != nil {
		return
	}
	if u.ctx.Err() != nil { // interrupted by the pause, not a failure
		log.Printf("Агент: %s: передача прервана паузой, продолжится с того же места", f.Path)
		return
	}
	var he *HTTPError
	if errors.As(err, &he) && he.Code == http.StatusInsufficientStorage {
		msg := he.Msg
		if msg == "" { // answer to HEAD: no body
			msg = "на приёмнике мало места на диске"
		}
		a.setDiskFull(msg) // not the file's fault: no retry penalty
		return
	}
	a.fail(f, err)
}

// setStreams applies the number of streams set on the receiver page.
func (a *Agent) setStreams(n int) {
	if n <= 0 {
		n = 1 // old receiver
	}
	n = min(n, protocol.MaxStreams)
	if old := a.streams.Swap(int32(n)); int(old) != n {
		log.Printf("Агент: одновременно передаётся файлов: %d", n)
		a.wakeUp()
	}
}

// setReadBlock applies the size of one read from the ASIAIR set on the
// receiver page; the files opened from now on read with it.
func (a *Agent) setReadBlock(n int64) {
	old := source.ReadBlock()
	source.SetReadBlock(n)
	if cur := source.ReadBlock(); cur != old {
		log.Printf("Агент: блок чтения с ASIAIR: %s", config.FormatBytes(cur))
	}
}

// setReadDepth applies how many reads of one file are in flight, set on the
// receiver page; the files opened from now on read with it.
func (a *Agent) setReadDepth(n int) {
	old := source.ReadDepth()
	source.SetReadDepth(n)
	if cur := source.ReadDepth(); cur != old {
		log.Printf("Агент: запросов к ASIAIR на файл одновременно: %d", cur)
	}
}

func (a *Agent) wanted(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	if slices.Contains(a.cfg.ExcludeExtensions, ext) {
		return false
	}
	return len(a.cfg.Extensions) == 0 || slices.Contains(a.cfg.Extensions, ext)
}

// excluded reports whether a file lies in an excluded folder (by name or
// by path inside the share).
func (a *Agent) excluded(p string) bool {
	_, rest, _ := strings.Cut(p, "/")
	dirs := strings.Split(path.Dir(rest), "/")
	for _, ex := range a.cfg.Exclude {
		ex = strings.Trim(strings.ReplaceAll(ex, `\`, "/"), "/")
		if ex == "" {
			continue
		}
		for i := range dirs {
			if dirs[i] == "." {
				continue
			}
			if strings.EqualFold(dirs[i], ex) || strings.EqualFold(strings.Join(dirs[:i+1], "/"), ex) {
				return true
			}
		}
	}
	return false
}

func (a *Agent) scan(ctx context.Context) error {
	defer a.updateSpace(ctx)
	var files, filtered []source.File
	// the whole tree is listed so the receiver can show it; filtered files
	// are reported but never transferred
	err := a.source().Walk(ctx, nil, func(f source.File) error {
		if a.wanted(f.Path) && !a.excluded(f.Path) {
			files = append(files, f)
		} else {
			filtered = append(filtered, f)
		}
		return nil
	})
	a.mu.Lock()
	a.status.Connected = err == nil
	if err != nil {
		a.status.ASIAIRError = err.Error()
		a.mu.Unlock()
		return err
	}
	a.status.ASIAIRError = ""
	a.scanErrors, a.everScanned = 0, true
	now := time.Now()
	a.status.LastScan = now
	present := make(map[string]bool, len(files))
	seen := make(map[string]seenInfo, len(files))
	var queue, kept []source.File
	for _, f := range files {
		if a.isDeleted(f.Path) {
			continue // removed while the scan was running
		}
		present[f.Path] = true
		kept = append(kept, f)
		s, ok := a.seen[f.Path]
		if !ok || s.f.Size != f.Size || !s.f.ModTime.Equal(f.ModTime) {
			s = seenInfo{f: f, since: now}
		}
		seen[f.Path] = s
		if !a.isSent(f) && now.Sub(s.since) >= a.cfg.MinFileAge.D() {
			queue = append(queue, f)
		}
	}
	a.seen = seen
	a.files = kept
	// forget files that are gone from the ASIAIR
	changed := false
	for p := range a.sent {
		if !present[p] {
			delete(a.sent, p)
			changed = true
		}
	}
	if changed {
		a.saveState()
	}
	for p := range a.deleted {
		if !present[p] {
			delete(a.deleted, p)
		}
	}
	for p := range a.partial {
		if !present[p] {
			delete(a.partial, p)
		}
	}
	for p := range a.retryAt {
		if !present[p] {
			delete(a.retryAt, p)
			delete(a.failures, p)
			delete(a.failErr, p)
		}
	}
	// oldest first: the night's frames arrive in shooting order
	sort.Slice(queue, func(i, j int) bool {
		if !queue[i].ModTime.Equal(queue[j].ModTime) {
			return queue[i].ModTime.Before(queue[j].ModTime)
		}
		return queue[i].Path < queue[j].Path
	})
	a.queue = queue
	a.updateQueueStatus()
	a.status.LastError = ""
	filtered = slices.DeleteFunc(filtered, func(f source.File) bool { return a.isDeleted(f.Path) })
	inv := a.inventoryLocked(filtered, now)
	a.mu.Unlock()

	if err := a.cl.postJSON(ctx, "/api/inventory", protocol.Inventory{Files: inv}, nil); err != nil {
		log.Printf("Агент: не удалось отправить список файлов: %v", err)
	}
	return nil
}

// inventoryLocked lists the files on the ASIAIR with their state.
// Caller holds a.mu.
func (a *Agent) inventoryLocked(filtered []source.File, now time.Time) []protocol.RemoteFile {
	queued := make(map[string]bool, len(a.queue))
	for _, f := range a.queue {
		queued[f.Path] = true
	}
	inv := make([]protocol.RemoteFile, 0, len(a.files)+len(filtered))
	for _, f := range a.files {
		st := protocol.StateWriting
		switch {
		case a.isSent(f):
			st = protocol.StateSent
		case !a.retryAt[f.Path].IsZero() && now.Before(a.retryAt[f.Path]):
			st = protocol.StateRetry
		case queued[f.Path]:
			st = protocol.StateQueued
		}
		inv = append(inv, protocol.RemoteFile{Path: f.Path, Size: f.Size, ModTime: f.ModTime, Sent: st == protocol.StateSent, State: st})
	}
	for _, f := range filtered {
		inv = append(inv, protocol.RemoteFile{Path: f.Path, Size: f.Size, ModTime: f.ModTime, State: protocol.StateFiltered})
	}
	return inv
}

// updateTotalsLocked recomputes the totals over the files to transfer.
// Caller holds a.mu.
func (a *Agent) updateTotalsLocked() {
	st := &a.status
	st.TotalFiles, st.TotalBytes, st.DoneFiles, st.DoneBytes = 0, 0, 0, 0
	for _, f := range a.files {
		st.TotalFiles++
		st.TotalBytes += f.Size
		if a.isSent(f) {
			st.DoneFiles++
			st.DoneBytes += f.Size
		}
	}
}

// isSent: caller holds a.mu.
func (a *Agent) isSent(f source.File) bool {
	s, ok := a.sent[f.Path]
	return ok && s.Size == f.Size && s.MTime == f.ModTime.UnixNano()
}

// updateQueueStatus: caller holds a.mu.
func (a *Agent) updateQueueStatus() {
	a.status.QueueFiles, a.status.QueueBytes = len(a.queue), 0
	for _, f := range a.queue {
		a.status.QueueBytes += f.Size
	}
	a.updateTotalsLocked()
}

// next returns the next file to upload that is not waiting for a retry.
// Files requested on the receiver page go first; onlyFetch (the transfer is
// paused) returns only them.
func (a *Agent) next(onlyFetch bool) (source.File, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if up := a.upcomingLocked(onlyFetch, 1); len(up) > 0 {
		return up[0], true
	}
	return source.File{}, false
}

// upcomingLocked returns up to n files to upload next, in the order they
// will go: files requested on the receiver page first, then the rest in
// the order set on the receiver (with "lights", calibration frames only
// after the last Light). onlyFetch (the transfer is paused) returns only
// requested files. Files being sent or waiting for a retry are skipped.
// Caller holds a.mu.
func (a *Agent) upcomingLocked(onlyFetch bool, n int) []source.File {
	if a.diskFull != "" {
		return nil // the receiver would refuse even requested files
	}
	now := time.Now()
	var out, later []source.File
	taken := map[string]bool{}
	for pass := 0; pass < 2 && len(out) < n; pass++ {
		if pass == 1 && onlyFetch {
			break
		}
		later = later[:0]
		for i := range a.queue {
			f := a.queue[i]
			if a.order == protocol.OrderNewest {
				f = a.queue[len(a.queue)-1-i] // the queue is oldest first
			}
			if taken[f.Path] || a.active[f.Path] != nil || a.isDeleted(f.Path) || a.isSent(f) || pass == 0 && !a.fetch[f.Path] {
				continue
			}
			if t, ok := a.retryAt[f.Path]; ok && now.Before(t) {
				continue
			}
			if a.order == protocol.OrderLights && FrameKind(f.Path) != "light" {
				if len(later) < n {
					later = append(later, f) // a calibration frame: after the Light frames
				}
				continue
			}
			out, taken[f.Path] = append(out, f), true
			if len(out) == n {
				return out
			}
		}
		for _, f := range later {
			if len(out) == n {
				break
			}
			out, taken[f.Path] = append(out, f), true
		}
	}
	return out
}

// FrameKind tells the frame type from the file name: see protocol.FrameKind.
func FrameKind(p string) string { return protocol.FrameKind(p) }

func (a *Agent) dequeue(p string) {
	q := a.queue[:0]
	for _, f := range a.queue {
		if f.Path != p {
			q = append(q, f)
		}
	}
	a.queue = q
	a.updateQueueStatus()
}

func (a *Agent) fail(f source.File, err error) {
	a.mu.Lock()
	a.failures[f.Path]++
	a.failErr[f.Path] = err.Error()
	n := a.failures[f.Path]
	backoff := min(30*time.Second<<min(n-1, 4), 5*time.Minute) // 30s … 5m
	a.retryAt[f.Path] = time.Now().Add(backoff)
	a.mu.Unlock()
	log.Printf("Агент: %s: %v (повтор через %s)", f.Path, err, backoff)
	a.setError(fmt.Errorf("%s: %w", f.Path, err))
}

func (a *Agent) markSent(f source.File) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent[f.Path] = sentInfo{Size: f.Size, MTime: f.ModTime.UnixNano(), At: time.Now()}
	delete(a.retryAt, f.Path)
	delete(a.failures, f.Path)
	delete(a.failErr, f.Path)
	delete(a.partial, f.Path)
	a.dequeue(f.Path)
	a.saveState()
}

func fileQuery(f source.File) url.Values {
	return url.Values{
		"path":  {f.Path},
		"size":  {strconv.FormatInt(f.Size, 10)},
		"mtime": {strconv.FormatInt(f.ModTime.UnixNano(), 10)},
	}
}

// errRestart asks upload to start over (receiver has a different offset).
var errRestart = errors.New("restart")

func (a *Agent) upload(u *upload) error {
	f := u.f
	err := a.uploadOnce(u)
	if errors.Is(err, errRestart) {
		err = a.uploadOnce(u)
	}
	if errors.Is(err, errRestart) {
		err = errors.New("приёмник сообщает другое смещение, повтор позже")
	}
	if err != nil {
		// remember how much the receiver got, for the overall progress
		if u.started.Load() {
			a.mu.Lock()
			a.partial[f.Path] = u.done.Load()
			a.mu.Unlock()
		}
	}
	return err
}

func (a *Agent) uploadOnce(u *upload) error {
	ctx, f := u.ctx, u.f
	q := fileQuery(f)
	hctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	resp, err := a.cl.do(hctx, http.MethodHead, "/api/upload", q, nil, -1)
	cancel()
	if err != nil {
		return err
	}
	resp.Body.Close()
	if err := httpErr(resp); err != nil {
		return err
	}
	if resp.Header.Get(protocol.HeaderComplete) == "1" {
		log.Printf("Агент: %s уже есть на приёмнике", f.Path)
		a.markSent(f)
		return nil
	}
	offset, err := strconv.ParseInt(resp.Header.Get(protocol.HeaderOffset), 10, 64)
	if err != nil || offset < 0 || offset > f.Size {
		return fmt.Errorf("неверный ответ приёмника: offset %q", resp.Header.Get(protocol.HeaderOffset))
	}

	src, err := a.source().Open(f.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			a.mu.Lock()
			a.dequeue(f.Path)
			a.mu.Unlock()
			return nil
		}
		return fmt.Errorf("чтение с ASIAIR: %w", err)
	}
	defer src.Close()

	// The checksum covers the whole file, so hash the part the receiver
	// already has (read over the local network, not rate limited).
	h := sha256.New()
	if offset > 0 {
		if _, err := io.CopyN(h, src, offset); err != nil {
			return fmt.Errorf("чтение с ASIAIR: %w", err)
		}
		log.Printf("Агент: %s: продолжение с %s из %s", f.Path, config.FormatBytes(offset), config.FormatBytes(f.Size))
	} else {
		log.Printf("Агент: отправка %s (%s)", f.Path, config.FormatBytes(f.Size))
	}

	u.done.Store(offset)
	u.started.Store(true)

	share, _, _ := strings.Cut(f.Path, "/")
	read := &timedSource{r: io.LimitReader(src, f.Size-offset), d: &a.diag, share: share}
	body := &timedBody{d: &a.diag, r: a.lim.Reader(ctx, &progressReader{r: io.TeeReader(read, h), n: &u.done})}
	q.Set("offset", strconv.FormatInt(offset, 10))
	start := time.Now()
	resp, err = a.cl.do(ctx, http.MethodPut, "/api/upload", q, io.NopCloser(body), f.Size-offset)
	if err != nil {
		return err
	}
	var res protocol.UploadResult
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return errRestart
	}
	if err := httpErr(resp); err != nil {
		resp.Body.Close()
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("неверный ответ приёмника: %w", err)
	}
	if res.Offset != f.Size {
		return fmt.Errorf("приёмник получил %d из %d байт", res.Offset, f.Size)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	q.Del("offset")
	q.Set("sha256", sum)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	resp, err = a.cl.do(cctx, http.MethodPost, "/api/commit", q, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := httpErr(resp); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Code == http.StatusConflict {
			return errRestart
		}
		return err
	}
	sent := f.Size - offset
	secs := time.Since(start).Seconds()
	log.Printf("Агент: отправлен %s (%s за %.0f с, %s/s)", f.Path, config.FormatBytes(sent), secs, config.FormatBytes(int64(float64(sent)/max(secs, 0.001))))
	a.mu.Lock()
	a.status.SentFiles++
	a.status.SentBytes += sent
	a.mu.Unlock()
	a.markSent(f)
	return nil
}

type progressReader struct {
	r io.Reader
	n *atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

// ---------- commands from the receiver ----------

func (a *Agent) pollLoop(ctx context.Context) {
	for ctx.Err() == nil {
		held, err := a.poll(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errKicked) {
			continue // local change: report it at once
		}
		a.mu.Lock()
		prev := a.syncErr
		if err != nil {
			a.syncErr = err.Error()
		} else {
			a.syncErr, a.lastSync = "", time.Now()
		}
		a.mu.Unlock()
		if err != nil {
			// log only changes, the agent keeps retrying quietly
			if err.Error() != prev {
				log.Printf("Агент: связь с приёмником: %v", err)
			}
			a.setError(err)
		} else if prev != "" {
			log.Printf("Агент: связь с приёмником восстановлена")
		}
		if err == nil {
			a.updateWorks()
		}
		if err == nil && held {
			// the receiver held the request until now: ask again right away,
			// with a short pause so a busy receiver is not flooded
			sleep(ctx, 100*time.Millisecond)
			continue
		}
		sleep(ctx, a.cfg.PollInterval.D())
	}
}

// errKicked: the held sync request was ended to report a local change.
var errKicked = errors.New("sync restarted")

// kickSync ends a held sync request so the receiver learns a local change
// (e.g. the pause in the agent window) at once.
func (a *Agent) kickSync() {
	a.mu.Lock()
	cancel := a.cancelSync
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Receiver reports the last successful exchange with the receiver and the
// current connection error, if any.
func (a *Agent) Receiver() (last time.Time, errText string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastSync, a.syncErr
}

// Status returns a snapshot of the agent state.
func (a *Agent) Status() protocol.AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.Uploads = a.status.Uploads[:0:0]
	ups := make([]*upload, 0, len(a.active))
	for _, u := range a.active {
		if u.started.Load() {
			ups = append(ups, u)
		}
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].seq < ups[j].seq })
	for _, u := range ups {
		a.status.Uploads = append(a.status.Uploads, protocol.Upload{Path: u.f.Path, Done: u.done.Load(), Size: u.f.Size})
	}
	a.status.Uploading, a.status.UploadDone, a.status.UploadSize = "", 0, 0
	if len(a.status.Uploads) > 0 {
		first := a.status.Uploads[0]
		a.status.Uploading, a.status.UploadDone, a.status.UploadSize = first.Path, first.Done, first.Size
	}
	a.status.Streams = int(a.streams.Load())
	a.status.ReadBlock = source.ReadBlock()
	a.status.ReadDepth = source.ReadDepth()
	a.status.Next = a.status.Next[:0:0]
	for _, f := range a.upcomingLocked(a.paused.Load(), 3) {
		a.status.Next = append(a.status.Next, protocol.Upload{Path: f.Path, Size: f.Size, Done: min(a.partial[f.Path], f.Size)})
	}
	a.status.Diag = a.diag.result(a.status.Streams)
	a.status.Speed = a.lim.Speed()
	a.status.Rate = a.lim.Rate()
	own := a.localRate.Load()
	a.status.OwnRate = &own
	name, off := time.Now().Zone()
	a.status.TZName, a.status.TZOffset = name, &off
	st := a.status
	// overall progress also counts the parts of interrupted files already at
	// the receiver (the file being sent is counted by UploadDone)
	for _, f := range a.files {
		if n := a.partial[f.Path]; n > 0 && a.active[f.Path] == nil && !a.isSent(f) {
			st.DoneBytes += min(n, f.Size)
		}
	}
	st.Failed = nil
	for p, t := range a.retryAt {
		st.Failed = append(st.Failed, protocol.FailedFile{Path: p, Error: a.failErr[p], RetryAt: t})
	}
	sort.Slice(st.Failed, func(i, j int) bool { return st.Failed[i].Path < st.Failed[j].Path })
	if len(st.Failed) > 50 {
		st.Failed = st.Failed[:50]
	}
	return st
}

// poll exchanges the status for commands. held reports that the receiver
// supports long polling, so the next poll can follow at once.
func (a *Agent) poll(ctx context.Context) (held bool, err error) {
	// Register the cancel first, then take the status: a local change made
	// after this point ends the held request (kickSync), one made before it
	// is already in the status sent.
	sctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancelSync = cancel
	a.mu.Unlock()
	st := a.Status()

	// the receiver holds the request until something changes for the agent
	a.syncSeq++
	req := protocol.SyncRequest{Status: st, Rev: a.syncRev, Session: a.syncSession, Seq: a.syncSeq}
	if a.syncRev != 0 {
		req.WaitMS = int(max(a.cfg.PollInterval.D(), time.Second) / time.Millisecond)
	}
	var resp protocol.SyncResponse
	err = a.cl.postJSON(sctx, "/api/sync", req, &resp)
	a.mu.Lock()
	a.cancelSync = nil
	a.mu.Unlock()
	kicked := sctx.Err() != nil && ctx.Err() == nil
	cancel()
	if kicked {
		return false, errKicked
	}
	if err != nil {
		a.syncRev = 0
		return false, err
	}
	a.syncRev = resp.Rev
	held = resp.Rev != 0
	a.mu.Lock()
	a.remoteRate = resp.MaxRate
	if a.status.Schedule != resp.Schedule {
		if resp.Schedule != "" {
			log.Printf("Агент: расписание приёмника: %s", resp.Schedule)
		} else if a.status.Schedule != "" {
			log.Printf("Агент: окно расписания приёмника закончилось")
		}
		a.status.Schedule = resp.Schedule
	}
	if a.order != resp.Order {
		a.order = resp.Order
		log.Printf("Агент: порядок передачи: %s", orderText(resp.Order))
	}
	a.mu.Unlock()
	a.applyRate()
	a.setStreams(resp.Streams)
	a.setReadBlock(resp.ReadBlock)
	a.setReadDepth(resp.ReadDepth)
	a.setFetch(resp.Fetch)
	a.setDiskFull(resp.DiskFull)
	a.maybeUpdate(ctx, resp.Update)
	a.setRemotePaused(resp.Paused, resp.WaitStart)
	a.startSpeedTest(ctx, resp.SpeedTest)
	for _, c := range resp.Commands {
		if ctx.Err() != nil {
			return held, nil
		}
		res := protocol.CommandResult{ID: c.ID, OK: true}
		switch c.Type {
		case protocol.CmdDelete, protocol.CmdDeleteDir:
			if err := a.deletePath(c.Path, c.Type == protocol.CmdDeleteDir); err != nil {
				res.OK, res.Error = false, err.Error()
			}
		default:
			res.OK, res.Error = false, "неизвестная команда "+c.Type
		}
		if err := a.cl.postJSON(ctx, "/api/result", res, nil); err != nil {
			return held, err
		}
	}
	return held, nil
}

// isDeleted reports whether the file, or a folder it lies in, was deleted
// during this run. Caller holds a.mu.
func (a *Agent) isDeleted(p string) bool {
	for len(a.deleted) > 0 && strings.Contains(p, "/") {
		if a.deleted[p] {
			return true
		}
		p = path.Dir(p)
	}
	return false
}

// deletePath removes a file, or with dir a folder with everything in it,
// from the ASIAIR and forgets it.
func (a *Agent) deletePath(p string, dir bool) error {
	if _, _, err := source.SplitPath(p); err != nil {
		return err
	}
	// p itself, or with dir everything under it
	under := func(q string) bool { return q == p || dir && strings.HasPrefix(q, p+"/") }
	what := "удалён"
	if dir {
		what = "удалена папка"
	}
	a.mu.Lock()
	var marked []string
	mark := func(q string) {
		if under(q) && !a.deleted[q] {
			a.deleted[q] = true
			marked = append(marked, q)
		}
	}
	mark(p)
	for _, f := range a.files {
		mark(f.Path)
	}
	a.mu.Unlock()
	var err error
	if dir {
		err = a.source().RemoveAll(p)
	} else {
		err = a.source().Remove(p)
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = nil // already gone
	}
	if err != nil {
		// with a folder some files may be gone already: the next scan tells
		a.mu.Lock()
		for _, q := range marked {
			delete(a.deleted, q)
		}
		a.mu.Unlock()
		log.Printf("Агент: не удалось удалить %s на ASIAIR: %v", p, err)
		return err
	}
	log.Printf("Агент: %s на ASIAIR: %s", what, p)
	a.mu.Lock()
	for q := range a.sent {
		if under(q) {
			delete(a.sent, q)
		}
	}
	for q := range a.seen {
		if under(q) {
			delete(a.seen, q)
		}
	}
	for q := range a.partial {
		if under(q) {
			delete(a.partial, q)
		}
	}
	kept := a.files[:0]
	for _, f := range a.files {
		if under(f.Path) {
			a.dequeue(f.Path)
		} else {
			kept = append(kept, f)
		}
	}
	a.files = kept
	a.dequeue(p)
	a.saveState()
	a.mu.Unlock()
	return nil
}

func newSession() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SetLocalRate changes the agent's own limit (the slider in the window).
func (a *Agent) SetLocalRate(bps int64) {
	a.localRate.Store(bps)
	a.applyRate()
	a.kickSync() // the receiver page shows the new limit at once
}

// applyRate sets the limit in effect: the agent's own one, or the lower
// limit set on the receiver page (which cannot raise it).
func (a *Agent) applyRate() {
	a.mu.Lock()
	rate := effectiveRate(a.localRate.Load(), a.remoteRate)
	a.mu.Unlock()
	if rate != a.lim.Rate() {
		log.Printf("Агент: ограничение скорости: %s", config.FormatRate(rate))
		a.lim.SetRate(rate)
	}
}

func effectiveRate(own int64, remote *int64) int64 {
	if remote != nil && *remote > 0 && (own <= 0 || *remote < own) {
		return *remote
	}
	return own
}

func orderText(o string) string {
	switch o {
	case protocol.OrderNewest:
		return "сначала новые"
	case protocol.OrderLights:
		return "сначала Light, потом калибровочные"
	}
	return "сначала старые"
}

// updateSpace asks the ASIAIR how much room its storages have left.
func (a *Agent) updateSpace(ctx context.Context) {
	sp, ok := a.source().(source.Spacer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out []protocol.StorageSpace
	for _, s := range sp.Space(ctx) {
		out = append(out, protocol.StorageSpace{Share: s.Share, Free: int64(s.Free), Total: int64(s.Total)})
	}
	a.mu.Lock()
	if out != nil || !a.status.Connected {
		a.status.Storage = out
	}
	a.mu.Unlock()
}
