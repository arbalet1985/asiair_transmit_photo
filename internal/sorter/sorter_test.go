package sorter

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeFITS writes a tiny 16-bit FITS file with the given keywords.
func writeFITS(t *testing.T, p string, keys map[string]string) {
	t.Helper()
	var h strings.Builder
	card := func(k, v string) { h.WriteString(fmt.Sprintf("%-8s= %-70s", k, v)) }
	card("SIMPLE", "T")
	card("BITPIX", "16")
	card("NAXIS", "2")
	card("NAXIS1", "4")
	card("NAXIS2", "4")
	for k, v := range keys {
		card(k, v)
	}
	h.WriteString(fmt.Sprintf("%-80s", "END"))
	for h.Len()%2880 != 0 {
		h.WriteByte(' ')
	}
	data := make([]byte, 2880)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte(h.String()), data...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeHeader(t *testing.T) {
	keys := map[string]string{"IMAGETYP": "Light Frame", "EXPTIME": "300.", "GAIN": "100", "CCD-TEMP": "-9.8",
		"FILTER": "Ha", "INSTRUME": "ZWO ASI2600MM Pro", "XBINNING": "1", "OBJECT": "NGC 7000"}
	f := describe(func(k string) string { return keys[k] },
		"Plan/Light/M 31/Light_M 31_300.0s_Bin1_Ha_gain100_20260926-231000_-10.0C_0001.fit", time.Now())
	if f.Kind != Light || f.Object != "M 31" || f.Filter != "Ha" || f.Exposure != 300 || f.Gain != 100 || f.Temp != -10 || f.Camera != "ZWO ASI2600MM Pro" {
		t.Fatalf("%+v", f)
	}
	if f.Night != "2026-09-26" {
		t.Fatalf("night %s", f.Night)
	}
	if f.HasTarget || f.HasSite {
		t.Fatalf("no coordinates in the header: %+v", f)
	}
	keys["RA"], keys["DEC"], keys["SITELAT"], keys["SITELONG"] = "10.68", "41.27", "+55 45 00", "37.62"
	keys["DATE-OBS"] = "2026-09-26T20:10:00.123"
	f = describe(func(k string) string { return keys[k] }, "x/Light_1.fit", time.Now())
	if !f.HasTarget || f.RA != 10.68 || !f.HasSite || f.Lat != 55.75 || !f.UTC.Equal(time.Date(2026, 9, 26, 20, 10, 0, 123e6, time.UTC)) {
		t.Fatalf("coordinates: %+v", f)
	}
	if f.Focal != 0 || f.Width != 0 {
		t.Fatalf("no optics in the header: %+v", f)
	}
	keys["FOCALLEN"], keys["XPIXSZ"], keys["NAXIS1"], keys["NAXIS2"] = "400", "3.76", "6248", "4176"
	f = describe(func(k string) string { return keys[k] }, "x/Light_1.fit", time.Now())
	if f.Focal != 400 || f.PixelUm != 3.76 || f.Width != 6248 || f.Height != 4176 {
		t.Fatalf("optics: %+v", f)
	}
}

func TestDescribeFromName(t *testing.T) {
	none := func(string) string { return "" }
	// morning flats of the same night, taken with Autorun
	f := describe(none, "Autorun/Flat/Flat_1.5s_Bin1_gain100_20260927-061500_-10.0C_0001.fit", time.Now())
	if f.Kind != Flat || f.Exposure != 1.5 || f.Gain != 100 || f.Night != "2026-09-26" {
		t.Fatalf("%+v", f)
	}
	f = describe(none, "Autorun/Light/M 31/Light_M 31_120.0s_Bin2_gain0_20260926-221000_0001.fit", time.Now())
	if f.Kind != Light || f.Object != "M 31" || f.Bin != 2 || f.Gain != 0 || !math.IsNaN(f.Temp) {
		t.Fatalf("%+v", f)
	}
	f = describe(none, "x/Bias_20.0ms_Bin1_gain100_20260926-221000_-10.0C_0001.fit", time.Now())
	if f.Kind != Bias || f.Exposure != 0.02 {
		t.Fatalf("%+v", f)
	}
	f = describe(func(k string) string {
		if k == "IMAGETYP" {
			return "Dark"
		}
		return ""
	}, "DarkFlat_1.5s_gain100_0001.fit", time.Now())
	if f.Kind != DarkFlat {
		t.Fatalf("%+v", f)
	}
}

func TestFormat(t *testing.T) {
	for in, want := range map[float64]string{300: "300s", 1.5: "1.5s", 0.02: "20ms", 0.001: "1ms", 0: "?s"} {
		if got := FormatExposure(in); got != want {
			t.Errorf("%v: %s, want %s", in, got, want)
		}
	}
	if SafeName(`a:b?`) != "a_b_" || SafeName("CON") != "_CON" || SafeName(" M31. ") != "M31" {
		t.Error(SafeName(`a:b?`), SafeName("CON"), SafeName(" M31. "))
	}
}

func frame(kind, object, filter, night string, exp float64, gain int, temp float64) Frame {
	return Frame{Kind: kind, Object: object, Filter: filter, Night: night, Exposure: exp, Gain: gain, Temp: temp,
		Bin: 1, Camera: "ASI2600MM", Size: 10, Rel: fmt.Sprintf("%s_%s_%v_%s.fit", kind, filter, exp, night)}
}

func TestPlanAndMatch(t *testing.T) {
	nan := math.NaN()
	frames := []Frame{
		frame(Light, "M 31", "Ha", "2026-09-26", 300, 100, -10),
		frame(Dark, "", "", "2026-01-01", 300, 100, -10),
		frame(Dark, "", "", "2026-01-01", 300, 100, 0),
		frame(Dark, "", "", "2026-01-01", 120, 100, -10),
		frame(Flat, "", "Ha", "2026-09-20", 1.5, 100, nan),
		frame(Flat, "", "Ha", "2026-09-27", 2, 100, nan), // the next day: as near as the 25th, but after
		frame(Flat, "", "Ha", "2026-09-25", 2, 100, nan),
		frame(Flat, "", "OIII", "2026-09-26", 2, 100, nan),
		frame(Bias, "", "", "2026-01-01", 0.001, 100, -10),
		frame(DarkFlat, "", "", "2026-01-01", 2, 100, nan),
		{Rel: "unknown.fit"},
	}
	l := Plan(frames)
	if len(l.Sessions) != 1 || len(l.Unknown) != 1 {
		t.Fatalf("sessions %d unknown %d", len(l.Sessions), len(l.Unknown))
	}
	s := l.Sessions[0]
	if s.Dir != "Objects/M 31/2026-09-26_Ha_300s_gain100_ASI2600MM_SESSION_1" {
		t.Fatal(s.Dir)
	}
	if s.Items[0].Dest != s.Dir+"/lights/"+frames[0].Rel {
		t.Fatal(s.Items[0].Dest)
	}
	if s.Darks == nil || s.Darks.Dir != "Library/Darks/ASI2600MM/gain100_300s_-10C" {
		t.Fatalf("darks %+v", s.Darks)
	}
	if s.Flats == nil || s.Flats.Dir != "Library/Flats/ASI2600MM/2026-09-27_Ha_gain100_2s" {
		t.Fatalf("flats %+v", s.Flats)
	}
	if s.Biases == nil || s.Biases.Kind != DarkFlat {
		t.Fatalf("biases %+v", s.Biases)
	}
	if len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "другой ночи") {
		t.Fatalf("notes %v", s.Notes)
	}
	if !strings.Contains(l.Summary(), "M 31") || !strings.Contains(s.CalibrationText(), `Library\Darks`) {
		t.Fatal(l.Summary())
	}
}

