package receiver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// Command statuses.
const (
	StatusPending = "pending"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Command is a request queued for the agent.
type Command struct {
	protocol.Command
	AlsoLocal bool      `json:"also_local"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// IsDir reports a command deleting a whole folder.
func (c *Command) IsDir() bool { return c.Type == protocol.CmdDeleteDir }

// Covers reports whether the file p is deleted by the command: the file
// itself, or any file in the folder and its subfolders.
func (c *Command) Covers(p string) bool {
	return p == c.Path || c.IsDir() && strings.HasPrefix(p, c.Path+"/")
}

// persisted is what survives a restart.
type persisted struct {
	Commands []*Command `json:"commands"`
	MaxRate  *int64     `json:"max_rate"`
	Paused   bool       `json:"paused"` // transfer paused from the page
	// WaitStart: a new receiver shows the ASIAIR tree but does not let the
	// agent transfer until "Старт" is pressed on the page.
	WaitStart bool `json:"wait_start"`
	// Fetch: files requested on the page; the agent sends them even while
	// the transfer is paused or not started.
	Fetch []string `json:"fetch"`
	// SyncDirs: folders synchronised even while the transfer is paused or
	// not started: their new files are added to Fetch as the agent finds them.
	SyncDirs  []string              `json:"sync_dirs"`
	Inventory []protocol.RemoteFile `json:"inventory"`
	InvTime   time.Time             `json:"inventory_time"`
	Status    protocol.AgentStatus  `json:"agent_status"`
	LastSeen  time.Time             `json:"agent_last_seen"`
}

// State holds the command queue, rate setting, and what the agent reported.
type State struct {
	mu   sync.Mutex
	file string
	p    persisted

	// rev changes whenever something the agent must learn changes; changed
	// is closed then, waking the held sync requests.
	rev     uint64
	changed chan struct{}

	session string // agent run and request number of the status kept
	seq     uint64
}

// bump marks a change for the agent. Caller holds s.mu.
func (s *State) bump() {
	s.rev++
	close(s.changed)
	s.changed = make(chan struct{})
}

// Changed returns a channel closed on the next change, or nil when the
// state is already newer than rev.
func (s *State) Changed(rev uint64) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rev != s.rev {
		return nil
	}
	return s.changed
}

func LoadState(file string, defaultRate *int64) (*State, error) {
	// a fresh rev after a restart never matches what an agent saw before
	s := &State{file: file, rev: uint64(time.Now().UnixNano()), changed: make(chan struct{})}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &s.p); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		s.p.MaxRate = defaultRate
		s.p.WaitStart = true
	}
	return s, nil
}

// save writes the state atomically. Caller holds s.mu.
func (s *State) save() error {
	b, err := json.MarshalIndent(&s.p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	os.Remove(s.file)
	return os.Rename(tmp, s.file)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// QueueDelete adds delete commands for paths not already pending: files
// with protocol.CmdDelete, whole folders with protocol.CmdDeleteDir.
func (s *State) QueueDelete(paths []string, typ string, alsoLocal bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := map[protocol.Command]bool{}
	for _, c := range s.p.Commands {
		if c.Status == StatusPending {
			pending[protocol.Command{Type: c.Type, Path: c.Path}] = true
		}
	}
	now := time.Now()
	n := 0
	for _, p := range paths {
		key := protocol.Command{Type: typ, Path: p}
		if pending[key] {
			continue
		}
		pending[key] = true
		s.p.Commands = append(s.p.Commands, &Command{
			Command:   protocol.Command{ID: newID(), Type: typ, Path: p},
			AlsoLocal: alsoLocal, Status: StatusPending, Created: now, Updated: now,
		})
		n++
	}
	if n > 0 {
		s.bump()
	}
	return n, s.save()
}

// Sync records the agent status and returns what the agent should do.
func (s *State) Sync(req protocol.SyncRequest) protocol.SyncResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	// keep the newest status: an older request may be handled after a newer one
	if req.Session == "" || req.Session != s.session || req.Seq >= s.seq {
		s.p.Status = req.Status
		s.session, s.seq = req.Session, req.Seq
	}
	s.p.LastSeen = time.Now()
	resp := protocol.SyncResponse{Rev: s.rev, MaxRate: s.p.MaxRate, Paused: s.p.Paused || s.p.WaitStart, WaitStart: s.p.WaitStart,
		Fetch: append([]string(nil), s.p.Fetch...), Commands: []protocol.Command{}}
	for _, c := range s.p.Commands {
		if c.Status == StatusPending {
			resp.Commands = append(resp.Commands, c.Command)
		}
	}
	return resp
}

// Result applies a command result and returns the command (nil if unknown).
func (s *State) Result(r protocol.CommandResult) (*Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.p.Commands {
		if c.ID != r.ID {
			continue
		}
		c.Updated = time.Now()
		if r.OK {
			c.Status, c.Error = StatusDone, ""
			// drop the file (or the folder's files) from the inventory right away
			gone := func(p string) bool { return c.Covers(p) }
			s.p.Inventory = slices.DeleteFunc(s.p.Inventory, func(f protocol.RemoteFile) bool { return gone(f.Path) })
			s.p.Fetch = slices.DeleteFunc(s.p.Fetch, gone)
			if c.IsDir() {
				s.p.SyncDirs = slices.DeleteFunc(s.p.SyncDirs, func(d string) bool { return d == c.Path || underDir(d, c.Path) })
			}
		} else {
			c.Status, c.Error = StatusFailed, r.Error
		}
		cp := *c
		return &cp, s.save()
	}
	return nil, nil
}

// SetInventory stores the list of files currently on the ASIAIR.
func (s *State) SetInventory(files []protocol.RemoteFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Inventory = files
	s.p.InvTime = time.Now()
	// forget requests for files that are gone or already received
	waiting := map[string]bool{}
	for _, f := range files {
		if f.State != protocol.StateSent && f.State != protocol.StateFiltered {
			waiting[f.Path] = true
		}
	}
	s.p.Fetch = slices.DeleteFunc(s.p.Fetch, func(p string) bool { return !waiting[p] })
	if s.addSyncFetchLocked() > 0 {
		s.bump()
	}
	return s.save()
}

// underDir reports whether the file p lies in the folder dir or below.
func underDir(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }

// addSyncFetchLocked requests the files of the synchronised folders that
// are still to be transferred. Caller holds s.mu.
func (s *State) addSyncFetchLocked() int {
	if len(s.p.SyncDirs) == 0 {
		return 0
	}
	n := 0
	for _, f := range s.p.Inventory {
		if f.State == protocol.StateSent || f.State == protocol.StateFiltered || f.State == "" && f.Sent {
			continue
		}
		if !slices.ContainsFunc(s.p.SyncDirs, func(d string) bool { return underDir(f.Path, d) }) || slices.Contains(s.p.Fetch, f.Path) {
			continue
		}
		s.p.Fetch = append(s.p.Fetch, f.Path)
		n++
	}
	return n
}

// SetSyncDir turns on (or off) the synchronisation of a folder while the
// transfer is paused. Turning it off also withdraws the requests for its
// files not sent yet.
func (s *State) SetSyncDir(dir string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(s.p.SyncDirs, dir)
	switch {
	case on && i < 0:
		s.p.SyncDirs = append(s.p.SyncDirs, dir)
		slices.Sort(s.p.SyncDirs)
		s.addSyncFetchLocked()
	case !on && i >= 0:
		s.p.SyncDirs = slices.Delete(s.p.SyncDirs, i, i+1)
		s.p.Fetch = slices.DeleteFunc(s.p.Fetch, func(p string) bool {
			return underDir(p, dir) && !slices.ContainsFunc(s.p.SyncDirs, func(d string) bool { return underDir(p, d) })
		})
	default:
		return nil
	}
	s.bump()
	return s.save()
}

// RequestFetch adds (on) or removes files requested on the page.
func (s *State) RequestFetch(paths []string, on bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range paths {
		i := slices.Index(s.p.Fetch, p)
		switch {
		case on && i < 0:
			s.p.Fetch = append(s.p.Fetch, p)
			n++
		case !on && i >= 0:
			s.p.Fetch = slices.Delete(s.p.Fetch, i, i+1)
			n++
		}
	}
	if n > 0 {
		s.bump()
	}
	return n, s.save()
}

// FetchDone removes a received file from the requests.
func (s *State) FetchDone(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.Index(s.p.Fetch, p); i >= 0 {
		s.p.Fetch = slices.Delete(s.p.Fetch, i, i+1)
		s.save()
	}
}

// SetRate sets the rate pushed to the agent; nil = agent's own setting.
func (s *State) SetRate(bps *int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.MaxRate = bps
	s.bump()
	return s.save()
}

// SetPaused pauses or resumes the transfer from the page. Resuming also
// starts a transfer that was never started.
func (s *State) SetPaused(p bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Paused = p
	if !p {
		s.p.WaitStart = false
	}
	s.bump()
	return s.save()
}

// ClearFinished removes done/failed commands from the list.
func (s *State) ClearFinished() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmds := s.p.Commands[:0]
	for _, c := range s.p.Commands {
		if c.Status == StatusPending {
			cmds = append(cmds, c)
		}
	}
	s.p.Commands = cmds
	return s.save()
}

// Cancel removes a pending command.
func (s *State) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmds := s.p.Commands[:0]
	for _, c := range s.p.Commands {
		if !(c.ID == id && c.Status == StatusPending) {
			cmds = append(cmds, c)
		}
	}
	s.p.Commands = cmds
	s.bump()
	return s.save()
}

// Snapshot returns a copy of the state for the UI.
func (s *State) Snapshot() persisted {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.p
	cp.Commands = make([]*Command, len(s.p.Commands))
	for i, c := range s.p.Commands {
		cc := *c
		cp.Commands[i] = &cc
	}
	cp.Inventory = append([]protocol.RemoteFile(nil), s.p.Inventory...)
	cp.Fetch = append([]string{}, s.p.Fetch...)
	cp.SyncDirs = append([]string{}, s.p.SyncDirs...)
	return cp
}
