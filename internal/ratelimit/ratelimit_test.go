package ratelimit

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestLimiterRate(t *testing.T) {
	const bps = 200 * 1024
	l := New(bps)
	data := make([]byte, bps*3/2) // 1.5 s worth
	start := time.Now()
	n, err := io.Copy(io.Discard, l.Reader(context.Background(), bytes.NewReader(data)))
	if err != nil || n != int64(len(data)) {
		t.Fatalf("copy: %d %v", n, err)
	}
	el := time.Since(start).Seconds()
	// the first burst is free, so allow some slack below 1.5 s
	if el < 1.25 || el > 2.0 {
		t.Fatalf("1.5 s of data at the limit took %.2f s", el)
	}
}

func TestLimiterUnlimitedAndChange(t *testing.T) {
	l := New(0)
	data := make([]byte, 20<<20)
	start := time.Now()
	io.Copy(io.Discard, l.Reader(context.Background(), bytes.NewReader(data)))
	if time.Since(start) > time.Second {
		t.Fatalf("unlimited copy was throttled")
	}
	l.SetRate(100 * 1024)
	if l.Rate() != 100*1024 {
		t.Fatalf("rate not updated")
	}
	start = time.Now()
	io.Copy(io.Discard, l.Reader(context.Background(), bytes.NewReader(make([]byte, 100*1024))))
	if el := time.Since(start).Seconds(); el < 0.7 {
		t.Fatalf("limit change not applied: %.2f s", el)
	}
}

func TestLimiterCancel(t *testing.T) {
	l := New(1024)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := io.Copy(io.Discard, l.Reader(ctx, bytes.NewReader(make([]byte, 1<<20))))
	if err == nil {
		t.Fatal("expected error after cancel")
	}
}
