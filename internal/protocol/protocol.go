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

// Version of the programs.
const Version = "1.0.0"

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
	Speed      float64   `json:"speed"`       // bytes/s, measured
	Rate       int64     `json:"rate"`        // bytes/s, limit in effect (0 = unlimited)
	QueueFiles int       `json:"queue_files"` // files waiting to be sent
	QueueBytes int64     `json:"queue_bytes"` // bytes waiting to be sent
	SentFiles  int       `json:"sent_files"`  // files sent since start
	SentBytes  int64     `json:"sent_bytes"`  // bytes sent since start
}

type SyncRequest struct {
	Status AgentStatus `json:"status"`
}

// SyncResponse carries pending commands and the rate limit set on the receiver.
type SyncResponse struct {
	Commands []Command `json:"commands"`
	// MaxRate in bytes/s; nil = use the agent's own setting, 0 = unlimited.
	MaxRate *int64 `json:"max_rate"`
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

// RemoteFile is a file present on the ASIAIR.
type RemoteFile struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Sent    bool      `json:"sent"`
}

// Inventory is the full list of files on the ASIAIR after a scan.
type Inventory struct {
	Files []RemoteFile `json:"files"`
}
