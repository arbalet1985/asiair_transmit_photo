package wire

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealFrames measures the compression of real frames:
// WIRE_FRAMES=<folder> go test -run TestRealFrames -v ./internal/wire
func TestRealFrames(t *testing.T) {
	root := os.Getenv("WIRE_FRAMES")
	if root == "" {
		t.Skip()
	}
	seen := map[string]int{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !Compressible(p) || fi.Size() < 5e6 {
			return nil
		}
		kind, _, _ := strings.Cut(filepath.Base(p), "_")
		if seen[kind] >= 3 {
			return nil
		}
		seen[kind]++
		data, _ := os.ReadFile(p)
		start := time.Now()
		enc := NewEncoder(bytes.NewReader(data))
		z, _ := io.ReadAll(enc)
		enc.Close()
		el := time.Since(start)
		dec, _ := NewDecoder(bytes.NewReader(z), int64(len(data)))
		got, _ := io.ReadAll(dec)
		t.Logf("%-6s %.2fx  %4.0f MB/s  ok=%v  %s", kind, float64(len(data))/float64(len(z)), float64(len(data))/el.Seconds()/1e6, bytes.Equal(got, data), filepath.Base(p)[:40])
		return nil
	})
}
