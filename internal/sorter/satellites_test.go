package sorter

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// the header of a frame of M 31 with its plate solution, as ASIAIR writes it
var m31Cards = []string{
	"NAXIS1  = 3008", "NAXIS2  = 3008", "FOCALLEN= 281", "XPIXSZ  = 3.76", "EXPTIME = 30.0",
	"RA      = 11.36667", "DEC     = 41.283056", "DATE-OBS= '2026-10-09T15:55:25.139872'",
	"SITELONG= 60.2952", "SITELAT = 56.7973", "CTYPE1  = 'RA---TAN-SIP'", "CTYPE2  = 'DEC--TAN-SIP'",
	"CRVAL1  = 11.2217745807", "CRVAL2  = 40.651029965", "CRPIX1  = 1012.20301692", "CRPIX2  = 1043.7744751",
	"CD1_1   = 0.000310761962424", "CD1_2   = -0.000699498213797", "CD2_1   = 0.000699492798936", "CD2_2   = 0.000311201968948",
}

const tankTLE = `BREEZE-M DEB [TANK]
1 39174U 13026C   26285.19178549  .00001711  00000+0  51168-3 0  9996
2 39174  50.7491 289.9004 5138254 228.5970  74.2821  5.34794868257164
`

func writeSatFITS(t *testing.T, name string, cards []string, data []int16) {
	t.Helper()
	var b bytes.Buffer
	for _, c := range append([]string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2"}, append(cards, "END")...) {
		fmt.Fprintf(&b, "%-80s", c)
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	if data != nil {
		binary.Write(&b, binary.BigEndian, data)
		for b.Len()%2880 != 0 {
			b.WriteByte(0)
		}
	}
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The track on a frame is found while its stars are measured, and kept in
// the cache.
func TestMeasureTracks(t *testing.T) {
	dir := t.TempDir()
	const w, h = 1600, 1200
	rnd := rand.New(rand.NewSource(5))
	d := make([]float64, w*h)
	for i := range d {
		d[i] = 2000 + rnd.NormFloat64()*40
	}
	// a track: about 3 noise sigmas, from (300,900) to (1300,200)
	for t := 0.0; t <= 1; t += 0.0002 {
		x, y := 300+1000*t, 900-700*t
		for yy := int(y) - 3; yy <= int(y)+3; yy++ {
			for xx := int(x) - 3; xx <= int(x)+3; xx++ {
				d[yy*w+xx] += 8 * math.Exp(-((float64(xx)-x)*(float64(xx)-x)+(float64(yy)-y)*(float64(yy)-y))/(2*1.5*1.5))
			}
		}
	}
	px := make([]int16, len(d))
	for i, v := range d {
		px[i] = int16(int(v) - 32768)
	}
	p := filepath.Join(dir, "Light_M31_30.0s_0001.fit")
	writeSatFITS(t, p, []string{fmt.Sprintf("NAXIS1  = %d", w), fmt.Sprintf("NAXIS2  = %d", h),
		"BZERO   = 32768", "BSCALE  = 1", "DATE-OBS= '2026-10-09T15:55:25'"}, px)
	cache := LoadQualityCache(filepath.Join(dir, "stars.json"))
	f := &Frame{Kind: Light}
	var got []Measurement
	MeasureFrames(context.Background(), []*Frame{f}, []string{p}, cache, func(m Measurement) { got = append(got, m) })
	if len(got) != 1 || len(got[0].Tracks) != 1 || got[0].start != time.Date(2026, 10, 9, 15, 55, 25, 0, time.UTC).UnixMilli() {
		t.Fatalf("%+v", got)
	}
	tr := got[0].Tracks[0]
	if math.Hypot(tr.X0-300, tr.Y0-900) > 30 && math.Hypot(tr.X1-300, tr.Y1-900) > 30 {
		t.Fatalf("track %+v", tr)
	}
	got[0].Apply()
	if !f.Measured || len(f.Tracks) != 1 || f.SatTold || f.TracksText() != "треков: 1" {
		t.Fatalf("%+v", f)
	}
	// from the cache next time
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	cache = LoadQualityCache(filepath.Join(dir, "stars.json"))
	got = nil
	MeasureFrames(context.Background(), []*Frame{f}, []string{p}, cache, func(m Measurement) { got = append(got, m) })
	if len(got) != 1 || len(got[0].Tracks) != 1 || got[0].key == "" {
		t.Fatalf("%+v", got)
	}
}

// A frame with a track is told with the catalogue nearest its time.
func TestIdentifySatellites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Light_M 31_30.0s_Bin1_533MC_uvir_gain101_20261009-205557_246deg_-10.0C_0021.fit")
	writeSatFITS(t, p, m31Cards, nil)
	cache := LoadQualityCache("")
	cat := NewSatCatalog(filepath.Join(dir, "satellites"))
	start := time.Date(2026, 10, 9, 15, 55, 25, 0, time.UTC)
	f := &Frame{Kind: Light}
	m := Measurement{Frame: f, path: p, key: "k", start: start.UnixMilli(), Stars: 900, HFR: 2,
		Tracks: []Track{{X0: 107.9, Y0: 2203.4, X1: 424.0, Y1: 1698.2}}}
	cache.put(m)
	// no catalogue and no download (tried a moment ago)
	os.MkdirAll(cat.store.Dir, 0o755)
	os.WriteFile(cat.tryFile(), nil, 0o644)
	var got []Measurement
	rep := IdentifySatellites(context.Background(), []Measurement{m}, cache, cat, nil, func(m Measurement) { got = append(got, m) })
	if rep.Frames != 1 || rep.Told != 0 || len(got) != 0 {
		t.Fatalf("%+v", rep)
	}
	far, err := cat.store.Save([]byte(tankTLE), start.Add(-20*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	os.Chtimes(cat.tryFile(), time.Now(), time.Now())
	rep = IdentifySatellites(context.Background(), []Measurement{m}, cache, cat, nil, func(m Measurement) { got = append(got, m) })
	if rep.Told != 1 || rep.Found != 1 || len(got) != 1 || got[0].satCat != filepath.Base(far) {
		t.Fatalf("%+v %+v", rep, got)
	}
	if m.Tracks[0].Sat != nil {
		t.Fatal("the measurement given was changed")
	}
	m = got[0]
	if s := m.Tracks[0].Sat; s == nil || s.NORAD != 39174 || math.Abs(s.Km-13290) > 300 {
		t.Fatalf("%+v", s)
	}
	if e := cache.m["k"]; e.SatCat != filepath.Base(far) || e.Tracks[0].Sat == nil {
		t.Fatalf("cache %+v", e)
	}
	// told already
	if rep = IdentifySatellites(context.Background(), []Measurement{m}, cache, cat, nil, func(Measurement) {}); rep.Told != 0 {
		t.Fatalf("told again: %+v", rep)
	}
	// a catalogue much nearer the frame: told again with it
	near, _ := cat.store.Save([]byte(tankTLE), start.Add(2*24*time.Hour))
	got = nil
	if rep = IdentifySatellites(context.Background(), []Measurement{m}, cache, cat, nil, func(m Measurement) { got = append(got, m) }); rep.Told != 1 || got[0].satCat != filepath.Base(near) {
		t.Fatalf("not told again with the nearer catalogue: %+v", rep)
	}
	got[0].Apply()
	if !f.SatTold || math.Abs(f.SatAge-2) > 0.1 || f.TracksText() != "BREEZE-M DEB [TANK]" ||
		!strings.Contains(f.TracksInfo(), "NORAD 39174") || f.TrackLabel(f.Tracks[0]) != "BREEZE-M DEB [TANK] (39174)" {
		t.Fatalf("%+v: %s", f, f.TracksInfo())
	}
}

// A track in the same place of the sky on two frames is not a satellite;
// a moving object on frames one after another is.
func TestStaticTracks(t *testing.T) {
	day := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC).UnixMilli()
	filament := Track{RA0: 314.0, Dec0: 30.5, RA1: 314.02, Dec1: 30.6, Sky: true, Sat: &Satellite{NORAD: 1}}
	shifted := filament
	shifted.RA0 += 0.002
	shifted.RA1 += 0.002 // the same place, 6″ off
	moving1 := Track{RA0: 10.0, Dec0: 40.0, RA1: 10.5, Dec1: 40.1, Sky: true}
	moving2 := Track{RA0: 10.5, Dec0: 40.1, RA1: 11.0, Dec1: 40.2, Sky: true}
	a := &Measurement{Frame: &Frame{}, start: day, Tracks: []Track{filament, moving1}}
	b := &Measurement{Frame: &Frame{}, start: day + 300000, Tracks: []Track{shifted, moving2}}
	later := &Measurement{Frame: &Frame{}, start: day + 10*864e5, Tracks: []Track{filament}}
	if n := markStatic([]*Measurement{a, b, later}); n != 2 {
		t.Fatalf("marked %d", n)
	}
	if !a.Tracks[0].Static || !b.Tracks[0].Static || a.Tracks[1].Static || b.Tracks[1].Static || a.Tracks[0].Sat != nil || later.Tracks[0].Static {
		t.Fatalf("%+v\n%+v", a, b)
	}
	a.Apply()
	if len(a.Frame.Tracks) != 1 || a.Frame.Tracks[0].RA0 != 10.0 {
		t.Fatalf("frame: %+v", a.Frame.Tracks)
	}
}

// The tracks the receiver told reach the frame.
func TestRemoteTracks(t *testing.T) {
	var f RemoteFile
	if err := json.Unmarshal([]byte(`{"path":"Udisk/Light_M 31_0001.fit","size":10,"local":true,"q":true,"stars":120,
		"tracks":[{"x0":1,"y0":2,"x1":300,"y1":400,"sat":{"t":0,"name":"STARLINK-1234","norad":44000,"off":0,"cross":0.01,"km":600,"alt":40,"per":540,"apo":560}},{"x0":5,"y0":6,"x1":7,"y1":8}],
		"sat_told":true,"sat_age":0.4,"flip":true}`), &f); err != nil {
		t.Fatal(err)
	}
	fr := remoteFrame(&Receiver{}, f, nil)
	if len(fr.Tracks) != 2 || !fr.SatTold || !fr.FlipRows || fr.TracksText() != "STARLINK-1234, ?" {
		t.Fatalf("%+v", fr)
	}
	if s := fr.TrackText(fr.Tracks[0]); !strings.Contains(s, "низкая орбита 540–560 км") || !strings.Contains(s, "600 км от обсерватории") {
		t.Fatal(s)
	}
}

func TestSatReport(t *testing.T) {
	if (SatReport{}).Text() != "" {
		t.Fatal("no tracks: no text")
	}
	r := SatReport{Frames: 3, Told: 2, Found: 1, CatalogErr: "нет сети"}
	if s := r.Text(); !strings.Contains(s, "не скачан (нет сети) — спутники не опознаны") {
		t.Fatal(s)
	}
}

// fakeSource gives the catalogues of a folder, as a receiver does.
type fakeSource struct {
	store   satellites.Store
	fetched []string
}

func (f *fakeSource) SatCatalogs(context.Context) ([]RemoteCatalog, error) {
	var out []RemoteCatalog
	for _, c := range f.store.Catalogs() {
		out = append(out, RemoteCatalog{Name: filepath.Base(c.Path), Time: c.Time})
	}
	return out, nil
}

func (f *fakeSource) FetchSatCatalog(_ context.Context, name string, w io.Writer) error {
	f.fetched = append(f.fetched, name)
	b, err := os.ReadFile(filepath.Join(f.store.Dir, name))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// The catalogues the receiver gathered are copied: the one nearest the
// frame, and its fresh newest one instead of a download from CelesTrak.
func TestSatCatalogsFromReceiver(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Light_M 31_30.0s_Bin1_533MC_uvir_gain101_20261009-205557_246deg_-10.0C_0021.fit")
	writeSatFITS(t, p, m31Cards, nil)
	start := time.Date(2026, 10, 9, 15, 55, 25, 0, time.UTC)
	src := &fakeSource{store: satellites.Store{Dir: filepath.Join(dir, "receiver"), Keep: 60}}
	near, _ := src.store.Save([]byte(tankTLE), start.Add(time.Hour))
	src.store.Save([]byte(tankTLE), start.Add(-10*24*time.Hour))
	fresh, _ := src.store.Save([]byte(tankTLE), time.Now().Add(-time.Hour))

	cat := NewSatCatalog(filepath.Join(dir, "satellites"))
	cat.Source = src
	// one of our own, far from the frame: the frame was told with it
	far, _ := cat.store.Save([]byte(tankTLE), start.Add(-20*24*time.Hour))
	m := Measurement{Frame: &Frame{Kind: Light}, path: p, key: "k", start: start.UnixMilli(),
		Tracks: []Track{{X0: 107.9, Y0: 2203.4, X1: 424.0, Y1: 1698.2}}, satCat: filepath.Base(far), satCatT: start.Add(-20 * 24 * time.Hour).UnixMilli()}
	cache := LoadQualityCache("")
	var got []Measurement
	rep := IdentifySatellites(context.Background(), []Measurement{m}, cache, cat, nil, func(m Measurement) { got = append(got, m) })
	if rep.Copied != 2 || rep.Downloaded != 0 || rep.CatalogErr != "" || rep.Told != 1 {
		t.Fatalf("%+v, fetched %v", rep, src.fetched)
	}
	if len(got) != 1 || got[0].satCat != filepath.Base(near) || got[0].Tracks[0].Sat == nil {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(cat.store.Dir, filepath.Base(fresh))); err != nil {
		t.Fatal("the fresh catalogue of the receiver was not copied")
	}
	if !strings.Contains(rep.Text(), "каталогов орбит взято с приёмника: 2") {
		t.Fatal(rep.Text())
	}
	// nothing more to copy
	src.fetched = nil
	if rep = IdentifySatellites(context.Background(), got, cache, cat, nil, func(Measurement) {}); rep.Copied != 0 || rep.Told != 0 || len(src.fetched) != 0 {
		t.Fatalf("%+v %v", rep, src.fetched)
	}
}

func TestPlaneFromReceiver(t *testing.T) {
	var tr Track
	if err := json.Unmarshal([]byte(`{"x0":1,"y0":2,"x1":3,"y1":4,"plane":{"icao":"4ca7b3","call":"AFL1234","km":12.5,"alt":40.2,"altm":10500,"cross":0.1}}`), &tr); err != nil {
		t.Fatal(err)
	}
	f := &Frame{Tracks: []Track{tr, {}}, SatTold: true}
	if got := f.TracksText(); got != "✈ AFL1234, ?" {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(f.TrackText(tr), "самолёт AFL1234 (ICAO 4ca7b3): высота 10500 м") {
		t.Fatal(f.TrackText(tr))
	}
}
