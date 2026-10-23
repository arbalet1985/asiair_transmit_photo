// Package protocol defines the HTTP API between the agent (next to the
// ASIAIR) and the receiver (destination computer). All requests carry
// "Authorization: Bearer <token>".
//
//	HEAD /api/upload?path=P&size=S&mtime=T   -> header Upload-Offset, Upload-Complete
//	PUT  /api/upload?path=P&size=S&mtime=T&offset=O  body: bytes [O, S)  -> UploadResult
//	     (compressed when HEAD offered Upload-Encodings: Content-Encoding)
//	POST /api/commit?path=P&size=S&mtime=T&sha256=H                      -> 200 or 409
//	POST /api/sync      body: SyncRequest   -> SyncResponse
//	POST /api/inventory body: Inventory     -> 204
//	POST /api/result    body: CommandResult -> 204
//	POST /api/results   body: CommandResults -> 204 (when SyncResponse.BatchResults)
//
// P is "<share>/<path inside share>" with forward slashes, T is Unix nanoseconds.
package protocol

import (
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Version of the programs; release builds set it from the tag
// (-ldflags "-X .../internal/protocol.Version=1.2.0"). Programs built by
// hand are "dev" and never update themselves.
var Version = "dev"

// Build identifies the code of the program regardless of the version
// number: a hash of the build made without Version and Build, set by the
// release build (-X .../internal/protocol.Build=…). A new release whose
// build of a program is the same as the running one has nothing new for
// that program, which then does not update. Empty in programs built by hand.
var Build = ""

// Fingerprint returns the SHA-256 fingerprint of a DER certificate as "AB:CD:...".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

const (
	HeaderOffset   = "Upload-Offset"
	HeaderComplete = "Upload-Complete"
	// HeaderEncodings: the compressions of a PUT body the receiver accepts
	// (sent with Content-Encoding; see package wire).
	HeaderEncodings = "Upload-Encodings"
)

// UploadResult is returned by PUT once the whole file has been received.
type UploadResult struct {
	Offset int64  `json:"offset"`
	SHA256 string `json:"sha256,omitempty"` // set when Offset == size
}

// AgentStatus is reported by the agent on every sync.
type AgentStatus struct {
	Version string `json:"version"`
	OS      string `json:"os,omitempty"` // GOOS/GOARCH, e.g. "windows/amd64"
	// Update: what the self-update is doing, e.g. "скачивается 1.2.0" or an error.
	Update    string `json:"update,omitempty"`
	ASIAIR    string `json:"asiair"`
	Connected bool   `json:"connected"`
	// Connection to the receiver: "internet" or "tailscale" (empty in old agents).
	Connection string `json:"connection,omitempty"`
	// ASIAIRError: why the last scan of the ASIAIR failed ("" when it worked).
	ASIAIRError string `json:"asiair_error,omitempty"`
	// Searching: the agent is looking for the ASIAIR in the local network.
	Searching bool      `json:"searching,omitempty"`
	LastScan  time.Time `json:"last_scan"`
	LastError string    `json:"last_error,omitempty"`
	// Uploading, UploadDone, UploadSize: the first of Uploads (older
	// receivers show only one file).
	Uploading  string  `json:"uploading,omitempty"`
	UploadDone int64   `json:"upload_done"`
	UploadSize int64   `json:"upload_size"`
	Speed      float64 `json:"speed"` // bytes/s, measured
	Rate       int64   `json:"rate"`  // bytes/s, limit in effect (0 = unlimited)
	// Uploads: the files being sent now, several at once with Streams > 1.
	Uploads []Upload `json:"uploads,omitempty"`
	// Storage: free space of the ASIAIR storages (eMMC, SD card, USB stick).
	Storage []StorageSpace `json:"storage,omitempty"`
	// Next: the files to be sent next (up to 3), in order; Done is what the
	// receiver already has of an interrupted file.
	Next []Upload `json:"next,omitempty"`
	// SpeedTest: the last speed test asked from the receiver page.
	SpeedTest *SpeedResult `json:"speed_test,omitempty"`
	// Logs: the last logs request from the receiver page.
	Logs *LogsResult `json:"logs,omitempty"`
	// CanLogs: the agent sends its logs when the receiver asks.
	CanLogs bool `json:"can_logs,omitempty"`
	// Streams: how many files the agent sends at once (set on the receiver).
	Streams int `json:"streams,omitempty"`
	// ReadBlock: the size of one read from the ASIAIR in effect, bytes
	// (0 in old agents, which cannot change it).
	ReadBlock int64 `json:"read_block,omitempty"`
	// ReadDepth: reads of one file in flight at once, in effect (0 in old
	// agents).
	ReadDepth int `json:"read_depth,omitempty"`
	// Diag: where the transfer spends its time, to find the bottleneck.
	Diag *Diag `json:"diag,omitempty"`
	// OwnRate is the agent's own limit (max_rate in agent.yaml), bytes/s,
	// 0 = unlimited. The receiver can only lower it. Nil in old agents,
	// where the receiver's limit replaced it.
	OwnRate    *int64 `json:"own_rate,omitempty"`
	QueueFiles int    `json:"queue_files"`      // files waiting to be sent
	QueueBytes int64  `json:"queue_bytes"`      // bytes waiting to be sent
	SentFiles  int    `json:"sent_files"`       // files sent since start
	SentBytes  int64  `json:"sent_bytes"`       // bytes sent since start
	Paused     bool   `json:"paused,omitempty"` // transfer paused (from either side)

	// Where the pause is set: in the agent window and/or on the receiver page.
	LocalPause  bool `json:"local_pause,omitempty"`
	RemotePause bool `json:"remote_pause,omitempty"`
	WaitStart   bool `json:"wait_start,omitempty"` // waiting for "Старт" on the receiver page
	// DiskFull: paused because the receiver is short of disk space (why).
	DiskFull string `json:"disk_full,omitempty"`
	// Schedule: the receiver's schedule window in effect ("" = none).
	Schedule string `json:"schedule,omitempty"`
	// Time zone of the observatory computer: the offset east of UTC in
	// seconds (nil in old agents) and its name ("MSK", "+03").
	TZOffset *int   `json:"tz_offset,omitempty"`
	TZName   string `json:"tz_name,omitempty"`

	// Totals over the files to transfer that are on the ASIAIR now.
	TotalFiles int   `json:"total_files"`
	TotalBytes int64 `json:"total_bytes"`
	DoneFiles  int   `json:"done_files"` // already at the receiver
	DoneBytes  int64 `json:"done_bytes"`

	// Failed lists files waiting for a retry (at most 50).
	Failed []FailedFile `json:"failed,omitempty"`

	// Deleting: the delete commands being carried out (nil when none).
	Deleting *DeleteProgress `json:"deleting,omitempty"`
	// WiFi: the Wi-Fi network the agent's computer is connected to ("" =
	// none or unknown, e.g. a cable).
	WiFi *WiFiInfo `json:"wifi,omitempty"`
	// Build identifies the agent's code (see Build); "" in old agents.
	Build string `json:"build,omitempty"`
	// CanRestart: the agent restarts itself when SyncResponse.Restart asks.
	CanRestart bool `json:"can_restart,omitempty"`
	// Modes: the connections the agent is set up for and can switch to on
	// SyncResponse.Switch (ConnInternet: it knows the internet address;
	// ConnTailscale: it has a Tailscale login). Nil in old agents.
	Modes []string `json:"modes,omitempty"`
	// SwitchBack: the agent switched its connection on the receiver's
	// request and goes back to this one unless it reaches the receiver soon.
	SwitchBack string `json:"switch_back,omitempty"`
	// SwitchDone: the last ConnSwitch ID the agent gave up (it failed or
	// was refused; the events tell why). A switch made restarts the agent
	// with the new Connection instead.
	SwitchDone int64 `json:"switch_done,omitempty"`
}

// Connections of the agent to the receiver (AgentStatus.Connection).
const (
	ConnInternet  = "internet"
	ConnTailscale = "tailscale"
)

// ConnSwitch asks the agent to connect another way.
type ConnSwitch struct {
	ID int64 `json:"id"` // once per ID
	// Connection: ConnInternet or ConnTailscale.
	Connection string `json:"connection"`
	// URLs: receiver addresses for that connection the receiver knows
	// (seen in the agent's requests, or its Tailscale name), tried after
	// the one the agent remembers.
	URLs []string `json:"urls,omitempty"`
}

// DeleteProgress is how far the agent is with the delete commands.
type DeleteProgress struct {
	Total   int    `json:"total"`  // commands taken since the queue was last empty
	Done    int    `json:"done"`   // carried out (deleted or failed)
	Failed  int    `json:"failed"` // of Done, not deleted
	Current string `json:"current,omitempty"`
}

// WiFiInfo is the wireless network the agent's computer uses.
type WiFiInfo struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal,omitempty"` // percent, 0 = unknown
	Iface  string `json:"iface,omitempty"`
}

