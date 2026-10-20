package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

// Commands from the receiver (deleting files and folders on the ASIAIR) are
// carried out apart from the sync loop: the agent keeps reporting its
// status while thousands of files are deleted, and the results go back in
// batches instead of one request per file.

const (
	cmdBatch       = 200 // commands carried out before the results are sent
	deleteParallel = 6   // files removed from the ASIAIR at once
)

// cmdRunner holds the commands taken from the receiver. Guarded by its mu.
type cmdRunner struct {
	mu      sync.Mutex
	known   map[string]bool // IDs taken during this run, until the receiver drops them
	queue   []protocol.Command
	running map[string]bool          // being carried out now
	results []protocol.CommandResult // carried out, not yet acknowledged
	batch   bool                     // the receiver accepts /api/results
	prog    protocol.DeleteProgress
	paused  bool // deleting paused on the receiver page
	wake    chan struct{}
}

func newCmdRunner() *cmdRunner {
	return &cmdRunner{known: map[string]bool{}, running: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (r *cmdRunner) wakeUp() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// take queues the commands not seen before; called with every sync answer.
// While deleting is paused the queued commands not started are dropped: the
// receiver keeps them pending and sends them again after the pause.
func (a *Agent) takeCommands(cmds []protocol.Command, batch, paused bool) {
	r := a.cmds
	r.mu.Lock()
	r.batch = batch
	dropped := 0
	if paused && !r.paused && len(r.queue) > 0 {
		dropped = len(r.queue)
		r.prog.Total -= dropped
		r.queue = nil
		if len(r.running) == 0 {
			r.prog = protocol.DeleteProgress{}
		}
	}
	r.paused = paused
	inAnswer := make(map[string]bool, len(cmds))
	added := 0
	for _, c := range cmds {
		inAnswer[c.ID] = true
		if r.known[c.ID] {
			continue
		}
		r.known[c.ID] = true
		r.queue = append(r.queue, c)
		r.prog.Total++
		added++
	}
	// forget the commands the receiver no longer lists, unless still ours
	waiting := map[string]bool{}
	for _, c := range r.queue {
		waiting[c.ID] = true
	}
	for _, res := range r.results {
		waiting[res.ID] = true
	}
	for id := range r.known {
		if !inAnswer[id] && !waiting[id] && !r.running[id] {
			delete(r.known, id)
		}
	}
	queued := len(r.queue)
	r.mu.Unlock()
	if dropped > 0 {
		a.event("", "удаление на ASIAIR на паузе: отложено команд %d", dropped)
	}
	if added > 0 {
		a.event("", "получено команд удаления: %d (в очереди %d)", added, queued)
		r.wakeUp()
	}
}

// deleting is the progress for the status, nil when nothing is to be deleted.
func (r *cmdRunner) deleting() *protocol.DeleteProgress {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 && len(r.running) == 0 {
		return nil
	}
	p := r.prog
	return &p
}

// commandLoop carries out the queued commands until ctx ends. A failure in
// one batch never stops the loop.
func (a *Agent) commandLoop(ctx context.Context) {
	r := a.cmds
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()
	for ctx.Err() == nil {
		// while the ASIAIR is out of reach the commands wait in the queue
		// instead of failing one by one
		up := a.asiairUp()
		r.mu.Lock()
		n := min(len(r.queue), cmdBatch)
		if !up {
			n = 0
		}
		batch := append([]protocol.Command(nil), r.queue[:n]...)
		r.queue = r.queue[n:]
		for _, c := range batch {
			r.running[c.ID] = true
		}
		r.mu.Unlock()
		if len(batch) > 0 {
			a.runBatch(ctx, batch)
			a.flushResults(ctx)
			continue
		}
		a.flushResults(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-retry.C:
		}
	}
}

// runBatch deletes the files and folders of a batch on the ASIAIR and
// records the results.
func (a *Agent) runBatch(ctx context.Context, batch []protocol.Command) {
	r := a.cmds
	files, dirs := 0, 0
	for _, c := range batch {
		if c.Type == protocol.CmdDeleteDir {
			dirs++
		} else {
			files++
		}
	}
	r.mu.Lock()
	first := r.prog.Done == 0
	left := len(r.queue) + len(batch)
	r.mu.Unlock()
	if first {
		a.event("", "начинаю удаление на ASIAIR: %d команд", left)
	}
	log.Printf("Агент: удаление на ASIAIR: файлов %d, папок %d (всего в очереди %d)", files, dirs, left)

	results := make([]protocol.CommandResult, len(batch))
	var valid []int
	for i, c := range batch {
		results[i] = protocol.CommandResult{ID: c.ID, OK: true}
		switch {
		case c.Type != protocol.CmdDelete && c.Type != protocol.CmdDeleteDir:
			results[i].OK, results[i].Error = false, "неизвестная команда "+c.Type
		default:
			if _, _, err := source.SplitPath(c.Path); err != nil {
				results[i].OK, results[i].Error = false, err.Error()
			} else {
				valid = append(valid, i)
			}
		}
	}
	// stop the uploads of what is about to go
	var targets []delTarget
	for _, i := range valid {
		targets = append(targets, delTarget{batch[i].Path, batch[i].Type == protocol.CmdDeleteDir})
	}
	marked := a.markDeleted(targets)

	src := a.source()
	// skipped: not started because deleting was paused meanwhile;
	// deferred: not done because the ASIAIR went out of reach, done later
	skipped := make([]bool, len(batch))
	deferred := make([]bool, len(batch))
	var offMu sync.Mutex
	offline, probed := false, false
	// reachable tells a failed delete from a lost ASIAIR: one quick check
	// per batch, after the first failure
	reachable := func() bool {
		offMu.Lock()
		defer offMu.Unlock()
		if !probed && ctx.Err() == nil {
			probed = true
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := src.Probe(pctx)
			cancel()
			if err != nil {
				offline = true
				a.asiairLost(err)
			}
		}
		return !offline
	}
	remove := func(i int) {
		c := batch[i]
		offMu.Lock()
		off := offline
		offMu.Unlock()
		if off {
			deferred[i] = true
			return
		}
		r.mu.Lock()
		paused := r.paused
		if !paused {
			r.prog.Current = c.Path
		}
		r.mu.Unlock()
		if paused {
			skipped[i] = true
			return
		}
		err := ctx.Err()
		if err == nil {
			err = func() (err error) {
				defer func() {
					if v := recover(); v != nil {
						err = fmt.Errorf("сбой: %v", v)
					}
				}()
				if c.Type == protocol.CmdDeleteDir {
					log.Printf("Агент: удаляется папка %s на ASIAIR…", c.Path)
					return src.RemoveAll(c.Path)
				}
				return src.Remove(c.Path)
			}()
		}
		if errors.Is(err, fs.ErrNotExist) {
			err = nil // already gone
		}
		if err != nil && ctx.Err() == nil && !reachable() {
			deferred[i] = true
			return
		}
		if err != nil {
			results[i].OK, results[i].Error = false, err.Error()
			log.Printf("Агент: не удалось удалить %s на ASIAIR: %v", c.Path, err)
		} else if c.Type == protocol.CmdDeleteDir {
			log.Printf("Агент: удалена папка на ASIAIR: %s", c.Path)
		}
		r.mu.Lock()
		r.prog.Done++
		if err != nil {
			r.prog.Failed++
		}
		r.mu.Unlock()
	}
	// files several at once; then folders one at a time (each is many files)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < deleteParallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				remove(i)
			}
		}()
	}
	for _, i := range valid {
		if batch[i].Type != protocol.CmdDeleteDir {
			jobs <- i
		}
	}
	close(jobs)
	wg.Wait()
	for _, i := range valid {
		if batch[i].Type == protocol.CmdDeleteDir {
			remove(i)
		}
	}
	// commands found invalid count as done too
	r.mu.Lock()
	r.prog.Done += len(batch) - len(valid)
	r.prog.Failed += len(batch) - len(valid)
	r.mu.Unlock()

	var gone, failed []delTarget
	var again []protocol.Command
	nSkipped := 0
	for _, i := range valid {
		t := delTarget{batch[i].Path, batch[i].Type == protocol.CmdDeleteDir}
		if deferred[i] {
			again = append(again, batch[i])
			failed = append(failed, t) // still on the ASIAIR
		} else if skipped[i] {
			nSkipped++
			failed = append(failed, t) // not deleted: uploads may go on
		} else if results[i].OK {
			gone = append(gone, t)
		} else {
			failed = append(failed, t)
		}
	}
	a.forgetDeleted(gone, failed, marked)
	if len(gone) > 0 {
		log.Printf("Агент: удалено на ASIAIR: %d из %d", len(gone), len(batch))
	}

	r.mu.Lock()
	for i, c := range batch {
		delete(r.running, c.ID)
		if deferred[i] {
			continue // back to the queue below
		} else if skipped[i] {
			delete(r.known, c.ID) // taken again after the pause
		} else {
			r.results = append(r.results, results[i])
		}
	}
	r.prog.Total -= nSkipped
	r.prog.Current = ""
	if len(again) > 0 && r.paused {
		// paused meanwhile: dropped like the rest of the queue, sent again after the pause
		for _, c := range again {
			delete(r.known, c.ID)
		}
		r.prog.Total -= len(again)
		again = nil
	}
	r.queue = append(again, r.queue...)
	last := len(r.queue) == 0
	prog := r.prog
	if last {
		r.prog = protocol.DeleteProgress{}
	}
	r.mu.Unlock()
	if len(again) > 0 {
		a.event("warn", "ASIAIR недоступен: удаление отложено до его возвращения (выполнено %d, ждут %d)", prog.Done-prog.Failed, len(again))
	}
	if last {
		level := ""
		if prog.Failed > 0 {
			level = "warn"
		}
		if nSkipped > 0 {
			a.event(level, "удаление на ASIAIR на паузе: выполнено %d, не удалось %d, отложено %d", prog.Done-prog.Failed, prog.Failed, nSkipped)
		} else {
			a.event(level, "удаление на ASIAIR закончено: выполнено %d, не удалось %d", prog.Done-prog.Failed, prog.Failed)
		}
	}
	a.kickSync() // the page shows the progress at once
}