func TestDuplicates(t *testing.T) {
	a := frame(Light, "M 31", "", "2026-09-26", 60, 100, -10)
	b := a
	c := a
	c.Size = 20
	l := Plan([]Frame{a, b, c})
	if !l.Items[1].Dup || l.Items[2].Dup || !strings.HasSuffix(l.Items[2].Dest, "_2.fit") {
		t.Fatalf("%+v %+v", l.Items[1], l.Items[2])
	}
}

func TestScanAndApply(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	light := map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "300", "GAIN": "100", "SET-TEMP": "-10", "FILTER": "'L'"}
	for i := 1; i <= 2; i++ {
		writeFITS(t, filepath.Join(in, "Plan", "Light", "M 31", fmt.Sprintf("Light_M 31_300.0s_Bin1_L_gain100_20260926-2210%02d_-10.0C_%04d.fit", i, i)), light)
	}
	writeFITS(t, filepath.Join(in, "Autorun", "Dark", "Dark_300.0s_Bin1_gain100_20260801-120000_-10.0C_0001.fit"),
		map[string]string{"IMAGETYP": "'Dark'", "EXPTIME": "300", "GAIN": "100", "SET-TEMP": "-10"})
	writeFITS(t, filepath.Join(in, "Autorun", "Flat", "Flat_2.0s_Bin1_L_gain100_20260927-063000_-10.0C_0001.fit"),
		map[string]string{"IMAGETYP": "'Flat'", "EXPTIME": "2", "GAIN": "100", "FILTER": "'L'"})
	writeFITS(t, filepath.Join(in, "Autorun", "Bias", "Bias_1.0ms_Bin1_gain100_20260801-120000_-10.0C_0001.fit"),
		map[string]string{"IMAGETYP": "'Bias'", "EXPTIME": "0.001", "GAIN": "100"})
	os.WriteFile(filepath.Join(in, "Light_M 31_0001_thn.jpg"), []byte("x"), 0o644)

	frames, errs := Scan(in, nil, nil)
	if len(errs) > 0 || len(frames) != 5 {
		t.Fatalf("%d frames, %v", len(frames), errs)
	}
	l := Plan(frames)
	r := l.Apply(context.Background(), out, Options{Mode: Copy, FillCalibration: true}, nil)
	if len(r.Errors) > 0 || r.Placed != 5 || r.Calibration != 3 {
		t.Fatalf("%+v", r)
	}
	sess := filepath.Join(out, "Objects", "M 31", "2026-09-26_L_300s_gain100_SESSION_1")
	for _, p := range []string{
		filepath.Join(out, "Objects", "M 31", PIDir, "L"),
		filepath.Join(sess, "lights", "Light_M 31_300.0s_Bin1_L_gain100_20260926-221001_-10.0C_0001.fit"),
		filepath.Join(sess, "darks", "Dark_300.0s_Bin1_gain100_20260801-120000_-10.0C_0001.fit"),
		filepath.Join(sess, "flats", "Flat_2.0s_Bin1_L_gain100_20260927-063000_-10.0C_0001.fit"),
		filepath.Join(sess, "biases", "Bias_1.0ms_Bin1_gain100_20260801-120000_-10.0C_0001.fit"),
		filepath.Join(sess, CalibrationFile),
		filepath.Join(sess, "process"),
		filepath.Join(out, "Library", "Flats", "2026-09-26_L_gain100_2s"),
		filepath.Join(out, "Library", "Darks", "gain100_300s_-10C"),
		filepath.Join(out, "Library", "Biases", "gain100"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}

	// again, with the library of the output scanned too: nothing new
	lib, _ := Scan(filepath.Join(out, LibraryDir), nil, nil)
	for i := range lib {
		lib[i].Rel = LibraryDir + "/" + lib[i].Rel
	}
	frames, _ = Scan(in, nil, nil)
	r = Plan(append(frames, lib...)).Apply(context.Background(), out, Options{Mode: Move}, nil)
	if len(r.Errors) > 0 || r.Placed != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(in, "Autorun", "Dark", "Dark_300.0s_Bin1_gain100_20260801-120000_-10.0C_0001.fit")); err != nil {
		t.Fatal("the original was removed although it was not moved")
	}
}

