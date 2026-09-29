package receiver

import (
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func TestUIReadBlockAndDepth(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	if c := uiCall(t, ui, "POST", "/ui/api/readblock", map[string]any{"depth": 8}, nil); c != 204 {
		t.Fatalf("depth: %d", c)
	}
	if c := uiCall(t, ui, "POST", "/ui/api/readblock", map[string]any{"bytes": 256 << 10}, nil); c != 204 {
		t.Fatalf("bytes: %d", c)
	}
	for _, bad := range []map[string]any{{"depth": 99}, {"depth": -1}, {"bytes": 1}} {
		if c := uiCall(t, ui, "POST", "/ui/api/readblock", bad, nil); c != 400 {
			t.Fatalf("%v: %d, want 400", bad, c)
		}
	}
	// setting one keeps the other; the agent gets both
	resp := s.state.Sync(protocol.SyncRequest{})
	if resp.ReadDepth != 8 || resp.ReadBlock != 256<<10 {
		t.Fatalf("sync: depth %d, block %d", resp.ReadDepth, resp.ReadBlock)
	}
	var st struct {
		ReadBlock readBlockInfo `json:"read_block"`
	}
	if uiCall(t, ui, "GET", "/ui/api/state", nil, &st) != 200 || st.ReadBlock.DepthSet != 8 || st.ReadBlock.DepthDefault != 6 || st.ReadBlock.DepthMax != 16 {
		t.Fatalf("state: %+v", st.ReadBlock)
	}
}