// AgentEvent is an important step the agent reports before (or right
// after) taking it, so the receiver knows what it was doing if it breaks.
type AgentEvent struct {
	Seq  uint64    `json:"seq"` // growing within one agent run (SyncRequest.Session)
	Time time.Time `json:"time"`
	Text string    `json:"text"`
	// Level: "" information, "warn", "error".
	Level string `json:"level,omitempty"`
}

// FailedFile is a file whose last transfer attempt failed.
type FailedFile struct {
	Path    string    `json:"path"`
	Error   string    `json:"error"`
	RetryAt time.Time `json:"retry_at"`
}

type SyncRequest struct {
	Status AgentStatus `json:"status"`
	// Long polling: with Rev from the previous answer and WaitMS > 0 the
	// receiver holds the request until something for the agent changes
	// (a command, pause, rate, files requested) or WaitMS passes. Receivers
	// that do not know it answer at once without Rev.
	Rev    uint64 `json:"rev,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
	// Session (random per agent run) and Seq (growing per request) let the
	// receiver ignore a status that arrives after a newer one: a request
	// the agent gave up may still reach the receiver late.
	Session string `json:"session,omitempty"`
	Seq     uint64 `json:"seq,omitempty"`
	// Events the receiver has not acknowledged yet (EventsAck), oldest first.
	Events []AgentEvent `json:"events,omitempty"`
}

// SyncResponse carries pending commands and the rate limit set on the receiver.
type SyncResponse struct {
	// Rev identifies the state the answer reflects; sent back in the next
	// SyncRequest. Zero from receivers without long polling.
	Rev      uint64    `json:"rev,omitempty"`
	Commands []Command `json:"commands"`
	// MaxRate in bytes/s; nil or 0 = the agent's own setting. Agents since
	// OwnRate use the lower of the two; older ones use MaxRate instead of
	// their own (0 = unlimited).
	MaxRate *int64 `json:"max_rate"`
	// Streams: how many files to send at once, 1..MaxStreams (0 from old
	// receivers: one).
	Streams int `json:"streams,omitempty"`
	// ReadBlock: the size of one read from the ASIAIR, bytes; 0 = the
	// agent's default (1 MB).
	ReadBlock int64 `json:"read_block,omitempty"`
	// ReadDepth: how many reads of one file are in flight at once; 0 = the
	// agent's default (6).
	ReadDepth int `json:"read_depth,omitempty"`
	// Order: which files to send first: "" oldest (default), "newest",
	// "lights" (Light frames before calibration frames).
	Order string `json:"order,omitempty"`
	// Paused: the transfer is paused from the receiver page.
	Paused bool `json:"paused,omitempty"`
	// DeletePaused: deleting on the ASIAIR is paused from the receiver
	// page. No commands are sent then, and the agent drops the commands it
	// has queued but not started: they come again after the pause.
	DeletePaused bool `json:"delete_paused,omitempty"`
	// SpeedTest: run this speed test (once per ID).
	SpeedTest *SpeedTest `json:"speed_test,omitempty"`
	// Logs: send the logs to the receiver (once per ID).
	Logs *LogsRequest `json:"logs,omitempty"`
	// WaitStart: the transfer has not been started on the receiver page yet
	// (Paused is also set).
	WaitStart bool `json:"wait_start,omitempty"`
	// Fetch lists files requested from the receiver page: they are sent
	// even while the transfer is paused or not started, and first otherwise.
	Fetch []string `json:"fetch,omitempty"`
	// Rediscover: look for the ASIAIR in the network now, asked on the
	// receiver page (once per value; older agents ignore it)
	Rediscover int64 `json:"rediscover,omitempty"`
	// DiskFull: the receiver keeps too little free space and accepts no
	// files until it is freed; the text says why.
	DiskFull string `json:"disk_full,omitempty"`
	// Schedule: the schedule window in effect, e.g. "с 22:00 до 06:00 —
	// пауза" (its pause or limit is already in Paused and MaxRate).
	Schedule string `json:"schedule,omitempty"`
	// Update offers a new agent version; the program is downloaded from
	// /api/update/agent.
	Update *UpdateOffer `json:"update,omitempty"`
	// BatchResults: the receiver takes many results at once on /api/results.
	BatchResults bool `json:"batch_results,omitempty"`
	// EventsAck: the highest SyncRequest.Events Seq of this session stored.
	EventsAck uint64 `json:"events_ack,omitempty"`
	// Restart: restart the agent program, asked on the receiver page (once
	// per value; sent until the agent comes back with a new Session).
	Restart int64 `json:"restart,omitempty"`
	// Switch: connect another way, asked on the receiver page.
	Switch *ConnSwitch `json:"switch,omitempty"`
}

// StorageSpace is the free space of one ASIAIR share.
type StorageSpace struct {
	Share string `json:"share"`
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
}

// Upload is a file being sent.
type Upload struct {
	Path string `json:"path"`
	Done int64  `json:"done"`
	Size int64  `json:"size"`
}

// Diag shows what holds the transfer back, measured over the last seconds
// of sending: the shares of time spent reading from the ASIAIR, waiting for
// the rate limit and pushing the data into the network.
type Diag struct {
	Read    float64 `json:"read"`    // 0..1
	Limit   float64 `json:"limit"`   // 0..1
	Network float64 `json:"network"` // 0..1
	// ReadSpeed: how fast one stream reads from the ASIAIR, bytes/s
	// (0 = not measured: reading never held the transfer back).
	ReadSpeed float64 `json:"read_speed,omitempty"`
	// Bottleneck: "read", "limit", "network" or "" (nothing sent lately).
	Bottleneck string `json:"bottleneck,omitempty"`
	// Path: how the Tailscale traffic goes ("напрямую …" or a relay).
	Path string `json:"path,omitempty"`
	// Text: the summary for people (without Path; "" when nothing was sent
	// lately).
	Text string `json:"text,omitempty"`
	// Shares: reading per ASIAIR storage (eMMC, SD card, USB stick) over
	// the same seconds, to tell a slow card from a slow network.
	Shares []ShareRead `json:"shares,omitempty"`
}

// ShareRead is how fast files of one ASIAIR storage were read while
// sending: bytes/s one stream got while it waited for the ASIAIR; 0 = the
// reading never held the sending back.
type ShareRead struct {
	Share string  `json:"share"`
	Speed float64 `json:"speed,omitempty"`
}

// Speed tests run by the agent when the receiver page asks.
const (
	SpeedTestNetwork = "network" // agent → receiver: raw upload speed
	SpeedTestASIAIR  = "asiair"  // ASIAIR → agent: reading a file of every storage
)

// LogsRequest asks the agent to send its logs of the last Days days (with
// today) to the receiver; ID is new for every request.
type LogsRequest struct {
	ID   int64 `json:"id"`
	Days int   `json:"days"`
}

// LogsResult is how the agent answered the last LogsRequest: Name is the
// archive the receiver stored.
type LogsResult struct {
	ID      int64     `json:"id"`
	Running bool      `json:"running,omitempty"`
	Error   string    `json:"error,omitempty"`
	Done    time.Time `json:"done,omitempty"`
	Name    string    `json:"name,omitempty"`
	Files   int       `json:"files,omitempty"`
}

// SpeedTest asks the agent for a measurement; ID is new for every request.
type SpeedTest struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
}

// SpeedResult is the last measurement of the agent.
type SpeedResult struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Running bool      `json:"running,omitempty"`
	Error   string    `json:"error,omitempty"`
	Done    time.Time `json:"done,omitempty"`
	// network: bytes sent over Streams connections in Seconds
	Bytes   int64   `json:"bytes,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Speed   float64 `json:"speed,omitempty"` // bytes/s
	Streams int     `json:"streams,omitempty"`
	// asiair: one file of every storage read with the current settings
	Shares []ShareSpeed `json:"shares,omitempty"`
}