func TestInside(t *testing.T) {
	if !Inside(filepath.FromSlash("/a/b/c"), filepath.FromSlash("/a/b")) || Inside(filepath.FromSlash("/a/bc"), filepath.FromSlash("/a/b")) {
		t.Fatal()
	}
}

func TestScanForOutputInside(t *testing.T) {
	in := t.TempDir()
	out := filepath.Join(in, "sorted")
	writeFITS(t, filepath.Join(out, "Library", "Darks", "gain100_300s", "Dark_300.0s_gain100_0001.fit"),
		map[string]string{"IMAGETYP": "'Dark'", "EXPTIME": "300", "GAIN": "100"})
	writeFITS(t, filepath.Join(in, "Light", "M 31", "Light_M 31_300.0s_gain100_0001.fit"),
		map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "300", "GAIN": "100"})
	frames, errs := ScanFor(in, out, nil)
	if len(errs) > 0 || len(frames) != 2 {
		t.Fatalf("%d %v", len(frames), errs)
	}
	l := Plan(frames)
	if l.Sessions[0].Darks == nil {
		t.Fatal("the library in the output is not used")
	}
	if r := l.Apply(context.Background(), out, Options{}, nil); r.Placed != 1 || len(r.Errors) > 0 {
		t.Fatalf("%+v", r)
	}
}

