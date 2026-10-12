package receiver

import (
	"math"
	"path"
	"sort"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// File statuses shown on the web page.
const (
	FileReceived  = "received"   // here, same size as on the ASIAIR
	FileUploading = "uploading"  // being transferred now
	FilePartial   = "partial"    // interrupted, will be resumed
	FileRetry     = "retry"      // last attempt failed, the agent retries later
	FileQueued    = "queued"     // waiting for its turn
	FileWriting   = "writing"    // still being written on the ASIAIR
	FileChanged   = "changed"    // differs from the copy here, will be sent again
	FileLocalOnly = "local-only" // here only (deleted on the ASIAIR)
	FileFiltered  = "filtered"   // not transferred by the agent's settings
	FileDeleting  = "deleting"   // delete command pending
)

type uiFile struct {
	Path       string    `json:"path"`
	Folder     string    `json:"folder"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	MTime      time.Time `json:"mtime"`
	Local      bool      `json:"local"`
	Remote     bool      `json:"remote"`
	RemoteSize int64     `json:"remote_size"`
	Pending    bool      `json:"pending"`
	Status     string    `json:"status"`
	Progress   float64   `json:"progress,omitempty"` // 0..1 for uploading/partial
	Error      string    `json:"error,omitempty"`
	RetryAt    time.Time `json:"retry_at,omitempty"`
	// Light frames received here: stars found and their median HFR in pixels.
	Measured bool    `json:"q,omitempty"`
	HFR      float64 `json:"hfr,omitempty"`
	Stars    int     `json:"stars,omitempty"`
	// the tracks on it and the satellites that left them (SatTold: looked
	// for, with orbits SatAge days from the frame)
	Tracks   []uiTrack `json:"tracks,omitempty"`
	SatTold  bool      `json:"sat_told,omitempty"`
	SatAge   float64   `json:"sat_age,omitempty"`
	SatErr   string    `json:"sat_err,omitempty"`
	FlipRows bool      `json:"flip,omitempty"`
}

// uiTotals summarise the transfer of everything the agent transfers.
type uiTotals struct {
	Files          int     `json:"files"`
	Bytes          int64   `json:"bytes"`
	DoneFiles      int     `json:"done_files"`
	DoneBytes      int64   `json:"done_bytes"`
	RemainingBytes int64   `json:"remaining_bytes"`
	Failed         int     `json:"failed"`
	ETASeconds     float64 `json:"eta_seconds,omitempty"` // while transferring
}

// buildFiles merges the files received here, the ASIAIR inventory, pending
// commands and the agent state into the list for the web page.
func buildFiles(local []LocalFile, inv []protocol.RemoteFile, cmds []*Command,
	agent protocol.AgentStatus, online bool, partials map[string]int64) ([]uiFile, uiTotals) {

	byPath := map[string]*uiFile{}
	var files []*uiFile
	get := func(p string) *uiFile {
		if f, ok := byPath[p]; ok {
			return f
		}
		f := &uiFile{Path: p, Folder: path.Dir(p), Name: path.Base(p)}
		byPath[p] = f
		files = append(files, f)
		return f
	}
	for _, l := range local {
		f := get(l.Path)
		f.Local, f.Size, f.MTime = true, l.Size, l.ModTime
	}
	state := map[string]string{}
	for _, rf := range inv {
		f := get(rf.Path)
		f.Remote, f.RemoteSize = true, rf.Size
		if !f.Local {
			f.Size, f.MTime = rf.Size, rf.ModTime
		}
		st := rf.State
		if st == "" { // older agent
			st = protocol.StateQueued
			if rf.Sent {
				st = protocol.StateSent
			}
		}
		state[rf.Path] = st
	}
	for _, c := range cmds {
		if c.Status != StatusPending {
			continue
		}
		if !c.IsDir() {
			if f, ok := byPath[c.Path]; ok {
				f.Pending = true
			}
			continue
		}
		for _, f := range files {
			if c.Covers(f.Path) {
				f.Pending = true
			}
		}
	}
	failed := map[string]protocol.FailedFile{}
	for _, ff := range agent.Failed {
		failed[ff.Path] = ff
	}
	uploading := map[string]protocol.Upload{}
	if online {
		for _, u := range agent.Uploads {
			uploading[u.Path] = u
		}
		if agent.Uploading != "" && len(agent.Uploads) == 0 { // older agent
			uploading[agent.Uploading] = protocol.Upload{Path: agent.Uploading, Done: agent.UploadDone, Size: agent.UploadSize}
		}
	}

	var t uiTotals
	for _, f := range files {
		ff, isFailed := failed[f.Path]
		switch {
		case f.Pending:
			f.Status = FileDeleting
		case !f.Remote:
			f.Status = FileLocalOnly
		case f.Local && f.Size == f.RemoteSize:
			f.Status = FileReceived
		case uploading[f.Path].Size > 0:
			f.Status = FileUploading
			f.Progress = math.Min(float64(uploading[f.Path].Done)/float64(uploading[f.Path].Size), 1)
		case partials[f.Path] > 0 && f.RemoteSize > 0:
			// interrupted: resumed from here, when the agent retries
			f.Status = FilePartial
			f.Progress = math.Min(float64(partials[f.Path])/float64(f.RemoteSize), 1)
			if isFailed {
				f.Error, f.RetryAt = ff.Error, ff.RetryAt
			}
		case isFailed:
			f.Status, f.Error, f.RetryAt = FileRetry, ff.Error, ff.RetryAt
		case f.Local:
			f.Status = FileChanged
		case state[f.Path] == protocol.StateFiltered:
			f.Status = FileFiltered
		case state[f.Path] == protocol.StateWriting:
			f.Status = FileWriting
		default:
			f.Status = FileQueued
		}

		// totals: what the agent transfers and is still on the ASIAIR
		if !f.Remote || state[f.Path] == protocol.StateFiltered {
			continue
		}
		t.Files++
		t.Bytes += f.RemoteSize
		switch f.Status {
		case FileReceived:
			t.DoneFiles++
			t.DoneBytes += f.RemoteSize
		case FileUploading, FilePartial:
			t.DoneBytes += int64(f.Progress * float64(f.RemoteSize))
		}
		if isFailed && f.Status != FileReceived {
			t.Failed++
		}
		if f.Status == FileDeleting && f.Local && f.Size == f.RemoteSize {
			t.DoneFiles++
			t.DoneBytes += f.RemoteSize
		}
	}
	t.RemainingBytes = t.Bytes - t.DoneBytes
	if len(uploading) > 0 && agent.Speed > 0 && t.RemainingBytes > 0 {
		t.ETASeconds = float64(t.RemainingBytes) / agent.Speed
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := make([]uiFile, len(files))
	for i, f := range files {
		out[i] = *f
	}
	return out, t
}
