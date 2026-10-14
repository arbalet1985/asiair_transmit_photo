package receiver

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func TestUIDeleteLocal(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	gone := "EMMC Images/Plan/Light/M 31/a.fit"  // deleted on the ASIAIR
	there := "EMMC Images/Plan/Light/M 31/b.fit" // still there
	for _, p := range []string{gone, there} {
		l := s.getStore().Local(p)
		os.MkdirAll(filepath.Dir(l), 0o755)
		os.WriteFile(l, []byte("x"), 0o644)
	}
	if err := s.state.SetInventory([]protocol.RemoteFile{{Path: there, Size: 1}}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Deleted int      `json:"deleted"`
		Errors  []string `json:"errors"`
	}
	if code := uiCall(t, ui, http.MethodPost, "/ui/api/delete_local", map[string]any{"paths": []string{gone, there}}, &out); code != 200 {
		t.Fatalf("code %d", code)
	}
	if out.Deleted != 1 || len(out.Errors) != 1 {
		t.Fatalf("%+v", out)
	}
	if _, err := os.Stat(s.getStore().Local(gone)); err == nil {
		t.Fatal("the copy of the file gone from the ASIAIR is left")
	}
	if _, err := os.Stat(s.getStore().Local(there)); err != nil {
		t.Fatal("the copy of a file still on the ASIAIR is deleted")
	}
	if code := uiCall(t, ui, http.MethodPost, "/ui/api/delete_local", map[string]any{"paths": []string{"../x"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a path out of the folder: %d", code)
	}
}
