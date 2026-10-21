package receiver

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// writeStarsFITS writes a 16-bit FITS frame with Gaussian stars of the given sigma.
func writeStarsFITS(t *testing.T, name string, nStars int, sigma float64) {
	t.Helper()
	const w, h = 600, 400
	rnd := rand.New(rand.NewSource(7))
	d := make([]float64, w*h)
	for i := range d {
		d[i] = 3000 + rnd.NormFloat64()*60
	}
	for s := 0; s < nStars; s++ {
		cx, cy := 30+rnd.Float64()*(w-60), 30+rnd.Float64()*(h-60)
		u := rnd.Float64()
		amp := 600 + 15000*u*u*u
		for y := int(cy) - 12; y <= int(cy)+12; y++ {
			for x := int(cx) - 12; x <= int(cx)+12; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				d[y*w+x] += amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
			}
		}
	}
	var b bytes.Buffer
	for _, c := range []string{"SIMPLE  =                    T", "BITPIX  =                   16", "NAXIS   =                    2",
		fmt.Sprintf("NAXIS1  = %20d", w), fmt.Sprintf("NAXIS2  = %20d", h), "BZERO   =                32768", "BSCALE  =                    1", "END"} {
		b.WriteString(fmt.Sprintf("%-80s", c))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	for _, v := range d {
		binary.Write(&b, binary.BigEndian, int16(int(math.Min(v, 65535))-32768))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(0)
	}
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Received Light frames are measured and shown on the page; other files are not.
func TestQuality(t *testing.T) {
	s, _ := newTestServer(t)
	root := s.getStore().root
	light := "EMMC Images/Autorun/Light/M31/Light_M31_300.0s_0001.fit"
	blurred := "EMMC Images/Autorun/Light/M31/Light_M31_300.0s_0002.fit"
	dark := "EMMC Images/Autorun/Dark/Dark_300.0s_0001.fit"
	writeStarsFITS(t, filepath.Join(root, filepath.FromSlash(light)), 120, 1.5)
	writeStarsFITS(t, filepath.Join(root, filepath.FromSlash(blurred)), 120, 3.5)
	writeStarsFITS(t, filepath.Join(root, filepath.FromSlash(dark)), 0, 1)
	if n := s.measurePending(context.Background()); n != 2 {
		t.Fatalf("measured %d, want the 2 Light frames", n)
	}
	if n := s.measurePending(context.Background()); n != 0 {
		t.Fatalf("measured again: %d", n)
	}
	rec := call(t, s.uiHandler(), map[string]string{}, "GET", "/ui/api/state", nil)
	var st struct {
		Files []uiFile `json:"files"`
	}
	json.Unmarshal(rec.Body.Bytes(), &st)
	got := map[string]uiFile{}
	for _, f := range st.Files {
		got[f.Path] = f
	}
	a, b := got[light], got[blurred]
	if !a.Measured || a.Stars < 80 || a.HFR < 1.4 || a.HFR > 2.6 {
		t.Fatalf("sharp frame: %+v", a)
	}
	if !b.Measured || b.HFR < 1.5*a.HFR {
		t.Fatalf("blurred frame: %+v vs %+v", b, a)
	}
	if got[dark].Measured {
		t.Fatal("a dark frame was measured")
	}
	// kept across a restart
	s.quality.save()
	q := newQuality(s.quality.file)
	if e, ok := q.get(light, a.Size, a.MTime); !ok || e.Stars != a.Stars {
		t.Fatalf("not saved: %+v %v", e, ok)
	}
}

// "Open the folder" works on this computer only and for received paths.
func TestOpenFolder(t *testing.T) {
	s, _ := newTestServer(t)
	var opened []string
	old := openInFolder
	openInFolder = func(local string, isDir bool) error {
		opened = append(opened, fmt.Sprintf("%s %v", local, isDir))
		return nil
	}
	defer func() { openInFolder = old }()
	root := s.getStore().root
	os.MkdirAll(filepath.Join(root, "EMMC Images", "Autorun"), 0o755)
	os.WriteFile(filepath.Join(root, "EMMC Images", "Autorun", "a.fit"), []byte("x"), 0o644)
	local := s.uiHandler()
	jar := map[string]string{}
	if rec := call(t, local, jar, "POST", "/ui/api/open", map[string]string{"path": "EMMC Images/Autorun/a.fit"}); rec.Code != 204 {
		t.Fatalf("file: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, local, jar, "POST", "/ui/api/open", map[string]string{"path": "EMMC Images/Autorun"}); rec.Code != 204 {
		t.Fatalf("folder: %d", rec.Code)
	}
	if rec := call(t, local, jar, "POST", "/ui/api/open", map[string]string{"path": "EMMC Images/../../etc"}); rec.Code != 400 {
		t.Fatalf("traversal: %d", rec.Code)
	}
	if rec := call(t, local, jar, "POST", "/ui/api/open", map[string]string{"path": "EMMC Images/none.fit"}); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if len(opened) != 2 || opened[0] != filepath.Join(root, "EMMC Images", "Autorun", "a.fit")+" false" {
		t.Fatalf("%v", opened)
	}
}

// Gallery thumbnails of FITS and PNG files.
func TestThumb(t *testing.T) {
	s, _ := newTestServer(t)
	root := s.getStore().root
	fitsPath := "EMMC Images/Autorun/Light/M31/Light_M31_300.0s_0001.fit"
	writeStarsFITS(t, filepath.Join(root, filepath.FromSlash(fitsPath)), 50, 1.5)
	pngPath := "EMMC Images/Live/x.png"
	img := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	var pb bytes.Buffer
	png.Encode(&pb, img)
	os.MkdirAll(filepath.Join(root, "EMMC Images", "Live"), 0o755)
	os.WriteFile(filepath.Join(root, filepath.FromSlash(pngPath)), pb.Bytes(), 0o644)

	h := s.uiHandler()
	for _, p := range []string{fitsPath, pngPath} {
		rec := call(t, h, map[string]string{}, "GET", "/ui/thumb?w=200&path="+url.QueryEscape(p), nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
		im, err := jpeg.Decode(rec.Body)
		if err != nil || im.Bounds().Dx() > 200 || im.Bounds().Dx() < 100 {
			t.Fatalf("%s: %v %v", p, err, im.Bounds())
		}
		req := httptest.NewRequest("GET", "/ui/thumb?w=200&path="+url.QueryEscape(p), nil)
		req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotModified {
			t.Fatalf("%s: cached copy not reused: %d", p, rr.Code)
		}
	}
	if rec := call(t, h, map[string]string{}, "GET", "/ui/thumb?path="+url.QueryEscape("EMMC Images/none.fit"), nil); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

// A deleted frame leaves its measurement for the history of the night; it
// is dropped again when the frame comes back.
func TestQualityGone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "quality.json")
	q := newQuality(file)
	q.set("a/Light_1.fit", qualityEntry{V: qualityVersion, HFR: 2.5, Stars: 300, Start: 1000})
	q.set("a/Light_2.fit", qualityEntry{V: qualityVersion, HFR: 2.6, Stars: 280})
	q.set("a/Light_3.fit", qualityEntry{V: qualityVersion, Err: "broken"})
	rev0, _ := q.deletedRev()
	q.forget(map[string]bool{"a/Light_2.fit": true})
	gone, rev := q.deleted()
	if len(gone) != 1 || gone["a/Light_1.fit"].HFR != 2.5 || gone["a/Light_1.fit"].Gone == 0 || rev == rev0 {
		t.Fatalf("%v %d", gone, rev)
	}
	if _, ok := q.m["a/Light_1.fit"]; ok {
		t.Fatal("still measured")
	}
	q.save()
	// kept over a restart
	q2 := newQuality(file)
	if g, _ := q2.deleted(); len(g) != 1 {
		t.Fatalf("not kept: %v", g)
	}
	// received again
	q2.forget(map[string]bool{"a/Light_1.fit": true, "a/Light_2.fit": true})
	if g, _ := q2.deleted(); len(g) != 0 {
		t.Fatalf("back: %v", g)
	}
}

func TestUIGone(t *testing.T) {
	s, _ := newTestServer(t)
	ui := s.uiHandler()
	s.quality.set("EMMC Images/Autorun/Light/M 31/Light_M 31_300.0s_Bin1_20261015-221000_0001.fit", qualityEntry{V: qualityVersion, HFR: 3.1, Stars: 120})
	s.quality.forget(map[string]bool{})
	var st struct {
		GoneRev int64 `json:"gone_rev"`
		GoneN   int   `json:"gone_n"`
	}
	if uiCall(t, ui, "GET", "/ui/api/state", nil, &st) != 200 || st.GoneN != 1 || st.GoneRev == 0 {
		t.Fatalf("state: %+v", st)
	}
	var got struct {
		Rev   int64
		Files []uiFile
	}
	if uiCall(t, ui, "GET", "/ui/api/gone", nil, &got) != 200 || got.Rev != st.GoneRev || len(got.Files) != 1 ||
		got.Files[0].HFR != 3.1 || got.Files[0].Gone == 0 || !got.Files[0].Measured || got.Files[0].Name != "Light_M 31_300.0s_Bin1_20261015-221000_0001.fit" {
		t.Fatalf("gone: %+v", got)
	}
}