func TestInPlace(t *testing.T) {
	d := frame(Dark, "", "", "2026-01-01", 300, 100, -10)
	lib := d
	lib.Rel = GroupDir(&d) + "/" + d.Rel
	lib.InLibrary = true
	l := Plan([]Frame{d, lib})
	if l.Frames() != 1 || !l.Items[1].InPlace() || l.Items[0].InPlace() {
		t.Fatalf("%d %+v", l.Frames(), l.Items[1])
	}
	if len(l.Gone()) != 0 {
		t.Fatal("a frame of the input is gone")
	}
	if g := Plan([]Frame{lib}).Gone(); len(g) != 1 || g[0].Frame.Rel != lib.Rel {
		t.Fatalf("gone %+v", g)
	}
}

func TestNights(t *testing.T) {
	nan := math.NaN()
	frames := []Frame{
		frame(Light, "M 31", "Ha", "2026-09-26", 300, 100, -10),
		frame(Light, "M 31", "Ha", "2026-09-26", 120, 100, -10),
		frame(Light, "M 31", "Ha", "2026-09-27", 300, 100, -10),
		frame(Flat, "", "Ha", "2026-09-26", 1.5, 100, nan),
		frame(Light, "NGC 7000", "", "2026-09-27", 60, 100, -10),
	}
	frames[1].Rel = "other.fit"
	rows := Plan(frames).Nights("")
	if len(rows) != 4 || rows[0].Night != "2026-09-27" || rows[2].Kind != Light || rows[2].Frames != 2 || rows[2].ExposureText() != "120s, 300s" || rows[3].Kind != Flat {
		for _, r := range rows {
			t.Logf("%+v", *r)
		}
		t.Fatal("rows")
	}
	if got := ObjectTotals(rows); got != "M 31: 3 Light, 12 мин за 2 ночи · NGC 7000: 1 Light, 1 мин за 1 ночь" {
		t.Fatal(got)
	}
	if NightLabel("2026-09-30") != "30.09 – 01.10" {
		t.Fatal(NightLabel("2026-09-30"))
	}
}

