package receiver

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// writeTestFITS writes a small 16-bit FITS frame with the given keywords.
func writeTestFITS(t *testing.T, p string, keys map[string]string) {
	t.Helper()
	var h strings.Builder
	card := func(k, v string) { fmt.Fprintf(&h, "%-8s= %-70s", k, v) }
	card("SIMPLE", "T")
	card("BITPIX", "16")
	card("NAXIS", "2")
	card("NAXIS1", "64")
	card("NAXIS2", "48")
	for k, v := range keys {
		card(k, v)
	}
	fmt.Fprintf(&h, "%-80s", "END")
	for h.Len()%2880 != 0 {
		h.WriteByte(' ')
	}
	data := make([]byte, 64*48*2)
	for i := range data {
		data[i] = byte(i * 7)
	}
	data = append(data, make([]byte, (2880-len(data)%2880)%2880)...)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, append([]byte(h.String()), data...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The sorter on another computer: it logs in like a phone, reads the
// headers with range requests, downloads only what the output lacks and
// continues a download that stopped.
func TestSorterFromReceiver(t *testing.T) {
	loginDelay = time.Millisecond
	defer func() { loginDelay = time.Second }()
	s, dir := newTestServer(t)
	local := s.uiHandler()
	jar := map[string]string{}
	for _, a := range []map[string]string{{"action": "internet-on"}, {"action": "password", "password": "звёздное небо"}} {
		if rec := call(t, local, jar, "POST", "/ui/api/remote", a); rec.Code != 200 {
			t.Fatalf("%v: %d %s", a, rec.Code, rec.Body)
		}
	}
	ts := httptest.NewUnstartedServer(s.agentPortHandler("internet"))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}}
	ts.StartTLS()
	defer ts.Close()

	root := filepath.Join(s.cfg.DestDir, "EMMC Images")
	light := map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "300", "GAIN": "100", "SET-TEMP": "-10", "FILTER": "'Ha'"}
	for i := 1; i <= 2; i++ {
		writeTestFITS(t, filepath.Join(root, "Plan", "Light", "M 31", fmt.Sprintf("Light_M 31_300.0s_Bin1_Ha_gain100_20260926-2210%02d_-10.0C_%04d.fit", i, i)), light)
	}
	writeTestFITS(t, filepath.Join(root, "Autorun", "Flat", "Flat_1.5s_Bin1_Ha_gain100_20260927-063000_-10.0C_0001.fit"),
		map[string]string{"IMAGETYP": "'Flat'", "EXPTIME": "1.5", "GAIN": "100", "FILTER": "'Ha'"})
	writeTestFITS(t, filepath.Join(root, "Autorun", "Dark", "Dark_300.0s_Bin1_gain100_20260801-120000_-10.0C_0001.fit"),
		map[string]string{"IMAGETYP": "'Dark'", "EXPTIME": "300", "GAIN": "100", "SET-TEMP": "-10"})
	os.WriteFile(filepath.Join(root, "Plan", "Light", "M 31", "Light_M 31_0001_thn.jpg"), []byte("jpg"), 0o644)

	ctx := context.Background()
	var pinned string
	r, err := sorter.NewReceiver(ts.URL+"/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r.OnPin = func(fp string) { pinned = fp }
	if _, err := r.Files(ctx); !errors.Is(err, sorter.ErrLogin) {
		t.Fatalf("files before login: %v", err)
	}
	if err := r.Login(ctx, "не тот", ""); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := r.Login(ctx, "звёздное небо", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(strings.ReplaceAll(pinned, ":", ""), strings.ReplaceAll(s.fingerprint, ":", "")) {
		t.Fatalf("pinned %q, receiver %q", pinned, s.fingerprint)
	}

	cacheFile := filepath.Join(dir, "headers.json")
	cache := sorter.LoadHeaderCache(cacheFile)
	frames, errs := sorter.ScanReceiver(ctx, r, cache, nil)
	if len(errs) > 0 || len(frames) != 4 {
		t.Fatalf("%d frames, %v", len(frames), errs)
	}
	if err := cache.Save(r.Base()); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	l := sorter.Plan(frames)
	if len(l.Sessions) != 1 || l.Sessions[0].Darks == nil || l.Sessions[0].Flats == nil {
		t.Fatalf("plan: %s", l.Summary())
	}
	if n, _ := l.Missing(out); n != 4 {
		t.Fatalf("missing %d", n)
	}
	var last sorter.Progress
	res := l.Apply(ctx, out, sorter.Options{FillCalibration: true}, func(p sorter.Progress) { last = p })
	if len(res.Errors) > 0 || res.Placed != 4 || res.Calibration != 2 || last.Bytes != last.TotalBytes || last.TotalBytes == 0 {
		t.Fatalf("%+v %+v", res, last)
	}
	sess := filepath.Join(out, "Objects", "M 31", "2026-09-26_Ha_300s_gain100")
	name := "Light_M 31_300.0s_Bin1_Ha_gain100_20260926-221001_-10.0C_0001.fit"
	want, _ := os.ReadFile(filepath.Join(root, "Plan", "Light", "M 31", name))
	got, err := os.ReadFile(filepath.Join(sess, "lights", name))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("downloaded frame differs: %v", err)
	}
	for _, p := range []string{"darks", "flats"} {
		if m, _ := filepath.Glob(filepath.Join(sess, p, "*.fit")); len(m) != 1 {
			t.Fatalf("%s: %v", p, m)
		}
	}

	// the next run: the headers come from the cache, nothing is downloaded
	os.Remove(filepath.Join(root, "Autorun", "Dark", "Dark_300.0s_Bin1_gain100_20260801-120000_-10.0C_0001.fit")) // gone on the receiver
	r2, _ := sorter.NewReceiver(ts.URL, pinned, r.Session())
	frames, errs = sorter.ScanReceiver(ctx, r2, sorter.LoadHeaderCache(cacheFile), nil)
	lib, _ := sorter.ScanLibrary(out, nil)
	if len(errs) > 0 || len(frames) != 3 || len(lib) != 2 {
		t.Fatalf("again: %d frames, %d in the library, %v", len(frames), len(lib), errs)
	}
	l = sorter.Plan(append(frames, lib...))
	if l.Sessions[0].Darks == nil {
		t.Fatal("the dark in the library is not used")
	}
	if res = l.Apply(ctx, out, sorter.Options{}, nil); len(res.Errors) > 0 || res.Placed != 0 || res.Downloaded != 0 {
		t.Fatalf("again: %+v", res)
	}

	// a download that stopped halfway continues from there
	dst := filepath.Join(sess, "lights", name)
	os.Remove(dst)
	os.WriteFile(dst+sorter.PartSuffix, want[:len(want)/2], 0o644)
	if n, b := l.Missing(out); n != 1 || b != int64(len(want)-len(want)/2) {
		t.Fatalf("missing %d, %d bytes", n, b)
	}
	res = l.Apply(ctx, out, sorter.Options{}, nil)
	got, _ = os.ReadFile(dst)
	if len(res.Errors) > 0 || res.Placed != 1 || res.Downloaded != int64(len(want)-len(want)/2) || !bytes.Equal(got, want) {
		t.Fatalf("resume: %+v", res)
	}
	if _, err := os.Stat(dst + sorter.PartSuffix); err == nil {
		t.Fatal("the part is left")
	}

	im, err := r2.Preview(ctx, frames[0].Path, 32)
	if err != nil || im.Width == 0 || im.Stretched() == nil {
		t.Fatalf("preview: %v", err)
	}

	// another certificate than the remembered one is refused
	r3, _ := sorter.NewReceiver(ts.URL, "AA:BB", r.Session())
	var fe *sorter.FingerprintError
	if _, err := r3.Files(ctx); !errors.As(err, &fe) {
		t.Fatalf("other certificate: %v", err)
	}
}
