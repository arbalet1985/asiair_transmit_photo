package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// Speed tests asked from the receiver page. The network test sends zeros
// to the receiver over as many connections as files are sent at once, for
// a few seconds and without the rate limit; the ASIAIR test reads the
// largest file of every storage with the current read settings. Both run
// beside the transfer, which shares the links with them.
var (
	speedNetDuration  = 8 * time.Second
	speedReadDuration = 6 * time.Second
	speedReadMax      = int64(512 << 20)
)

// startSpeedTest runs the test asked by the receiver, once per ID.
func (a *Agent) startSpeedTest(ctx context.Context, t *protocol.SpeedTest) {
	if t == nil {
		return
	}
	a.mu.Lock()
	if cur := a.status.SpeedTest; cur != nil && (cur.ID == t.ID || cur.Running) {
		a.mu.Unlock()
		return
	}
	a.status.SpeedTest = &protocol.SpeedResult{ID: t.ID, Kind: t.Kind, Running: true}
	a.mu.Unlock()
	go func() {
		res := protocol.SpeedResult{ID: t.ID, Kind: t.Kind}
		switch t.Kind {
		case protocol.SpeedTestNetwork:
			log.Printf("Агент: проверка скорости до приёмника…")
			a.speedNetwork(ctx, &res)
			if res.Error == "" {
				log.Printf("Агент: скорость до приёмника: %s/s (%d соединений)", config.FormatBytes(int64(res.Speed)), res.Streams)
			}
		case protocol.SpeedTestASIAIR:
			log.Printf("Агент: проверка скорости чтения с ASIAIR…")
			a.speedASIAIR(ctx, &res)
			for _, s := range res.Shares {
				if s.Error == "" {
					log.Printf("Агент: чтение с ASIAIR, %s: %s/s", s.Share, config.FormatBytes(int64(s.Speed)))
				}
			}
		default:
			res.Error = "неизвестная проверка " + t.Kind
		}
		if res.Error != "" {
			log.Printf("Агент: проверка скорости: %s", res.Error)
		}
		res.Done = time.Now()
		a.mu.Lock()
		a.status.SpeedTest = &res
		a.mu.Unlock()
		a.kickSync() // the result goes to the receiver now
	}()
}

// zeros yields zero bytes until its deadline.
type zeros struct {
	until time.Time
	n     *atomic.Int64
}

func (z zeros) Read(p []byte) (int, error) {
	if time.Now().After(z.until) {
		return 0, io.EOF
	}
	clear(p)
	z.n.Add(int64(len(p)))
	return len(p), nil
}

func (a *Agent) speedNetwork(ctx context.Context, res *protocol.SpeedResult) {
	streams := max(1, int(a.streams.Load()))
	res.Streams = streams
	ctx, cancel := context.WithTimeout(ctx, speedNetDuration+time.Minute)
	defer cancel()
	start := time.Now()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		received int64
		firstErr error
		sent     atomic.Int64
	)
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := a.speedUpload(ctx, zeros{until: start.Add(speedNetDuration), n: &sent})
			mu.Lock()
			received += n
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	res.Bytes, res.Seconds = received, elapsed
	if firstErr != nil && received == 0 {
		res.Error = "не удалось: " + firstErr.Error()
		return
	}
	res.Speed = float64(received) / elapsed
}

// speedUpload sends body to the receiver, which counts and drops it.
func (a *Agent) speedUpload(ctx context.Context, body io.Reader) (int64, error) {
	resp, err := a.cl.do(ctx, http.MethodPost, "/api/speedtest", nil, body, -1)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, errors.New("приёмник этой версии не умеет проверять скорость")
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("приёмник ответил %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Bytes, nil
}

func (a *Agent) speedASIAIR(ctx context.Context, res *protocol.SpeedResult) {
	// the largest file of every storage found by the last scan
	a.mu.Lock()
	largest := map[string]int{}
	for i, f := range a.files {
		share, _, _ := strings.Cut(f.Path, "/")
		if j, ok := largest[share]; !ok || f.Size > a.files[j].Size {
			largest[share] = i
		}
	}
	type pick struct {
		share, path string
		size        int64
	}
	var picks []pick
	for share, i := range largest {
		picks = append(picks, pick{share, a.files[i].Path, a.files[i].Size})
	}
	a.mu.Unlock()
	sort.Slice(picks, func(i, j int) bool { return picks[i].share < picks[j].share })
	if len(picks) == 0 {
		res.Error = "на ASIAIR не найдено файлов для проверки (дождитесь обхода)"
		return
	}
	for _, p := range picks {
		if ctx.Err() != nil {
			return
		}
		s := protocol.ShareSpeed{Share: p.share, File: p.path}
		s.Bytes, s.Seconds, s.Error = a.readFor(p.path)
		if s.Error == "" && s.Seconds > 0 {
			s.Speed = float64(s.Bytes) / s.Seconds
		}
		res.Shares = append(res.Shares, s)
	}
}

// readFor reads the file from the ASIAIR for a few seconds, dropping the data.
func (a *Agent) readFor(path string) (n int64, secs float64, errText string) {
	f, err := a.source().Open(path)
	if err != nil {
		return 0, 0, err.Error()
	}
	defer f.Close()
	start := time.Now()
	buf := make([]byte, 256<<10)
	for n < speedReadMax && time.Since(start) < speedReadDuration {
		k, err := f.Read(buf)
		n += int64(k)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n, time.Since(start).Seconds(), err.Error()
		}
	}
	return n, time.Since(start).Seconds(), ""
}