// Flats laid out before the camera was part of their folder move to the
// new folder: the ones also in the input are not copied again, the ones
// only in the library are moved too, and the old folders go away.
func TestLibraryMovesToNewLayout(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	flat := map[string]string{"IMAGETYP": "'Flat'", "EXPTIME": "2", "GAIN": "100", "FILTER": "'L'", "INSTRUME": "'ASI2600MM'"}
	name1 := "Flat_2.0s_Bin1_L_gain100_20260927-063000_-10.0C_0001.fit"
	name2 := "Flat_2.0s_Bin1_L_gain100_20260927-063001_-10.0C_0002.fit"
	old := filepath.Join(out, "Library", "Flats", "2026-09-26_L_gain100_2s")
	writeFITS(t, filepath.Join(old, name1), flat)
	writeFITS(t, filepath.Join(old, name2), flat)
	writeFITS(t, filepath.Join(in, "Autorun", "Flat", name1), flat) // still in the input

	frames, errs := ScanFor(in, out, nil)
	if len(errs) > 0 || len(frames) != 3 {
		t.Fatalf("%d %v", len(frames), errs)
	}
	l := Plan(frames)
	r := l.Apply(context.Background(), out, Options{Mode: Copy}, nil)
	if len(r.Errors) > 0 || r.Moved != 2 || r.Placed != 2 {
		t.Fatalf("%+v", r)
	}
	nu := filepath.Join(out, "Library", "Flats", "ASI2600MM", "2026-09-26_L_gain100_2s")
	for _, n := range []string{name1, name2} {
		if _, err := os.Stat(filepath.Join(nu, n)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old folder is left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, "Autorun", "Flat", name1)); err != nil {
		t.Fatal("the input frame was touched")
	}
	// again: nothing to do
	frames, _ = ScanFor(in, out, nil)
	if r := Plan(frames).Apply(context.Background(), out, Options{Mode: Copy}, nil); len(r.Errors) > 0 || r.Moved != 0 || r.Placed != 0 {
		t.Fatalf("again: %+v", r)
	}
}

// Sessions are numbered by the nights of their object; every filter of a
// night has the night's number.
func TestSessionNumbers(t *testing.T) {
	frames := []Frame{
		frame(Light, "M 31", "OIII", "2026-09-27", 300, 100, -10),
		frame(Light, "M 31", "Ha", "2026-09-26", 300, 100, -10),
		frame(Light, "M 31", "OIII", "2026-09-26", 300, 100, -10),
		frame(Light, "M 33", "", "2026-09-27", 300, 100, -10),
	}
	var got []string
	for _, s := range Plan(frames).Sessions {
		got = append(got, s.Dir)
	}
	want := []string{
		"Objects/M 31/2026-09-26_Ha_300s_gain100_ASI2600MM_SESSION_1",
		"Objects/M 31/2026-09-26_OIII_300s_gain100_ASI2600MM_SESSION_1",
		"Objects/M 31/2026-09-27_OIII_300s_gain100_ASI2600MM_SESSION_2",
		"Objects/M 33/2026-09-27_300s_gain100_ASI2600MM_SESSION_1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatal(got)
	}
	if PIFilterDir("Objects/M 33", "") != "Objects/M 33/PI/NoFilter" {
		t.Fatal(PIFilterDir("Objects/M 33", ""))
	}
}