// ShareSpeed is the reading test of one ASIAIR storage.
type ShareSpeed struct {
	Share   string  `json:"share"`
	File    string  `json:"file,omitempty"`
	Bytes   int64   `json:"bytes,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Speed   float64 `json:"speed,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// MaxStreams limits the files sent at once; DefaultStreams is used until
// the receiver page changes it.
const (
	MaxStreams     = 8
	DefaultStreams = 3
)

// Transfer orders (SyncResponse.Order).
const (
	OrderOldest = ""
	OrderNewest = "newest"
	OrderLights = "lights"
)

// UpdateOffer describes the agent program the receiver can hand out (and,
// at /ui/api/sorter, the sorter program for a sorter connected to it).
type UpdateOffer struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	// Build of the offered program (protocol.Build); "" when unknown. An
	// agent whose own Build is the same skips the offer.
	Build string `json:"build,omitempty"`
}

// Command types.
const (
	CmdDelete = "delete" // Path is a file
	// CmdDeleteDir removes the folder Path with all its files and subfolders.
	// Agents older than 1.11 answer "неизвестная команда".
	CmdDeleteDir = "delete_dir"
)

type Command struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Path string `json:"path"`
}

// CommandResults carries the results of many commands in one request.
type CommandResults struct {
	Results []CommandResult `json:"results"`
}

type CommandResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// File states reported in the inventory.
const (
	StateSent     = "sent"     // at the receiver
	StateQueued   = "queued"   // waiting for its turn
	StateWriting  = "writing"  // still changing on the ASIAIR (not stable yet)
	StateRetry    = "retry"    // last attempt failed, retry later
	StateFiltered = "filtered" // not transferred (extension or excluded folder)
)

// RemoteFile is a file present on the ASIAIR.
type RemoteFile struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Sent    bool      `json:"sent"`
	State   string    `json:"state,omitempty"` // empty from older agents
}

// Inventory is the full list of files on the ASIAIR after a scan.
type Inventory struct {
	Files []RemoteFile `json:"files"`
}

var frameRe = regexp.MustCompile(`(?:^|[^a-z])(light|dark|flat|bias)(?:[^a-z]|$)`)

// FrameKind tells the frame type from the file name ("Light_M31_300.0s_….fit"):
// "light", "dark", "flat", "bias", "darkflat" or "".
func FrameKind(p string) string {
	n := strings.ToLower(path.Base(p))
	if strings.Contains(n, "darkflat") || strings.Contains(n, "flatdark") ||
		strings.Contains(n, "dark_flat") || strings.Contains(n, "flat_dark") {
		return "darkflat"
	}
	if m := frameRe.FindStringSubmatch(n); m != nil {
		return m[1]
	}
	return ""
}
