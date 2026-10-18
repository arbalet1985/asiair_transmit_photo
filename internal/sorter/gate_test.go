package sorter

import (
	"context"
	"testing"
	"time"
)

// A pause cancels the downloads going on and holds the next ones until
// resumed.
func TestGate(t *testing.T) {
	g := NewGate()
	ctx := context.Background()
	c, cancel := g.context(ctx)
	defer cancel()
	g.SetPaused(true)
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("the pause did not interrupt the download")
	}
	waited := make(chan error, 1)
	go func() { waited <- g.wait(ctx) }()
	select {
	case <-waited:
		t.Fatal("a download began while paused")
	case <-time.After(100 * time.Millisecond):
	}
	g.SetPaused(false)
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	c2, cancel2 := g.context(ctx)
	defer cancel2()
	if c2.Err() != nil || g.Paused() {
		t.Fatal("still paused after resuming")
	}
}