func TestShortCamera(t *testing.T) {
	for in, want := range map[string]string{"ZWO ASI2600MM Pro": "ASI2600MM Pro", "zwo ASI533MC": "ASI533MC", "QHY268M": "QHY268M", "": ""} {
		if got := ShortCamera(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A session laid out before the camera was in its name moves whole to the
// new name: lights, calibration and Siril's results.
func TestSessionMovesToNewName(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	light := map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "300", "GAIN": "100", "FILTER": "'L'", "INSTRUME": "'ZWO ASI2600MM Pro'"}
	n1 := "Light_M 31_300.0s_Bin1_L_gain100_20260926-221000_-10.0C_0001.fit"
	n2 := "Light_M 31_300.0s_Bin1_L_gain100_20260926-221100_-10.0C_0002.fit"
	old := filepath.Join(out, "Objects", "M 31", "2026-09-26_L_300s_gain100")
	writeFITS(t, filepath.Join(old, "lights", n1), light)
	writeFITS(t, filepath.Join(old, "lights", n2), light)
	writeFITS(t, filepath.Join(old, "darks", "Dark_0001.fit"), map[string]string{"IMAGETYP": "'Dark'", "EXPTIME": "300", "GAIN": "100"})
	os.MkdirAll(filepath.Join(old, "process"), 0o755)
	os.WriteFile(filepath.Join(old, "process", "result.fit"), []byte("siril"), 0o644)
	writeFITS(t, filepath.Join(in, "Plan", "Light", "M 31", n1), light) // still in the input

	frames, errs := ScanFor(in, out, nil)
	if len(errs) > 0 || len(frames) != 3 { // the dark inside the session is not read
		t.Fatalf("%d %v", len(frames), errs)
	}
	l := Plan(frames)
	r := l.Apply(context.Background(), out, Options{Mode: Copy}, nil)
	if len(r.Errors) > 0 || r.Sessions != 1 || r.Placed != 0 {
		t.Fatalf("%+v", r)
	}
	nu := filepath.Join(out, "Objects", "M 31", "2026-09-26_L_300s_gain100_ASI2600MM Pro_SESSION_1")
	for _, p := range []string{"lights/" + n1, "lights/" + n2, "darks/Dark_0001.fit", "process/result.fit", CalibrationFile} {
		if _, err := os.Stat(filepath.Join(nu, filepath.FromSlash(p))); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old session is left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, "Plan", "Light", "M 31", n1)); err != nil {
		t.Fatal("the input frame was touched")
	}
	frames, _ = ScanFor(in, out, nil)
	if r := Plan(frames).Apply(context.Background(), out, Options{Mode: Copy}, nil); len(r.Errors) > 0 || r.Sessions != 0 || r.Placed != 0 || r.Moved != 0 {
		t.Fatalf("again: %+v", r)
	}
}

// Lights of two cameras once mixed in one session go to a session each.
func TestMixedSessionSplits(t *testing.T) {
	out := t.TempDir()
	old := filepath.Join(out, "Objects", "M 31", "2026-09-26_L_300s_gain100")
	for i, cam := range []string{"ZWO ASI2600MM Pro", "ZWO ASI294MM Pro"} {
		writeFITS(t, filepath.Join(old, "lights", fmt.Sprintf("Light_M 31_300.0s_gain100_20260926-2210%02d_%04d.fit", i, i)),
			map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "300", "GAIN": "100", "FILTER": "'L'", "INSTRUME": "'" + cam + "'"})
	}
	frames, _ := ScanLibrary(out, nil)
	r := Plan(frames).Apply(context.Background(), out, Options{}, nil)
	if len(r.Errors) > 0 || r.Moved != 2 || r.Sessions != 0 {
		t.Fatalf("%+v", r)
	}
	for i, cam := range []string{"ASI2600MM Pro", "ASI294MM Pro"} {
		if _, err := os.Stat(filepath.Join(out, "Objects", "M 31", "2026-09-26_L_300s_gain100_"+cam+"_SESSION_1", "lights", fmt.Sprintf("Light_M 31_300.0s_gain100_20260926-2210%02d_%04d.fit", i, i))); err != nil {
			t.Error(err)
		}
	}
}

func TestRemoveEmptyDirs(t *testing.T) {
	out := t.TempDir()
	lights := filepath.Join(out, "Objects", "M 31", "s1", "lights")
	os.MkdirAll(lights, 0o755)
	os.MkdirAll(filepath.Join(out, "Objects", "M 31", "s1", "darks"), 0o755)
	lib := filepath.Join(out, "Library", "Darks", "cam", "gain100_300s")
	os.MkdirAll(lib, 0o755)
	os.WriteFile(filepath.Join(out, "Library", "note.txt"), nil, 0o644)
	RemoveEmptyDirs([]string{lights, lib}, out)
	for p, want := range map[string]bool{
		lights: false, filepath.Join(out, "Objects", "M 31", "s1"): true, // the session keeps its darks
		lib: false, filepath.Join(out, "Library", "Darks"): false, filepath.Join(out, "Library"): true,
	} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", p, err == nil, want)
		}
	}
}

