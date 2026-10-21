package receiver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// A received Light frame to the planner: its framing and its picture.
func TestUIFraming(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ui.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	// no coordinates in the header
	bare := "EMMC Images/Autorun/Light/M42/Light_M42_60.0s_0001.fit"
	writeFITS(t, s.getStore().Local(bare), 300, 200)
	if rec := get("/ui/api/framing?path=" + url.QueryEscape(bare)); rec.Code != 422 {
		t.Fatalf("no coordinates: %d %s", rec.Code, rec.Body)
	}
	// plate solved
	p := "EMMC Images/Autorun/Light/M 31/Light_M 31_120.0s_Bin1_20261016-220000_0002.fit"
	var b bytes.Buffer
	for _, c := range []string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2", "NAXIS1  = 300", "NAXIS2  = 200", "BZERO   = 32768",
		"OBJECT  = 'M 31'", "EXPTIME = 120.0", "CRVAL1  = 10.68", "CRVAL2  = 41.27", "CRPIX1  = 150.5", "CRPIX2  = 100.5",
		"CD1_1   = -0.001", "CD1_2   = 0", "CD2_1   = 0", "CD2_2   = 0.001", "END"} {
		b.WriteString(fmt.Sprintf("%-80s", c))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	for i := 0; i < 300*200; i++ {
		binary.Write(&b, binary.BigEndian, int16(i%1000-32768))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(0)
	}
	local := s.getStore().Local(p)
	os.MkdirAll(filepath.Dir(local), 0o755)
	if err := os.WriteFile(local, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name                  string
		RA, Dec, Angle, Width float64
		Exposure              float64
		Image                 string
		WCS                   map[string]any
	}
	if uiCall(t, ui, "GET", "/ui/api/framing?path="+url.QueryEscape(p), nil, &got) != 200 || got.Name != "M 31" || got.RA != 10.68 ||
		got.Exposure != 120 || got.Width < 0.29 || got.Width > 0.31 || got.WCS["NAXIS"] != float64(2) || got.Image == "" {
		t.Fatalf("%+v", got)
	}
	// rows from the top going north: north is down
	if got.Angle < 179.9 || got.Angle > 180.1 {
		t.Fatalf("angle %v", got.Angle)
	}
	rec := get(got.Image)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.Len() < 100 {
		t.Fatalf("picture: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}
