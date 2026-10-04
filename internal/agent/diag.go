package agent

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// diag measures where the uploads spend their time. Every upload body is
// read by the HTTP transport: the time inside the read of the source is
// "read" (the ASIAIR is slow), the rest of the time inside the body read is
// "limit" (waiting for the rate limit), and the time between two reads is
// "network" (the transport pushes the data out). The shares are summed over
// all streams.
type diag struct {
	bytes, readNs, limitNs, netNs atomic.Int64
	raw                           atomic.Int64 // bytes read from the ASIAIR (more than bytes when compressed)

	mu     sync.Mutex
	shares map[string]*shareTime // reading per ASIAIR storage in the window
	start  time.Time             // of the current window
	last   protocol.Diag         // result of the last full window
	when   time.Time             // when last was measured
	path   string                // Tailscale path to the receiver
}

const diagWindow = 3 * time.Second

// shareTime is the reading of one storage: bytes and time spent waiting.
type shareTime struct{ bytes, ns int64 }

func (d *diag) addShare(share string, n int, ns int64) {
	d.mu.Lock()
	if d.shares == nil {
		d.shares = map[string]*shareTime{}
	}
	st := d.shares[share]
	if st == nil {
		st = &shareTime{}
		d.shares[share] = st
	}
	st.bytes += int64(n)
	st.ns += ns
	d.mu.Unlock()
}

// shareMinWait: less waiting than this in a window means the storage did
// not hold the sending back, and its speed cannot be told.
const shareMinWait = 150 * time.Millisecond

// result returns the measurement of the last window (or none when nothing
// was sent for a while). streams is the number of files sent at once.
func (d *diag) result(streams int) *protocol.Diag {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if d.start.IsZero() {
		d.start = now
	}
	if now.Sub(d.start) >= diagWindow {
		read, limit, net := d.readNs.Swap(0), d.limitNs.Swap(0), d.netNs.Swap(0)
		n, raw := d.bytes.Swap(0), d.raw.Swap(0)
		d.start = now
		defer func() { d.shares = nil }() // a new window
		if total := read + limit + net; total > int64(100*time.Millisecond) && n > 0 {
			d.last = protocol.Diag{
				Read: float64(read) / float64(total), Limit: float64(limit) / float64(total),
				Network: float64(net) / float64(total),
			}
			if read > total/5 { // too little reading to tell its speed otherwise
				d.last.ReadSpeed = float64(raw) / (float64(read) / 1e9)
			}
			names := make([]string, 0, len(d.shares))
			for name, st := range d.shares {
				if st.bytes > 0 {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			for _, name := range names {
				st, sr := d.shares[name], protocol.ShareRead{Share: name}
				if st.ns >= int64(shareMinWait) {
					sr.Speed = float64(st.bytes) / (float64(st.ns) / 1e9)
				}
				d.last.Shares = append(d.last.Shares, sr)
			}
			d.when = now
		}
	}
	var out protocol.Diag
	if now.Sub(d.when) < 3*diagWindow {
		out = d.last
	}
	out.Path = d.path
	out.Bottleneck = bottleneck(out)
	out.Text = diagText(out, streams)
	if out.Text == "" && out.Path == "" {
		return nil
	}
	return &out
}

func (d *diag) setPath(p string) {
	d.mu.Lock()
	d.path = p
	d.mu.Unlock()
}

func bottleneck(d protocol.Diag) string {
	switch {
	case d.Read+d.Limit+d.Network == 0:
		return ""
	case d.Limit >= d.Read && d.Limit >= d.Network:
		return "limit"
	case d.Read >= d.Network:
		return "read"
	}
	return "network"
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func diagText(d protocol.Diag, streams int) string {
	var t, hint string
	switch d.Bottleneck {
	case "limit":
		t = "упирается в ограничение скорости (бегунок)"
	case "read":
		t = "упирается в чтение с ASIAIR (Wi-Fi или сеть обсерватории)"
		if d.ReadSpeed > 0 {
			t += fmt.Sprintf(", ~%s/s на поток", config.FormatBytes(int64(d.ReadSpeed)))
		}
	case "network":
		t = "упирается в сеть до приёмника (интернет обсерватории или Tailscale)"
		if streams < protocol.MaxStreams {
			hint = "; попробуйте больше потоков на приёмнике"
		}
	default:
		return ""
	}
	return fmt.Sprintf("%s — время: ASIAIR %s, сеть %s, лимит %s%s", t, pct(d.Read), pct(d.Network), pct(d.Limit), hint)
}

// timedBody is the upload body: it counts the time between reads as
// network time and the time inside reads as limit time; the source reads
// (timedSource) are moved from limit to read time.
type timedBody struct {
	r    io.Reader
	d    *diag
	last time.Time
}

func (b *timedBody) Read(p []byte) (int, error) {
	start := time.Now()
	if !b.last.IsZero() {
		b.d.netNs.Add(int64(start.Sub(b.last)))
	}
	n, err := b.r.Read(p)
	b.last = time.Now()
	b.d.limitNs.Add(int64(b.last.Sub(start)))
	b.d.bytes.Add(int64(n))
	return n, err
}

type timedSource struct {
	r     io.Reader
	d     *diag
	share string // ASIAIR storage of the file
}

func (s *timedSource) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := s.r.Read(p)
	ns := int64(time.Since(start))
	s.d.readNs.Add(ns)
	s.d.limitNs.Add(-ns) // it is inside the timedBody read
	s.d.raw.Add(int64(n))
	s.d.addShare(s.share, n, ns)
	return n, err
}

// busy takes out of the limit the time spent compressing (inside the
// timedBody read, like the reading of the source).
func (d *diag) busy(t time.Duration) { d.limitNs.Add(-int64(t)) }

// watchPath keeps the Tailscale path to the receiver current.
func (a *Agent) watchPath(ctx context.Context) {
	if a.ts == nil {
		return
	}
	u, err := url.Parse(a.cfg.ReceiverURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	for {
		a.diag.setPath(a.ts.Path(ctx, host))
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}