func TestFoldersHas(t *testing.T) {
	only := Folders{"Plan/Light/M 31", `Autorun\Flat`}
	for rel, want := range map[string]bool{
		"Plan/Light/M 31/a.fit":     true,
		`plan\light\m 31\sub\a.fit`: true,
		"Plan/Light/M 31x/a.fit":    false,
		"Plan/Light/a.fit":          false,
		"Autorun/Flat/f.fit":        true,
		"Autorun/Dark/d.fit":        false,
	} {
		if only.Has(rel) != want {
			t.Errorf("%s: %v", rel, !want)
		}
	}
	if !(Folders{}).Has("x/y.fit") {
		t.Error("no choice: all")
	}
	if !only.below("Plan") || !only.below("Plan/Light/M 31/sub") || only.below("Autorun/Dark") {
		t.Error("below")
	}
}

func TestScanForOnly(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	light := map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "60", "OBJECT": "'M 31'"}
	writeFITS(t, filepath.Join(in, "Plan", "Light", "M 31", "Light_M 31_1.fit"), light)
	writeFITS(t, filepath.Join(in, "Plan", "Light", "M 31", "Light_M 31_2.fit"), light)
	writeFITS(t, filepath.Join(in, "Plan", "Light", "M 42", "Light_M 42_1.fit"), map[string]string{"IMAGETYP": "'Light'", "EXPTIME": "60", "OBJECT": "'M 42'"})
	writeFITS(t, filepath.Join(in, "Autorun", "Flat", "Flat_1.fit"), map[string]string{"IMAGETYP": "'Flat'", "EXPTIME": "1"})

	dirs, err := LocalFolders(in, out)
	if err != nil || len(dirs) != 3 || dirs[0].Dir != "Autorun/Flat" || dirs[1].Dir != "Plan/Light/M 31" || dirs[1].Files != 2 || dirs[1].Bytes == 0 {
		t.Fatalf("folders %+v %v", dirs, err)
	}
	frames, errs := ScanForOnly(in, out, Folders{"Plan/Light/M 31"}, nil)
	if len(errs) > 0 || len(frames) != 2 {
		t.Fatalf("%d frames, %v", len(frames), errs)
	}
	for _, f := range frames {
		if f.Object != "M 31" {
			t.Fatalf("frame of %s", f.Rel)
		}
	}
	if frames, _ := ScanForOnly(in, out, nil, nil); len(frames) != 4 {
		t.Fatalf("all: %d", len(frames))
	}
}

func TestObjectsMatching(t *testing.T) {
	all := []string{"M 1", "M 101", "M 31", "NGC 7000", "IC 1396"}
	for _, c := range []struct {
		q    string
		want string
	}{
		{"m1", "M 1"},       // exact, not M 101
		{"M 31", "M 31"},    // spaces do not split the name
		{"m31", "M 31"},     // nor do they have to be typed
		{"ngc", "NGC 7000"}, // a part of the name
		{"10", "M 101"},     // likewise
		{"", "M 1,M 101,M 31,NGC 7000,IC 1396"},
		{"xyz", ""},
	} {
		if got := strings.Join(ObjectsMatching(all, c.q), ","); got != c.want {
			t.Errorf("%q: %q, want %q", c.q, got, c.want)
		}
	}
	names := []string{"M 10", "M 2", "IC 5", "m 1"}
	sort.Slice(names, func(i, j int) bool { return NaturalLess(names[i], names[j]) })
	if got := strings.Join(names, ","); got != "IC 5,m 1,M 2,M 10" {
		t.Errorf("order %q", got)
	}
}