// flushResults sends the results not yet acknowledged by the receiver.
func (a *Agent) flushResults(ctx context.Context) {
	r := a.cmds
	r.mu.Lock()
	res := append([]protocol.CommandResult(nil), r.results...)
	batch := r.batch
	r.mu.Unlock()
	if len(res) == 0 {
		return
	}
	sent := 0
	var err error
	if batch {
		for sent < len(res) && err == nil {
			n := min(len(res)-sent, 500)
			err = a.cl.postJSON(ctx, "/api/results", protocol.CommandResults{Results: res[sent : sent+n]}, nil)
			if err == nil {
				sent += n
			}
		}
	} else {
		for _, x := range res {
			if err = a.cl.postJSON(ctx, "/api/result", x, nil); err != nil {
				break
			}
			sent++
		}
	}
	r.mu.Lock()
	r.results = r.results[sent:]
	r.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		log.Printf("Агент: результаты удаления не отправлены (%d), повтор позже: %v", len(res)-sent, err)
	}
}

// delTarget is a file or (dir) a folder with everything in it.
type delTarget struct {
	path string
	dir  bool
}

// matcher reports whether a path is one of the targets or lies in one of
// the target folders.
func matcher(ts []delTarget) func(string) bool {
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, t := range ts {
		files[t.path] = true
		if t.dir {
			dirs[t.path] = true
		}
	}
	return func(p string) bool {
		if files[p] {
			return true
		}
		if len(dirs) == 0 {
			return false
		}
		for i := strings.LastIndexByte(p, '/'); i > 0; i = strings.LastIndexByte(p[:i], '/') {
			if dirs[p[:i]] {
				return true
			}
		}
		return false
	}
}

