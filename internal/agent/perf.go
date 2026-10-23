package agent

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// perf keeps how long the agent spends on each kind of work and how fast
// it goes, for the perf log (protocol.PerfLogName): a record per try of
// sending a file and a summary per minute. The file goes to the receiver
// with the other logs. Writing it never holds the work back: lines go
// through a channel to perfLoop and are dropped when it is full.

type perfAct int

const (
	actCheck   perfAct = iota // HEAD: what the receiver already has
	actOpen                   // opening the file on the ASIAIR
	actReread                 // reading again the start the receiver has
	actRead                   // reading from the ASIAIR while sending
	actHash                   // SHA-256
	actPreview                // the tray picture of a FITS frame
	actZstd                   // compression
	actLimit                  // waiting for the rate limit
	actNet                    // the network to the receiver (TLS included)
	actCommit                 // the receiver checking the file
	actScan                   // listing the ASIAIR
	actDelete                 // deleting on the ASIAIR
	nActs
)

// perfKeys are the names of the actions in protocol.PerfRecord.
var perfKeys = [nActs]string{
	actCheck: "check", actOpen: "open", actReread: "reread", actRead: "read", actHash: "hash",
	actPreview: "preview", actZstd: "zstd", actLimit: "limit", actNet: "net", actCommit: "commit",
	actScan: "scan", actDelete: "delete",
}

// stepAct: the action is done once per file (or per scan, per batch of deletes):
// they are counted, the others are streams of bytes.
func stepAct(act perfAct) bool {
	switch act {
	case actCheck, actOpen, actCommit, actScan, actDelete:
		return true
	}
	return false
}

// perfCounts are the times, bytes (items) and steps of every action.
type perfCounts struct {
	ns, bytes, n, max [nActs]atomic.Int64
}

func (c *perfCounts) add(act perfAct, bytes int64, d time.Duration, step bool) {
	if d < 0 {
		d = 0
	}
	c.ns[act].Add(int64(d))
	c.bytes[act].Add(bytes)
	if step {
		c.n[act].Add(1)
		for m := c.max[act].Load(); int64(d) > m && !c.max[act].CompareAndSwap(m, int64(d)); m = c.max[act].Load() {
		}
	}
}

// take returns the counts and starts them anew.
func (c *perfCounts) take() (ns, bytes, n, max [nActs]int64) {
	for i := range nActs {
		ns[i], bytes[i] = c.ns[i].Swap(0), c.bytes[i].Swap(0)
		n[i], max[i] = c.n[i].Swap(0), c.max[i].Swap(0)
	}
	return
}

type perf struct {
	minute perfCounts
	files  atomic.Int64 // sent in the minute
	lines  chan string
}

func (p *perf) add(act perfAct, bytes int64, d time.Duration) {
	p.minute.add(act, bytes, d, stepAct(act))
}

func (p *perf) write(r protocol.PerfRecord) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	select {
	case p.lines <- string(b):
	default: // the disk is slow (or no perfLoop): the line is lost, not the work
	}
}

// filePerf counts one try of sending a file, and the minute with it.
type filePerf struct {
	p     *perf
	c     perfCounts
	inner atomic.Int64 // ns of reading, hashing… done inside the body reads
	last  atomic.Int64 // unix ns of the end of the last body read
}

func (p *perf) file() *filePerf { return &filePerf{p: p} }

func (fp *filePerf) add(act perfAct, bytes int64, d time.Duration) {
	fp.c.add(act, bytes, d, stepAct(act))
	fp.p.add(act, bytes, d)
	switch act {
	case actRead, actHash, actPreview, actZstd:
		fp.inner.Add(int64(d))
	}
}

// timedWriter counts the writes to w as act, taken out of the limit time
// of diag like the compression (it is done inside the body reads).
type timedWriter struct {
	w   io.Writer
	fp  *filePerf
	d   *diag
	act perfAct
}

func (t *timedWriter) Write(p []byte) (int, error) {
	start := time.Now()
	n, err := t.w.Write(p)
	d := time.Since(start)
	t.fp.add(t.act, int64(n), d)
	t.d.busy(d)
	return n, err
}

// perfActsOf turns the counts into the actions of a record, leaving out
// the ones not done.
func perfActsOf(ns, bytes, n, max [nActs]int64) map[string]protocol.PerfAct {
	out := map[string]protocol.PerfAct{}
	for i := range nActs {
		if ns[i] == 0 && bytes[i] == 0 && n[i] == 0 {
			continue
		}
		out[perfKeys[i]] = protocol.PerfAct{MS: ms(ns[i]), Bytes: bytes[i], N: n[i], MaxMS: ms(max[i])}
	}
	return out
}

func ms(ns int64) float64 { return math.Round(float64(ns)/1e4) / 100 }

// done writes the record of one try of sending a file: sent bytes of it in
// total time, err when it failed.
func (fp *filePerf) done(path string, sent int64, total time.Duration, streams int, err error) {
	r := protocol.PerfRecord{Time: time.Now(), Kind: "file", Path: path, Bytes: sent, MS: ms(int64(total)),
		Streams: streams, Acts: perfActsOf(fp.c.take())}
	if err != nil {
		r.Error = err.Error()
	} else {
		fp.p.files.Add(1)
	}
	fp.p.write(r)
}

const perfEvery = time.Minute

const perfHeader = "# Журнал скорости агента: по строке JSON на попытку отправить файл и на минуту.\n" +
	"# Расшифровка с графиками — на странице приёмника: «Журналы» → «Скорость по шагам».\n"

func (a *Agent) perfFile() string {
	main := a.cfg.LogFile
	if main == "" {
		main = svc.DefaultLogFile("asiair-agent")
	}
	return filepath.Join(filepath.Dir(main), protocol.PerfLogName)
}

// perfLoop writes the perf records and the summary of every minute.
func (a *Agent) perfLoop(ctx context.Context) {
	path := a.perfFile()
	var f *svc.RotatingLog
	failed := false
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	put := func(line string) {
		if f == nil {
			var err error
			// rotated and kept like the agent log
			if f, err = svc.OpenLog(path); err != nil {
				f = nil
				if !failed {
					failed = true
					log.Printf("Агент: журнал скорости %s не открылся: %v", path, err)
				}
				return
			}
			f.OnRotate = func(nf *os.File) { nf.WriteString(perfHeader) }
			if info, err := f.File().Stat(); err == nil && info.Size() == 0 {
				f.Write([]byte(perfHeader))
			}
		}
		f.Write([]byte(line + "\n"))
	}
	tick := time.NewTicker(perfEvery)
	defer tick.Stop()
	lastCPU, lastAt := processCPU(), time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case line := <-a.perf.lines:
			put(line)
		case now := <-tick.C:
			cpu := processCPU()
			ns, bytes, n, max := a.perf.minute.take()
			r := protocol.PerfRecord{Time: now, Kind: "minute", Bytes: bytes[actNet], Files: a.perf.files.Swap(0),
				Streams: a.activeCount(), Cores: runtime.NumCPU(), Acts: perfActsOf(ns, bytes, n, max)}
			if wall := now.Sub(lastAt); wall > 0 && cpu > 0 {
				r.CPU = math.Round(1000*float64(cpu-lastCPU)/float64(wall)) / 10
			}
			lastCPU, lastAt = cpu, now
			if len(r.Acts) == 0 {
				continue // nothing done: no line
			}
			if b, err := json.Marshal(r); err == nil {
				put(string(b))
			}
		}
	}
}
