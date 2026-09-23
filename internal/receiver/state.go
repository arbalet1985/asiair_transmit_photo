package receiver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

// persisted is what survives a restart.
type persisted struct {
	Commands  []*Command            `json:"commands"`
	MaxRate   *int64                `json:"max_rate"`
	Paused    bool                  `json:"paused"` // transfer paused from the page
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
}

func LoadState(file string, defaultRate *int64) (*State, error) {
	s := &State{file: file}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &s.p); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		s.p.MaxRate = defaultRate
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

// QueueDelete adds delete commands for paths not already pending.
func (s *State) QueueDelete(paths []string, alsoLocal bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := map[string]bool{}
	for _, c := range s.p.Commands {
		if c.Status == StatusPending {
			pending[c.Path] = true
		}
	}
	now := time.Now()
	n := 0
	for _, p := range paths {
		if pending[p] {
			continue
		}
		pending[p] = true
		s.p.Commands = append(s.p.Commands, &Command{
			Command:   protocol.Command{ID: newID(), Type: protocol.CmdDelete, Path: p},
			AlsoLocal: alsoLocal, Status: StatusPending, Created: now, Updated: now,
		})
		n++
	}
	return n, s.save()
}

// Sync records the agent status and returns what the agent should do.
func (s *State) Sync(st protocol.AgentStatus) protocol.SyncResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Status = st
	s.p.LastSeen = time.Now()
	resp := protocol.SyncResponse{MaxRate: s.p.MaxRate, Paused: s.p.Paused, Commands: []protocol.Command{}}
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
			// drop the file from the inventory right away
			inv := s.p.Inventory[:0]
			for _, f := range s.p.Inventory {
				if f.Path != c.Path {
					inv = append(inv, f)
				}
			}
			s.p.Inventory = inv
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
	return s.save()
}

// SetRate sets the rate pushed to the agent; nil = agent's own setting.
func (s *State) SetRate(bps *int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.MaxRate = bps
	return s.save()
}

// SetPaused pauses or resumes the transfer from the page.
func (s *State) SetPaused(p bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Paused = p
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
	return cp
}
