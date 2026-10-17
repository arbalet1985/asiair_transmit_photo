package receiver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/satinfo"
)

func TestUISatInfo(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	if code := uiCall(t, ui, "GET", "/ui/api/satinfo?norad=x", nil, nil); code != http.StatusBadRequest {
		t.Errorf("no number: %d", code)
	}
	if code := uiCall(t, ui, "GET", "/ui/api/satinfo/photo?norad=25544", nil, nil); code != http.StatusNotFound {
		t.Errorf("no picture: %d", code)
	}
	// what was found before is answered without asking the sources
	dir := filepath.Join(s.cfg.DataDir, "satinfo")
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(satinfo.Info{NORAD: 25544, Name: "ISS (ZARYA)", Intl: "1998-067A", OwnerText: "МКС", Fetched: time.Now(),
		Photo: &satinfo.Photo{Source: "SatNOGS DB"}})
	os.WriteFile(filepath.Join(dir, "25544.json"), b, 0o644)
	os.WriteFile(s.satAbout.PhotoPath(25544), []byte("\xff\xd8jpeg"), 0o644)
	var got struct {
		Name  string         `json:"name"`
		Rows  [][2]string    `json:"rows"`
		Links []satinfo.Link `json:"links"`
		Photo *satinfo.Photo `json:"photo"`
	}
	if code := uiCall(t, ui, "GET", "/ui/api/satinfo?norad=25544&name=ISS", nil, &got); code != 200 {
		t.Fatalf("info: %d", code)
	}
	if got.Name != "ISS (ZARYA)" || len(got.Rows) < 2 || len(got.Links) < 3 || got.Photo == nil {
		t.Errorf("info %+v", got)
	}
	req := httptest.NewRequest("GET", "/ui/api/satinfo/photo?norad=25544", nil)
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("picture: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}
