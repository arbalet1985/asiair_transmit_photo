package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// The logs sent to the receiver when its page asks: the agent log of the
// last days (today's and the rotated ones, also .gz) and the log of the
// update watchdog. The settings file is not sent: it holds the token.
const (
	logsMaxDays  = 14
	logsMaxBytes = 64 << 20 // of the archive
)

// startLogs sends the logs asked by the receiver, once per ID.
func (a *Agent) startLogs(ctx context.Context, r *protocol.LogsRequest) {
	if r == nil {
		return
	}
	a.mu.Lock()
	if cur := a.status.Logs; cur != nil && (cur.ID == r.ID || cur.Running) {
		a.mu.Unlock()
		return
	}
	a.status.Logs = &protocol.LogsResult{ID: r.ID, Running: true}
	a.mu.Unlock()
	go func() {
		res := protocol.LogsResult{ID: r.ID}
		name, n, err := a.sendLogs(ctx, r)
		if err != nil {
			res.Error = err.Error()
			log.Printf("Агент: журналы не отправлены: %v", err)
		} else {
			res.Name, res.Files = name, n
			log.Printf("Агент: журналы отправлены на приёмник (%d файл(ов))", n)
		}
		res.Done = time.Now()
		a.mu.Lock()
		a.status.Logs = &res
		a.mu.Unlock()
		a.kickSync()
	}()
}

// logFiles lists the agent logs of the last days, oldest first, and the
// log of the update watchdog.
func (a *Agent) logFiles(days int) []string {
	days = min(max(days, 1), logsMaxDays)
	main := a.cfg.LogFile
	if main == "" {
		main = svc.DefaultLogFile("asiair-agent")
	}
	files := svc.LogFiles(main, svc.LogsSince(days))
	if exe, err := a.exe(); err == nil {
		if _, err := os.Stat(update.WatchLog(exe)); err == nil {
			files = append(files, update.WatchLog(exe))
		}
	}
	return files
}

// sendLogs packs the logs into a zip and posts it to the receiver.
func (a *Agent) sendLogs(ctx context.Context, r *protocol.LogsRequest) (name string, n int, err error) {
	files := a.logFiles(r.Days)
	if len(files) == 0 {
		return "", 0, errors.New("журналы не найдены")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range files {
		if buf.Len() > logsMaxBytes {
			break
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		info, _ := f.Stat()
		h := &zip.FileHeader{Name: filepath.Base(p), Method: zip.Deflate}
		if info != nil {
			h.Modified = info.ModTime()
		}
		if strings.HasSuffix(p, ".gz") {
			h.Method = zip.Store // already packed
		}
		w, err := zw.CreateHeader(h)
		if err == nil {
			// the current log grows while it is read: take what is there now
			size := int64(logsMaxBytes)
			if info != nil {
				size = info.Size()
			}
			_, err = io.Copy(w, io.LimitReader(f, size))
		}
		f.Close()
		if err != nil {
			return "", 0, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		n++
	}
	if err := zw.Close(); err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	q := url.Values{"id": {fmt.Sprint(r.ID)}}
	resp, err := a.cl.do(ctx, http.MethodPost, "/api/logs", q, bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("приёмник ответил %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.Name, n, nil
}
