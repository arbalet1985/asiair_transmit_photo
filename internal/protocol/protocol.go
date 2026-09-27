// Package protocol defines the HTTP API between the agent (next to the
// ASIAIR) and the receiver (destination computer). All requests carry
// "Authorization: Bearer <token>".
//
//	HEAD /api/upload?path=P&size=S&mtime=T   -> header Upload-Offset, Upload-Complete
//	PUT  /api/upload?path=P&size=S&mtime=T&offset=O  body: bytes [O, S)  -> UploadResult
//	POST /api/commit?path=P&size=S&mtime=T&sha256=H                      -> 200 or 409
//	POST /api/sync      body: SyncRequest   -> SyncResponse
//	POST /api/inventory body: Inventory     -> 204
//	POST /api/result    body: CommandResult -> 204
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
	// Streams: how many files the agent sends at once (set on the receiver).
	Streams int `json:"streams,omitempty"`
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
	// Order: which files to send first: "" oldest (default), "newest",
	// "lights" (Light frames before calibration frames).
	Order string `json:"order,omitempty"`
	// Paused: the transfer is paused from the receiver page.
	Paused bool `json:"paused,omitempty"`
	// WaitStart: the transfer has not been started on the receiver page yet
	// (Paused is also set).
	WaitStart bool `json:"wait_start,omitempty"`
	// Fetch lists files requested from the receiver page: they are sent
	// even while the transfer is paused or not started, and first otherwise.
	Fetch []string `json:"fetch,omitempty"`
	// DiskFull: the receiver keeps too little free space and accepts no
	// files until it is freed; the text says why.
	DiskFull string `json:"disk_full,omitempty"`
	// Schedule: the schedule window in effect, e.g. "с 22:00 до 06:00 —
	// пауза" (its pause or limit is already in Paused and MaxRate).
	Schedule string `json:"schedule,omitempty"`
	// Update offers a new agent version; the program is downloaded from
	// /api/update/agent.
	Update *UpdateOffer `json:"update,omitempty"`
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

// UpdateOffer describes the agent program the receiver can hand out.
type UpdateOffer struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
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
