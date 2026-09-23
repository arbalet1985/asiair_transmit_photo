// Package ratelimit throttles data streams to a shared bytes-per-second budget.
package ratelimit

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const maxChunk = 64 * 1024

// Limiter is shared by all uploads; its rate can be changed at any time.
type Limiter struct {
	mu  sync.Mutex
	bps int64
	lim *rate.Limiter

	// throughput measurement
	total   atomic.Int64
	speedMu sync.Mutex
	lastT   time.Time
	lastN   int64
	speed   float64
}

// New creates a limiter; bps <= 0 means unlimited.
func New(bps int64) *Limiter {
	l := &Limiter{lim: rate.NewLimiter(rate.Inf, maxChunk), lastT: time.Now()}
	l.SetRate(bps)
	return l
}

// SetRate changes the limit; bps <= 0 means unlimited.
func (l *Limiter) SetRate(bps int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bps == l.bps && l.lim != nil {
		return
	}
	l.bps = bps
	if bps <= 0 {
		l.lim.SetLimit(rate.Inf)
		return
	}
	l.lim.SetBurst(burstFor(bps))
	l.lim.SetLimit(rate.Limit(bps))
}

// Rate returns the current limit in bytes per second (0 = unlimited).
func (l *Limiter) Rate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bps
}

// burstFor keeps bursts around 1/10 s of traffic so the rate is smooth even
// for slow links, but not below 1 KiB or above one read chunk.
func burstFor(bps int64) int {
	b := bps / 10
	if b < 1024 {
		b = 1024
	}
	if b > maxChunk {
		b = maxChunk
	}
	return int(b)
}

// Reader wraps r so that reads are throttled by the limiter.
func (l *Limiter) Reader(ctx context.Context, r io.Reader) io.Reader {
	return &reader{ctx: ctx, r: r, l: l}
}

// Speed returns the measured throughput in bytes per second.
func (l *Limiter) Speed() float64 {
	l.speedMu.Lock()
	defer l.speedMu.Unlock()
	now := time.Now()
	dt := now.Sub(l.lastT).Seconds()
	if dt >= 1 {
		n := l.total.Load()
		inst := float64(n-l.lastN) / dt
		// smooth a bit so the UI does not jump around
		if l.speed == 0 || dt > 10 {
			l.speed = inst
		} else {
			l.speed = 0.5*l.speed + 0.5*inst
		}
		l.lastT, l.lastN = now, n
	}
	return l.speed
}

// Total returns the number of bytes passed through the limiter.
func (l *Limiter) Total() int64 { return l.total.Load() }

type reader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (r *reader) Read(p []byte) (int, error) {
	lim := r.l.lim
	if b := lim.Burst(); lim.Limit() != rate.Inf && len(p) > b {
		p = p[:b]
	} else if len(p) > maxChunk {
		p = p[:maxChunk]
	}
	n, err := r.r.Read(p)
	// Wait in pieces no larger than the burst: the rate may change between
	// the read and the wait.
	for left := n; left > 0; {
		step := min(left, lim.Burst())
		if werr := lim.WaitN(r.ctx, step); werr != nil {
			return n, werr
		}
		left -= step
	}
	r.l.total.Add(int64(n))
	return n, err
}
