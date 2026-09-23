// Package agent runs next to the ASIAIR: it finds new images, sends them to
// the receiver at a limited rate, and executes delete requests from the receiver.
package agent

import (
	"context"
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
	cfg       *config.Agent
	src       source.Source
	cl        *client
	lim       *ratelimit.Limiter
	localRate int64

	mu       sync.Mutex
	sent     map[string]sentInfo
	seen     map[string]seenInfo
	queue    []source.File
	deleted  map[string]bool // deleted during this run; never upload again
	retryAt  map[string]time.Time
	failures map[string]int
	failErr  map[string]string // last error per failing file
	partial  map[string]int64  // bytes the receiver holds of interrupted files
	files    []source.File     // files to transfer found by the last scan
	status   protocol.AgentStatus

	// connection to the receiver, for the window
	lastSync time.Time
	syncErr  string

	uploadDone atomic.Int64

	// pause: no uploads while set; scanning and commands from the receiver go on.
	// It is set from the agent window (local) or from the receiver page (remote).
	paused       atomic.Bool
	localPause   bool          // guarded by mu
	remotePause  bool          // guarded by mu
	wake         chan struct{} // wakes the transfer loop on pause changes
	cancelUpload context.CancelFunc
}

func New(cfg *config.Agent) (*Agent, error) {
	src, err := source.New(cfg.ASIAIR, cfg.Shares, cfg.Username, cfg.Password, cfg.Domain)
	if err != nil {
		return nil, err
	}
	cl, err := newClient(cfg.ReceiverURL, cfg.Token, cfg.ReceiverFingerprint)
	if err != nil {
		return nil, err
	}
	rate, _ := config.ParseRate(cfg.MaxRate)
	a := &Agent{
		cfg: cfg, src: src, cl: cl,
		lim: ratelimit.New(rate), localRate: rate,
		sent: map[string]sentInfo{}, seen: map[string]seenInfo{},
		deleted: map[string]bool{}, retryAt: map[string]time.Time{}, failures: map[string]int{}, failErr: map[string]string{},
		partial: map[string]int64{},
		wake:    make(chan struct{}, 1),
	}
	a.status.Version = protocol.Version
	a.status.ASIAIR = cfg.ASIAIR
	a.status.Rate = rate
	if err := a.loadState(); err != nil {
		return nil, fmt.Errorf("файл состояния %s: %w", cfg.StateFile, err)
	}
	return a, nil
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
		protocol.Version, a.cfg.ASIAIR, a.cfg.ReceiverURL, config.FormatRate(a.localRate))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.pollLoop(ctx)
	}()
	a.transferLoop(ctx)
	wg.Wait()
	a.src.Close()
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
	}
}

// setRemotePaused applies the pause set on the receiver page.
func (a *Agent) setRemotePaused(p bool) {
	a.mu.Lock()
	changed := a.remotePause != p
	a.remotePause = p
	a.mu.Unlock()
	if changed {
		a.applyPause(p, "на приёмнике")
	}
}

// applyPause recomputes the pause from both sources; where names the source
// of the change for the log.
func (a *Agent) applyPause(p bool, where string) {
	a.mu.Lock()
	on := a.localPause || a.remotePause
	a.status.Paused, a.status.LocalPause, a.status.RemotePause = on, a.localPause, a.remotePause
	cancel := a.cancelUpload
	a.mu.Unlock()
	if p {
		log.Printf("Агент: пауза %s", where)
	} else {
		log.Printf("Агент: пауза %s снята", where)
	}
	if a.paused.Swap(on) == on {
		return
	}
	if on {
		log.Printf("Агент: передача приостановлена")
		if cancel != nil {
			cancel()
		}
	} else {
		log.Printf("Агент: передача продолжена")
	}
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
	for ctx.Err() == nil {
		if time.Since(lastScan) >= a.cfg.ScanInterval.D() {
			if err := a.scan(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("Агент: обход ASIAIR: %v", err)
				a.setError(err)
			}
			lastScan = time.Now()
		}
		if a.paused.Load() {
			a.wait(ctx, time.Until(lastScan.Add(a.cfg.ScanInterval.D())))
			continue
		}
		f, ok := a.next()
		if !ok {
			a.wait(ctx, time.Until(lastScan.Add(a.cfg.ScanInterval.D())))
			continue
		}
		uctx, cancel := context.WithCancel(ctx)
		a.mu.Lock()
		a.cancelUpload = cancel
		a.mu.Unlock()
		if a.paused.Load() { // paused between the check above and now
			cancel()
		}
		err := a.upload(uctx, f)
		a.mu.Lock()
		a.cancelUpload = nil
		a.mu.Unlock()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if uctx.Err() != nil { // interrupted by the pause, not a failure
				a.mu.Lock()
				a.status.Uploading = ""
				a.mu.Unlock()
				log.Printf("Агент: %s: передача прервана паузой, продолжится с того же места", f.Path)
				continue
			}
			a.fail(f, err)
		}
	}
}

