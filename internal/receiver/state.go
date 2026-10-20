package receiver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
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
	// Streams: files the agent sends at once; 0 = protocol.DefaultStreams.
	Streams int `json:"streams,omitempty"`
	// ReadBlock: bytes per read from the ASIAIR; 0 = the agent's default.
	ReadBlock int64 `json:"read_block,omitempty"`
	// ReadDepth: reads of one file in flight; 0 = the agent's default.
	ReadDepth int `json:"read_depth,omitempty"`
	// SpeedResults: the last finished speed test of each kind.
	SpeedResults map[string]protocol.SpeedResult `json:"speed_results,omitempty"`
	// Order: which files the agent sends first (OrderOldest, …).
	Order    string           `json:"order,omitempty"`
	Cleanup  CleanupSettings  `json:"cleanup"`
	Schedule ScheduleSettings `json:"schedule"`
	Site     Site             `json:"site"`
	Planner  PlannerState     `json:"planner"`
	Paused   bool             `json:"paused"` // transfer paused from the page
	// DeletePaused: deleting on the ASIAIR paused from the page; the
	// commands stay pending.
	DeletePaused bool `json:"delete_paused,omitempty"`
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
	// Events: the important steps the agent reported, oldest first (at
	// most maxAgentEvents).
	Events []AgentEvent `json:"agent_events,omitempty"`
	// EventSession, EventSeq: the agent run and its last event stored.
	EventSession string `json:"agent_event_session,omitempty"`
	EventSeq     uint64 `json:"agent_event_seq,omitempty"`
}

// AgentEvent is an event of the agent as kept here.
type AgentEvent struct {
	Time     time.Time `json:"time"`
	Text     string    `json:"text"`
	Level    string    `json:"level,omitempty"`
	Received time.Time `json:"received"`
}

const (
	maxAgentEvents  = 500
	maxSyncCommands = 1000 // commands sent to the agent in one answer
)

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

	speedTest *protocol.SpeedTest   // asked on the page, until the agent reports it
	logsReq   *protocol.LogsRequest // likewise
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
	s.storeEventsLocked(req)
	resp := protocol.SyncResponse{BatchResults: true, EventsAck: s.p.EventSeq, Rev: s.rev, MaxRate: s.p.MaxRate, Streams: s.streamsLocked(), ReadBlock: s.p.ReadBlock, ReadDepth: s.p.ReadDepth, Paused: s.p.Paused || s.p.WaitStart, WaitStart: s.p.WaitStart,
		DeletePaused: s.p.DeletePaused, Fetch: append([]string(nil), s.p.Fetch...), Commands: []protocol.Command{}, Order: s.p.Order}
	s.applySchedule(&resp, time.Now())
	if r := s.p.Status.SpeedTest; r != nil && !r.Running && r.ID != s.p.SpeedResults[r.Kind].ID {
		if s.p.SpeedResults == nil {
			s.p.SpeedResults = map[string]protocol.SpeedResult{}
		}
		s.p.SpeedResults[r.Kind] = *r
		s.save()
	}
	if t := s.speedTest; t != nil {
		if r := s.p.Status.SpeedTest; r != nil && r.ID == t.ID && !r.Running {
			s.speedTest = nil // done
		} else {
			resp.SpeedTest = t
		}
	}
	s.logsForSync(&resp)
	// while the agent does not reach the ASIAIR the deletes wait here: they
	// would only fail
	for _, c := range s.p.Commands {
		if s.p.DeletePaused || !s.p.Status.Connected {
			break
		}
		if c.Status == StatusPending && len(resp.Commands) < maxSyncCommands {
			resp.Commands = append(resp.Commands, c.Command)
		}
	}
	return resp
}

// storeEventsLocked keeps the new events of the request. Caller holds s.mu.
func (s *State) storeEventsLocked(req protocol.SyncRequest) {
	if req.Session == "" {
		return
	}
	if req.Session != s.p.EventSession {
		s.p.EventSession, s.p.EventSeq = req.Session, 0
	}
	now := time.Now()
	added := false
	for _, e := range req.Events {
		if e.Seq <= s.p.EventSeq {
			continue
		}
		s.p.EventSeq = e.Seq
		s.p.Events = append(s.p.Events, AgentEvent{Time: e.Time, Text: e.Text, Level: e.Level, Received: now})
		added = true
	}
	if !added {
		return
	}
	if len(s.p.Events) > maxAgentEvents {
		s.p.Events = slices.Clone(s.p.Events[len(s.p.Events)-maxAgentEvents:])
	}
	s.save()
}

