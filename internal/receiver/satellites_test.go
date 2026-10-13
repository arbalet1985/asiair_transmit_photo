package receiver

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// the header of a frame of M 31 with its plate solution, as ASIAIR writes it
var m31Cards = []string{
	"NAXIS1  = 3008", "NAXIS2  = 3008", "FOCALLEN= 281", "XPIXSZ  = 3.76", "EXPTIME = 30.0",
	"RA      = 11.36667", "DEC     = 41.283056", "DATE-OBS= '2026-10-09T15:55:25.139872'",
	"SITELONG= 60.2952", "SITELAT = 56.7973", "CTYPE1  = 'RA---TAN-SIP'", "CTYPE2  = 'DEC--TAN-SIP'",
	"CRVAL1  = 11.2217745807", "CRVAL2  = 40.651029965", "CRPIX1  = 1012.20301692", "CRPIX2  = 1043.7744751",
	"CD1_1   = 0.000310761962424", "CD1_2   = -0.000699498213797", "CD2_1   = 0.000699492798936", "CD2_2   = 0.000311201968948",
}

func writeHeader(t *testing.T, name string, cards []string) {
	t.Helper()
	var b bytes.Buffer
	for _, c := range append([]string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2"}, append(cards, "END")...) {
		fmt.Fprintf(&b, "%-80s", c)
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	os.MkdirAll(filepath.Dir(name), 0o755)
	if err := os.WriteFile(name, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const tankTLE = `BREEZE-M DEB [TANK]
1 39174U 13026C   26285.19178549  .00001711  00000+0  51168-3 0  9996
2 39174  50.7491 289.9004 5138254 228.5970  74.2821  5.34794868257164
`

// A frame with a track is told with the catalogue nearest its time, and
// the page shows which satellite it was.
func TestSatelliteIdentify(t *testing.T) {
	s, _ := newTestServer(t)
	root := s.getStore().root
	rel := "EMMC Images/Plan/Light/M 31/Light_M 31_30.0s_Bin1_533MC_uvir_gain101_20261009-205557_246deg_-10.0C_0021.fit"
	local := filepath.Join(root, filepath.FromSlash(rel))
	writeHeader(t, local, m31Cards)
	st, _ := os.Stat(local)
	start := time.Date(2026, 10, 9, 15, 55, 25, 0, time.UTC)
	s.quality.set(rel, qualityEntry{Size: st.Size(), MTime: st.ModTime().UnixNano(), Stars: 900, HFR: 2, V: qualityVersion,
		TV: trackVersion, Start: start.UnixMilli(), Tracks: []trackEntry{{X0: 107.9, Y0: 2203.4, X1: 424.0, Y1: 1698.2}}})

	if n := s.identifyPending(context.Background()); n != 0 {
		t.Fatalf("told %d without a catalogue", n)
	}
	far, err := s.sats.store.Save([]byte(tankTLE), start.Add(-20*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n := s.identifyPending(context.Background()); n != 1 {
		t.Fatalf("told %d", n)
	}
	e, _ := s.quality.get(rel, st.Size(), st.ModTime())
	if e.SatCat != filepath.Base(far) || len(e.Sats) != 1 || e.Sats[0].NORAD != 39174 {
		t.Fatalf("%+v", e)
	}
	if n := s.identifyPending(context.Background()); n != 0 {
		t.Fatalf("told again: %d", n)
	}
	// a catalogue much nearer the frame: told again with it
	near, _ := s.sats.store.Save([]byte(tankTLE), start.Add(2*24*time.Hour))
	if n := s.identifyPending(context.Background()); n != 1 {
		t.Fatalf("not told again with the nearer catalogue: %d", n)
	}
	e, _ = s.quality.get(rel, st.Size(), st.ModTime())
	if e.SatCat != filepath.Base(near) || len(e.Sats) != 1 {
		t.Fatalf("%+v", e)
	}

	rec := call(t, s.uiHandler(), map[string]string{}, "GET", "/ui/api/state", nil)
	var out struct {
		Files []uiFile `json:"files"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	for _, f := range out.Files {
		if f.Path != rel {
			continue
		}
		if !f.SatTold || len(f.Tracks) != 1 || f.Tracks[0].Sat == nil || f.Tracks[0].Sat.Name != "BREEZE-M DEB [TANK]" ||
			math.Abs(f.SatAge-2) > 0.1 || math.Abs(f.Tracks[0].Sat.Km-13290) > 100 {
			t.Fatalf("%+v %+v", f, f.Tracks)
		}
		return
	}
	t.Fatal("the frame is not on the page")
}

// The track on a received frame is found when it is measured.
func TestMeasureTracks(t *testing.T) {
	s, _ := newTestServer(t)
	root := s.getStore().root
	rel := "EMMC Images/Autorun/Light/M31/Light_M31_30.0s_0001.fit"
	const w, h = 1600, 1200
	rnd := rand.New(rand.NewSource(5))
	d := make([]float64, w*h)
	for i := range d {
		d[i] = 2000 + rnd.NormFloat64()*40
	}
	// a track: about 3 noise sigmas (the steps overlap), from (300,900) to (1300,200)
	for t := 0.0; t <= 1; t += 0.0002 {
		x, y := 300+1000*t, 900-700*t
		for yy := int(y) - 3; yy <= int(y)+3; yy++ {
			for xx := int(x) - 3; xx <= int(x)+3; xx++ {
				d[yy*w+xx] += 8 * math.Exp(-((float64(xx)-x)*(float64(xx)-x)+(float64(yy)-y)*(float64(yy)-y))/(2*1.5*1.5))
			}
		}
	}
	var b bytes.Buffer
	for _, c := range []string{"SIMPLE  = T", "BITPIX  = 16", "NAXIS   = 2", fmt.Sprintf("NAXIS1  = %d", w), fmt.Sprintf("NAXIS2  = %d", h),
		"BZERO   = 32768", "BSCALE  = 1", "DATE-OBS= '2026-10-09T15:55:25'", "END"} {
		fmt.Fprintf(&b, "%-80s", c)
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	for _, v := range d {
		binary.Write(&b, binary.BigEndian, int16(int(v)-32768))
	}
	for b.Len()%2880 != 0 {
		b.WriteByte(0)
	}
	local := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(local), 0o755)
	os.WriteFile(local, b.Bytes(), 0o644)
	if n := s.measurePending(context.Background()); n != 1 {
		t.Fatalf("measured %d", n)
	}
	st, _ := os.Stat(local)
	e, _ := s.quality.get(rel, st.Size(), st.ModTime())
	if len(e.Tracks) != 1 || e.TV != trackVersion || e.Start != time.Date(2026, 10, 9, 15, 55, 25, 0, time.UTC).UnixMilli() {
		t.Fatalf("%+v", e)
	}
	tr := e.Tracks[0]
	if math.Hypot(tr.X0-300, tr.Y0-900) > 30 && math.Hypot(tr.X1-300, tr.Y1-900) > 30 {
		t.Fatalf("track %+v", tr)
	}
	// the background is kept for the graphs of the night
	if math.Abs(e.Bg-2000) > 5 || e.V != qualityVersion {
		t.Fatalf("background %v, version %d", e.Bg, e.V)
	}

	// measured again by a newer version: the satellites told stay
	e.V = qualityVersion - 1
	e.Sats, e.SatCat, e.SatCatT = []satEntry{{T: 0, Name: "X", NORAD: 1}}, "catalog-20261009-1200.tle.gz", 1
	s.quality.set(rel, e)
	if n := s.measurePending(context.Background()); n != 1 {
		t.Fatalf("measured again %d", n)
	}
	e, _ = s.quality.get(rel, st.Size(), st.ModTime())
	if e.V != qualityVersion || len(e.Sats) != 1 || e.SatCat == "" {
		t.Fatalf("%+v", e)
	}
}

func TestFrameLevels(t *testing.T) {
	bg, sat, pct := frameLevels([]fits.ChannelStats{
		{Count: 1000, Median: 1000, Saturated: 5}, {Count: 1000, Median: 1200, Saturated: 15},
		{Count: 1000, Mean: 1400, NoMedian: true}})
	if bg != 1200 || sat != 20 || math.Abs(pct-0.6667) > 1e-3 {
		t.Fatal(bg, sat, pct)
	}
}

// A track in the same place of the sky on two frames is not a satellite;
// a moving object on frames one after another is.
func TestStaticTracks(t *testing.T) {
	q := newQuality(filepath.Join(t.TempDir(), "q.json"))
	day := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC).UnixMilli()
	filament := trackEntry{RA0: 314.0, Dec0: 30.5, RA1: 314.02, Dec1: 30.6, Sky: true}
	shifted := filament
	shifted.RA0 += 0.002
	shifted.RA1 += 0.002 // the same place, 6″ off
	moving1 := trackEntry{RA0: 10.0, Dec0: 40.0, RA1: 10.5, Dec1: 40.1, Sky: true}
	moving2 := trackEntry{RA0: 10.5, Dec0: 40.1, RA1: 11.0, Dec1: 40.2, Sky: true}
	q.set("a", qualityEntry{Start: day, Tracks: []trackEntry{filament, moving1}, Sats: []satEntry{{T: 0, NORAD: 1}}})
	q.set("b", qualityEntry{Start: day + 300000, Tracks: []trackEntry{shifted, moving2}})
	if n := q.markStatic(); n != 2 {
		t.Fatalf("marked %d", n)
	}
	a, b := q.m["a"], q.m["b"]
	if !a.Tracks[0].Static || !b.Tracks[0].Static || a.Tracks[1].Static || b.Tracks[1].Static || len(a.Sats) != 0 {
		t.Fatalf("%+v\n%+v", a, b)
	}
	if tr, _, _, _ := uiTracks(a); len(tr) != 1 || tr[0].RA0 != 10.0 {
		t.Fatalf("page: %+v", tr)
	}
}

// A sorter lists the catalogues of orbits kept here and copies one.
func TestSatCatalogsForSorter(t *testing.T) {
	s, _ := newTestServer(t)
	when := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p, err := s.sats.store.Save([]byte(tankTLE), when)
	if err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewServer(s.uiHandler())
	defer ui.Close()
	r, err := sorter.NewReceiver(ui.URL+"/", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := r.SatCatalogs(ctx)
	if err != nil || len(list) != 1 || list[0].Name != filepath.Base(p) || !list[0].Time.Equal(when) {
		t.Fatalf("%v %+v", err, list)
	}
	var b bytes.Buffer
	if err := r.FetchSatCatalog(ctx, list[0].Name, &b); err != nil {
		t.Fatal(err)
	}
	if want, _ := os.ReadFile(p); !bytes.Equal(b.Bytes(), want) {
		t.Fatal("the catalogue differs")
	}
	if err := r.FetchSatCatalog(ctx, "../receiver.yaml", io.Discard); err == nil {
		t.Fatal("a file outside the catalogues was sent")
	}
}
