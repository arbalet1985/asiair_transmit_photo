package receiver

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFITS writes a small 16-bit mono FITS file.
func writeFITS(t *testing.T, name string, w, h int) {
	t.Helper()
	var b bytes.Buffer
	for _, c := range []string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2",
		fmt.Sprintf("NAXIS1  = %d", w), fmt.Sprintf("NAXIS2  = %d", h), "BZERO   = 32768",
		"OBJECT  = 'M 42'", "EXPTIME = 60.0", "END"} {
		b.WriteString(fmt.Sprintf("%-80s", c))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	for i := 0; i < w*h; i++ {
		binary.Write(&b, binary.BigEndian, int16(i%1000-32768))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(0)
	}
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUIPreview(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	p := "EMMC Images/Light/M42/a.fit"
	writeFITS(t, s.getStore().Local(p), 300, 200)

	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ui.ServeHTTP(rec, httptest.NewRequest("GET", "/ui/preview?"+q, nil))
		return rec
	}
	rec := get("path=" + url.QueryEscape(p) + "&max=100")
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.Bytes()
	n := binary.LittleEndian.Uint32(body)
	var info previewInfo
	if err := json.Unmarshal(body[4:4+n], &info); err != nil {
		t.Fatal(err)
	}
	if info.Width != 100 || info.Height != 66 || info.Channels != 1 || info.Source != [2]int{300, 200} ||
		info.Header["OBJECT"] != "M 42" || info.Header["EXPTIME"] != "60.0" {
		t.Fatalf("info: %+v", info)
	}
	if n%2 != 0 || len(body) != 4+int(n)+info.Width*info.Height*2 {
		t.Fatalf("body length %d, json %d", len(body), n)
	}
	// second request is served from the cache
	if len(s.previews.items) != 1 {
		t.Fatal("preview not cached")
	}
	get("path=" + url.QueryEscape(p) + "&max=100")
	if len(s.previews.items) != 1 {
		t.Fatal("cache entry duplicated")
	}

	for q, code := range map[string]int{
		"path=" + url.QueryEscape("EMMC Images/Light/a.jpg"):     400,
		"path=" + url.QueryEscape("EMMC Images/../../etc/x.fit"): 400,
		"path=" + url.QueryEscape("EMMC Images/none.fit"):        422,
	} {
		if rec := get(q); rec.Code != code {
			t.Errorf("%s: got %d, want %d (%s)", q, rec.Code, code, strings.TrimSpace(rec.Body.String()))
		}
	}
}