// StartSpeedTest asks the agent for a speed test (protocol.SpeedTest*).
func (s *State) StartSpeedTest(kind string) (*protocol.SpeedTest, error) {
	if kind != protocol.SpeedTestNetwork && kind != protocol.SpeedTestASIAIR {
		return nil, fmt.Errorf("неизвестная проверка %q", kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.speedTest != nil || (s.p.Status.SpeedTest != nil && s.p.Status.SpeedTest.Running) {
		return nil, errors.New("проверка уже идёт — дождитесь результата")
	}
	s.speedTest = &protocol.SpeedTest{ID: time.Now().UnixNano(), Kind: kind}
	s.bump()
	return s.speedTest, nil
}

// SpeedTestPending is the test asked on the page that the agent has not
// finished yet.
func (s *State) SpeedTestPending() *protocol.SpeedTest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speedTest
}

// Result applies a command result and returns the command (nil if unknown).
func (s *State) Result(r protocol.CommandResult) (*Command, error) {
	cmds, err := s.Results([]protocol.CommandResult{r})
	if len(cmds) == 0 {
		return nil, err
	}
	return cmds[0], err
}

// Results applies the results of many commands at once (one save) and
// returns the commands they belong to, in order; unknown ones are skipped.
func (s *State) Results(rs []protocol.CommandResult) ([]*Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := make(map[string]*Command, len(s.p.Commands))
	for _, c := range s.p.Commands {
		byID[c.ID] = c
	}
	now := time.Now()
	var out []*Command
	files := map[string]bool{}
	var dirs []string
	for _, r := range rs {
		c := byID[r.ID]
		if c == nil {
			continue
		}
		c.Updated = now
		if r.OK {
			c.Status, c.Error = StatusDone, ""
			files[c.Path] = true
			if c.IsDir() {
				dirs = append(dirs, c.Path)
			}
		} else {
			c.Status, c.Error = StatusFailed, r.Error
		}
		cp := *c
		out = append(out, &cp)
	}
	if len(out) == 0 {
		return nil, nil
	}
	// drop the files (and the folders' files) from the inventory right away
	gone := func(p string) bool {
		if files[p] {
			return true
		}
		for _, d := range dirs {
			if underDir(p, d) {
				return true
			}
		}
		return false
	}
	s.p.Inventory = slices.DeleteFunc(s.p.Inventory, func(f protocol.RemoteFile) bool { return gone(f.Path) })
	s.p.Fetch = slices.DeleteFunc(s.p.Fetch, gone)
	if len(dirs) > 0 {
		s.p.SyncDirs = slices.DeleteFunc(s.p.SyncDirs, gone)
	}
	return out, s.save()
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

func (s *State) streamsLocked() int {
	if s.p.Streams <= 0 {
		return protocol.DefaultStreams
	}
	return min(s.p.Streams, protocol.MaxStreams)
}

// Streams returns how many files the agent sends at once.
func (s *State) Streams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamsLocked()
}

// SetStreams sets how many files the agent sends at once.
func (s *State) SetStreams(n int) error {
	if n < 1 || n > protocol.MaxStreams {
		return fmt.Errorf("число потоков должно быть от 1 до %d", protocol.MaxStreams)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Streams = n
	s.bump()
	return s.save()
}

// SetReadBlock sets the size of one read from the ASIAIR, bytes; 0 = the
// agent's default.
func (s *State) SetReadBlock(n int64) error {
	if n != 0 && (n < source.MinReadBlock || n > source.MaxReadBlock) {
		return fmt.Errorf("размер блока чтения должен быть от %s до %s",
			config.FormatBytes(source.MinReadBlock), config.FormatBytes(source.MaxReadBlock))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.ReadBlock = n
	s.bump()
	return s.save()
}

// SetReadDepth sets how many reads of one file the agent keeps in flight;
// 0 = the agent's default.
func (s *State) SetReadDepth(n int) error {
	if n != 0 && (n < source.MinReadDepth || n > source.MaxReadDepth) {
		return fmt.Errorf("число запросов на файл должно быть от %d до %d", source.MinReadDepth, source.MaxReadDepth)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.ReadDepth = n
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

// SetDeletePaused pauses or resumes deleting on the ASIAIR.
func (s *State) SetDeletePaused(p bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.DeletePaused = p
	s.bump()
	return s.save()
}

// ClearFinished removes done/failed commands from the list.
func (s *State) ClearFinished() error { return s.Clear(nil, "") }

// Clear removes finished commands: those of ids (all when nil) whose status
// is status ("" = done or failed).
func (s *State) Clear(ids []string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := idSet(ids)
	cmds := s.p.Commands[:0]
	for _, c := range s.p.Commands {
		finished := c.Status != StatusPending && (status == "" || c.Status == status)
		if !(finished && (pick == nil || pick[c.ID])) {
			cmds = append(cmds, c)
		}
	}
	s.p.Commands = cmds
	return s.save()
}

func idSet(ids []string) map[string]bool {
	if ids == nil {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// Cancel removes a pending command.
func (s *State) Cancel(id string) error { return s.CancelMany([]string{id}) }

// CancelMany removes the pending commands of ids.
func (s *State) CancelMany(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := idSet(ids)
	cmds := s.p.Commands[:0]
	for _, c := range s.p.Commands {
		if !(pick[c.ID] && c.Status == StatusPending) {
			cmds = append(cmds, c)
		}
	}
	s.p.Commands = cmds
	s.bump()
	return s.save()
}

// Retry queues the failed commands of ids again.
func (s *State) Retry(ids []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pick := idSet(ids)
	n := 0
	now := time.Now()
	for _, c := range s.p.Commands {
		if pick[c.ID] && c.Status == StatusFailed {
			c.Status, c.Error, c.Updated = StatusPending, "", now
			n++
		}
	}
	if n > 0 {
		s.bump()
	}
	return n, s.save()
}

// AgentBuild is the build the agent reported last ("" = unknown).
func (s *State) AgentBuild() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p.Status.Build
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
	cp.Events = append([]AgentEvent(nil), s.p.Events...)
	return cp
}