func (a *Agent) wanted(p string) bool {
	if len(a.cfg.Extensions) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(p))
	for _, e := range a.cfg.Extensions {
		if ext == e {
			return true
		}
	}
	return false
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
	var files, filtered []source.File
	// the whole tree is listed so the receiver can show it; filtered files
	// are reported but never transferred
	err := a.src.Walk(ctx, nil, func(f source.File) error {
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
		a.mu.Unlock()
		return err
	}
	now := time.Now()
	a.status.LastScan = now
	present := make(map[string]bool, len(files))
	seen := make(map[string]seenInfo, len(files))
	var queue, kept []source.File
	for _, f := range files {
		if a.deleted[f.Path] {
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
func (a *Agent) next() (source.File, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for _, f := range a.queue {
		if a.deleted[f.Path] || a.isSent(f) {
			continue
		}
		if t, ok := a.retryAt[f.Path]; ok && now.Before(t) {
			continue
		}
		return f, true
	}
	return source.File{}, false
}

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
	a.status.Uploading = ""
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
	a.status.Uploading = ""
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

func (a *Agent) upload(ctx context.Context, f source.File) error {
	err := a.uploadOnce(ctx, f)
	if errors.Is(err, errRestart) {
		err = a.uploadOnce(ctx, f)
	}
	if errors.Is(err, errRestart) {
		err = errors.New("приёмник сообщает другое смещение, повтор позже")
	}
	if err != nil {
		// remember how much the receiver got, for the overall progress
		a.mu.Lock()
		if a.status.Uploading == f.Path {
			a.partial[f.Path] = a.uploadDone.Load()
		}
		a.mu.Unlock()
	}
	return err
}

func (a *Agent) uploadOnce(ctx context.Context, f source.File) error {
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

	src, err := a.src.Open(f.Path)
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

	a.mu.Lock()
	a.status.Uploading, a.status.UploadSize = f.Path, f.Size
	a.mu.Unlock()
	a.uploadDone.Store(offset)

	body := a.lim.Reader(ctx, &progressReader{r: io.TeeReader(io.LimitReader(src, f.Size-offset), h), n: &a.uploadDone})
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
		err := a.poll(ctx)
		if ctx.Err() != nil {
			return
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
		sleep(ctx, a.cfg.PollInterval.D())
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
	a.status.UploadDone = a.uploadDone.Load()
	if a.status.Uploading == "" {
		a.status.UploadDone, a.status.UploadSize = 0, 0
	}
	a.status.Speed = a.lim.Speed()
	a.status.Rate = a.lim.Rate()
	st := a.status
	// overall progress also counts the parts of interrupted files already at
	// the receiver (the file being sent is counted by UploadDone)
	for _, f := range a.files {
		if n := a.partial[f.Path]; n > 0 && f.Path != st.Uploading && !a.isSent(f) {
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

func (a *Agent) poll(ctx context.Context) error {
	st := a.Status()

	var resp protocol.SyncResponse
	if err := a.cl.postJSON(ctx, "/api/sync", protocol.SyncRequest{Status: st}, &resp); err != nil {
		return err
	}
	rate := a.localRate
	if resp.MaxRate != nil {
		rate = *resp.MaxRate
	}
	if rate != a.lim.Rate() {
		log.Printf("Агент: ограничение скорости: %s", config.FormatRate(rate))
		a.lim.SetRate(rate)
	}
	a.setRemotePaused(resp.Paused)
	for _, c := range resp.Commands {
		if ctx.Err() != nil {
			return nil
		}
		res := protocol.CommandResult{ID: c.ID, OK: true}
		switch c.Type {
		case protocol.CmdDelete:
			if err := a.deleteFile(c.Path); err != nil {
				res.OK, res.Error = false, err.Error()
			}
		default:
			res.OK, res.Error = false, "неизвестная команда "+c.Type
		}
		if err := a.cl.postJSON(ctx, "/api/result", res, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) deleteFile(p string) error {
	if _, _, err := source.SplitPath(p); err != nil {
		return err
	}
	a.mu.Lock()
	a.deleted[p] = true
	a.mu.Unlock()
	err := a.src.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil // already gone
	}
	if err != nil {
		a.mu.Lock()
		delete(a.deleted, p)
		a.mu.Unlock()
		log.Printf("Агент: не удалось удалить %s на ASIAIR: %v", p, err)
		return err
	}
	log.Printf("Агент: удалён на ASIAIR: %s", p)
	a.mu.Lock()
	delete(a.sent, p)
	delete(a.seen, p)
	kept := a.files[:0]
	for _, f := range a.files {
		if f.Path != p {
			kept = append(kept, f)
		}
	}
	a.files = kept
	delete(a.partial, p)
	a.dequeue(p)
	a.saveState()
	a.mu.Unlock()
	return nil
}
