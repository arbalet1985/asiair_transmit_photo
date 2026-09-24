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
	"strings"
	"time"
)

// Version of the programs; release builds set it from the tag
// (-ldflags "-X .../internal/protocol.Version=1.2.0").
var Version = "1.0.0"

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
	Version    string    `json:"version"`
	ASIAIR     string    `json:"asiair"`
	Connected  bool      `json:"connected"`
	LastScan   time.Time `json:"last_scan"`
	LastError  string    `json:"last_error,omitempty"`
	Uploading  string    `json:"uploading,omitempty"`
	UploadDone int64     `json:"upload_done"`
	UploadSize int64     `json:"upload_size"`
	Speed      float64   `json:"speed"`            // bytes/s, measured
	Rate       int64     `json:"rate"`             // bytes/s, limit in effect (0 = unlimited)
	QueueFiles int       `json:"queue_files"`      // files waiting to be sent
	QueueBytes int64     `json:"queue_bytes"`      // bytes waiting to be sent
	SentFiles  int       `json:"sent_files"`       // files sent since start
	SentBytes  int64     `json:"sent_bytes"`       // bytes sent since start
	Paused     bool      `json:"paused,omitempty"` // transfer paused (from either side)

	// Where the pause is set: in the agent window and/or on the receiver page.
	LocalPause  bool `json:"local_pause,omitempty"`
	RemotePause bool `json:"remote_pause,omitempty"`
	WaitStart   bool `json:"wait_start,omitempty"` // waiting for "Старт" on the receiver page
	// DiskFull: paused because the receiver is short of disk space (why).
	DiskFull string `json:"disk_full,omitempty"`

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
}

// SyncResponse carries pending commands and the rate limit set on the receiver.
type SyncResponse struct {
	Commands []Command `json:"commands"`
	// MaxRate in bytes/s; nil = use the agent's own setting, 0 = unlimited.
	MaxRate *int64 `json:"max_rate"`
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
}

// Command types.
const CmdDelete = "delete"

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