// markDeleted stops uploading the targets and returns the paths it marked.
func (a *Agent) markDeleted(ts []delTarget) []string {
	if len(ts) == 0 {
		return nil
	}
	under := matcher(ts)
	a.mu.Lock()
	defer a.mu.Unlock()
	var marked []string
	mark := func(q string) {
		if !a.deleted[q] {
			a.deleted[q] = true
			marked = append(marked, q)
		}
	}
	for _, t := range ts {
		mark(t.path)
	}
	var cancels []context.CancelFunc
	for _, f := range a.files {
		if under(f.Path) {
			mark(f.Path)
			if u := a.active[f.Path]; u != nil {
				cancels = append(cancels, u.cancel)
			}
		}
	}
	for _, c := range cancels {
		c()
	}
	return marked
}

// forgetDeleted drops what was deleted from the agent's lists (one pass,
// one save) and unmarks what failed: the next scan tells what is left.
func (a *Agent) forgetDeleted(gone, failed []delTarget, marked []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(failed) > 0 {
		bad := matcher(failed)
		for _, q := range marked {
			if bad(q) {
				delete(a.deleted, q)
			}
		}
	}
	if len(gone) == 0 {
		return
	}
	under := matcher(gone)
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
		if !under(f.Path) {
			kept = append(kept, f)
		}
	}
	a.files = kept
	q := a.queue[:0]
	for _, f := range a.queue {
		if !under(f.Path) {
			q = append(q, f)
		}
	}
	a.queue = q
	a.updateQueueStatus()
	a.saveState()
}

// asiairUp: the last look at the ASIAIR found it.
func (a *Agent) asiairUp() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status.Connected
}

// asiairLost marks the ASIAIR out of reach until the next scan finds it:
// the receiver holds the delete commands meanwhile.
func (a *Agent) asiairLost(err error) {
	a.mu.Lock()
	was := a.status.Connected
	a.status.Connected = false
	a.status.ASIAIRError = err.Error()
	a.mu.Unlock()
	if was {
		a.event("warn", "ASIAIR перестал отвечать: %v", err)
	}
	a.kickSync()
}
