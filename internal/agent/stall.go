package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// A connection that lost packets (a Wi-Fi hiccup, a busy uplink) may go on
// at a few bytes a second for many minutes, while a new one gets going at
// once. The watchdog ends an upload that has hardly moved for a while; it is
// resumed at once from where the receiver stopped.

// timings of the watchdog (tests shorten them)
var (
	stallWindow = time.Minute
	stallMin    = int64(16 << 10) // bytes the network must take within stallWindow
	stallRetry  = 2               // stalled uploads resumed at once before the usual pause
)

// errStalled ends an upload the watchdog found stuck.
var errStalled = errors.New("передача зависла")

// stallError tells which side stood still.
type stallError struct {
	sent, read int64 // bytes taken by the network and read from the ASIAIR within the window
}

func (e *stallError) Error() string {
	what := "сеть до приёмника не принимает данные"
	if e.read < stallMin {
		what = "чтение с ASIAIR не движется"
	}
	return fmt.Sprintf("передача зависла: за %v ушло %d байт (%s) — соединение открыто заново", stallWindow, e.sent, what)
}

func (e *stallError) Unwrap() error { return errStalled }

// counter counts the bytes read through it.
type counter struct {
	r   io.Reader
	n   atomic.Int64
	eof atomic.Bool // all read: the upload waits for the answer
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	if err == io.EOF {
		c.eof.Store(true)
	}
	return n, err
}

// watchStall cancels the upload (with a *stallError) when the network took
// fewer than stallMin bytes within stallWindow, until stop is closed. A rate
// limit too low for that turns the watchdog off.
func (a *Agent) watchStall(cancel context.CancelCauseFunc, sent, read *counter, stop <-chan struct{}) {
	if r := a.lim.Rate(); r > 0 && float64(r)*stallWindow.Seconds() < float64(4*stallMin) {
		return
	}
	type sample struct {
		t          time.Time
		sent, read int64
	}
	tick := time.NewTicker(stallWindow / 12)
	defer tick.Stop()
	samples := []sample{{time.Now(), 0, 0}}
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			if sent.eof.Load() {
				return // all sent: the receiver checks the file and answers
			}
			s := sample{now, sent.n.Load(), read.n.Load()}
			samples = append(samples, s)
			// the oldest sample at least a window old
			i := 0
			for i+1 < len(samples) && now.Sub(samples[i+1].t) >= stallWindow {
				i++
			}
			samples = samples[i:]
			old := samples[0]
			if now.Sub(old.t) >= stallWindow && s.sent-old.sent < stallMin {
				cancel(&stallError{sent: s.sent - old.sent, read: s.read - old.read})
				return
			}
		}
	}
}

// ctxReader lets a canceled upload end while a read of the file hangs (the
// ASIAIR stopped answering): the HTTP client gives a canceled request back
// only once the body read in progress returns. Every read runs apart, into
// a buffer of its own; after the cancel it is left to finish by itself (the
// file is closed when the upload ends).
type ctxReader struct {
	ctx  context.Context
	r    io.Reader
	buf  []byte
	gone bool // a read was left running: r is not read again
}

type readResult struct {
	n   int
	err error
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if c.gone {
		return 0, context.Cause(c.ctx)
	}
	if len(c.buf) < len(p) {
		c.buf = make([]byte, len(p))
	}
	buf := c.buf[:len(p)]
	ch := make(chan readResult, 1)
	go func() {
		n, err := c.r.Read(buf)
		ch <- readResult{n, err}
	}()
	select {
	case res := <-ch:
		copy(p, buf[:res.n])
		return res.n, res.err
	case <-c.ctx.Done():
		c.gone, c.buf = true, nil // buf stays with the read left running
		return 0, context.Cause(c.ctx)
	}
}
