package satellites

import (
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

type vec struct {
	t time.Time
	r [3]float64
}

// positions from the sgp4 package of Python (Skyfield), WGS-72
var sgp4Cases = []struct {
	name, l1, l2 string
	want         []vec
}{
	// a highly eccentric deep space orbit
	{"BREEZE-M DEB [TANK]", "1 39174U 13026C   26285.19178549  .00001711  00000+0  51168-3 0  9996", "2 39174  50.7491 289.9004 5138254 228.5970  74.2821  5.34794868257164", []vec{
		{time.Date(2026, 10, 9, 15, 55, 25, 139872000, time.UTC), [3]float64{12537.226158, -593.908399, 13960.246779}},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), [3]float64{3849.975833, -13761.856367, -1344.808872}},
	}},
	// low, with drag
	{"STARLINK-34089", "1 64039U 25102T   26285.13785636  .00013269  00000+0  46526-3 0  9990", "2 64039  69.9988 283.5332 0002705 252.7802 107.3078 15.29577493 78507", []vec{
		{time.Date(2026, 10, 9, 15, 55, 25, 139872000, time.UTC), [3]float64{3095.305379, -2598.262267, 5532.736591}},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), [3]float64{-146.967433, -5539.385900, -4044.398414}},
	}},
	// geosynchronous: the one day resonance
	{"SYNCOM 2 (A 26)", "1 00634U 63031A   26284.60198836 -.00000022  00000+0  00000+0 0  9992", "2 00634  29.7362 298.7855 0006940 217.0729 146.8183  1.00281003231535", []vec{
		{time.Date(2026, 10, 9, 15, 55, 25, 139872000, time.UTC), [3]float64{31657.699285, -26550.941296, 8529.336412}},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), [3]float64{105.464596, 40631.886417, 11208.859999}},
	}},
	// eccentric 12 hour: the half day resonance
	{"DELTA 1 R/B", "1 00862U 64047B   26284.40583010 -.00000399  00000+0  00000+0 0  9990", "2 00862  16.6035 301.0407 7109704  86.6455 345.0517  2.05100357260541", []vec{
		{time.Date(2026, 10, 9, 15, 55, 25, 139872000, time.UTC), [3]float64{-39753.094928, -11742.336754, -11933.862517}},
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), [3]float64{-29377.243197, 4758.836039, -6799.197834}},
	}},
}

func tle(name, l1, l2 string) string { return name + "\n" + l1 + "\n" + l2 + "\n" }

func TestSGP4(t *testing.T) {
	for _, c := range sgp4Cases {
		orbits, err := ParseTLE(strings.NewReader(tle(c.name, c.l1, c.l2)))
		if err != nil || len(orbits) != 1 {
			t.Fatalf("%s: %v %d", c.name, err, len(orbits))
		}
		o := orbits[0]
		if o.Name != c.name {
			t.Errorf("name %q", o.Name)
		}
		for _, v := range c.want {
			r, err := o.TEME(v.t)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if d := math.Sqrt(sq(r[0]-v.r[0]) + sq(r[1]-v.r[1]) + sq(r[2]-v.r[2])); d > 0.001 {
				t.Errorf("%s at %v: %v, want %v (%.4f km off)", c.name, v.t, r, v.r, d)
			}
		}
	}
}

func sq(v float64) float64 { return v * v }

func TestParseTLE(t *testing.T) {
	c := sgp4Cases[0]
	orbits, _ := ParseTLE(strings.NewReader("0 " + tle(c.name, c.l1, c.l2) + "garbage\n1 short\n"))
	if len(orbits) != 1 {
		t.Fatalf("%d orbits", len(orbits))
	}
	o := orbits[0]
	if o.NORAD != 39174 || o.IntlID != "2013-026C" || o.Name != "BREEZE-M DEB [TANK]" {
		t.Fatalf("%+v", o)
	}
	if p, a := o.PerigeeKm(), o.ApogeeKm(); math.Abs(p-337) > 2 || math.Abs(a-14533) > 2 {
		t.Fatalf("perigee %.0f apogee %.0f", p, a)
	}
	if want := time.Date(2026, 10, 12, 4, 36, 10, 0, time.UTC); o.Epoch.Sub(want).Abs() > time.Second {
		t.Fatalf("epoch %v", o.Epoch)
	}
	if n, err := alpha5("A0001"); err != nil || n != 100001 {
		t.Fatalf("alpha5 %d %v", n, err)
	}
	if v, _ := expField("-11606-4"); math.Abs(v+0.11606e-4) > 1e-12 {
		t.Fatalf("exp %v", v)
	}
}

// the header of a frame of M 31 taken at Kourovka (as ASIAIR writes it)
var m31 = map[string]string{
	"NAXIS1": "3008", "NAXIS2": "3008", "FOCALLEN": "281", "XPIXSZ": "3.75999999046326", "EXPTIME": "30.0",
	"RA": "11.36667", "DEC": "41.283056", "DATE-OBS": "'2026-10-09T15:55:25.139872'", "SITELONG": "60.2952",
	"SITELAT": "56.7973", "ROTATOR": "246", "CTYPE1": "'RA---TAN-SIP'", "CTYPE2": "'DEC--TAN-SIP'",
	"CRVAL1": "11.2217745807", "CRVAL2": "40.651029965", "CRPIX1": "1012.20301692", "CRPIX2": "1043.7744751",
	"CD1_1": "0.000310761962424", "CD1_2": "-0.000699498213797", "CD2_1": "0.000699492798936", "CD2_2": "0.000311201968948",
	"A_ORDER": "2", "B_ORDER": "2", "AP_ORDER": "2", "BP_ORDER": "2",
	"A_0_2": "5.6703520399E-08", "A_1_1": "1.46907246467E-08", "A_2_0": "1.20657543528E-07",
	"B_0_2": "-1.01255470584E-07", "B_1_1": "1.30821083637E-07", "B_2_0": "-4.6138515153E-07",
	"AP_0_0": "-3.49639025912E-06", "AP_0_1": "-4.64602770126E-08", "AP_0_2": "-5.67047817195E-08",
	"AP_1_0": "5.27407078507E-08", "AP_1_1": "-1.47100071793E-08", "AP_2_0": "-1.20645324823E-07",
	"BP_0_0": "8.31832480521E-05", "BP_0_1": "1.271958406E-07", "BP_0_2": "1.01249588683E-07",
	"BP_1_0": "-2.28690868757E-07", "BP_1_1": "-1.30793047836E-07", "BP_2_0": "4.61183736231E-07",
}

func header(t *testing.T, cards map[string]string, drop ...string) *fits.Header {
	t.Helper()
	var b bytes.Buffer
	for _, c := range []string{"SIMPLE  =                    T", "BITPIX  =                   16", "NAXIS   =                    2"} {
		fmt.Fprintf(&b, "%-80s", c)
	}
next:
	for k, v := range cards {
		for _, d := range drop {
			if strings.HasPrefix(k, d) {
				continue next
			}
		}
		fmt.Fprintf(&b, "%-80s", fmt.Sprintf("%-8s= %s", k, v))
	}
	fmt.Fprintf(&b, "%-80s", "END")
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	h, err := fits.ReadHeader(&b)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// the WCS agrees with astropy, and goes back and forth
func TestWCS(t *testing.T) {
	w, ok := fits.ReadWCS(header(t, m31), "x.fit")
	if !ok || w.Approx {
		t.Fatal("no solution read")
	}
	for _, c := range []struct{ x, y, ra, dec float64 }{
		{130, 2172, 9.8261607, 40.3775783},
		{2000, 500, 12.1348508, 41.1700711},
		{1503.5, 1503.5, 10.9971139, 41.1385358},
	} {
		ra, dec := w.PixelToWorld(c.x, c.y)
		if math.Abs(ra-c.ra) > 1e-5 || math.Abs(dec-c.dec) > 1e-5 {
			t.Errorf("(%v,%v): %.7f %.7f, want %.7f %.7f", c.x, c.y, ra, dec, c.ra, c.dec)
		}
		x, y, ok := w.WorldToPixel(fits.Direction(ra, dec))
		if !ok || math.Hypot(x-c.x, y-c.y) > 0.05 {
			t.Errorf("back: (%v,%v) → (%.3f,%.3f)", c.x, c.y, x, y)
		}
	}
	if s := w.Scale() * 3600; math.Abs(s-2.756) > 0.01 {
		t.Errorf("scale %.3f″", s)
	}
	// without the solution: from the pointing and the camera angle,
	// within a degree of the solution, turned the same way
	a, ok := fits.ReadWCS(header(t, m31, "CRVAL", "CRPIX", "CD", "A_", "B_", "AP_", "BP_", "ROTATOR"), "Light_M 31_30.0s_Bin1_533MC_uvir_gain101_20261009-205557_246deg_-10.0C_0021.fit")
	if !ok || !a.Approx {
		t.Fatal("no WCS from the pointing")
	}
	r1, d1 := w.PixelToWorld(1503.5, 1503.5)
	r2, d2 := a.PixelToWorld(1503.5, 1503.5)
	if math.Hypot((r1-r2)*math.Cos(d1*math.Pi/180), d1-d2) > 1 {
		t.Errorf("centre %.3f %.3f vs %.3f %.3f", r2, d2, r1, d1)
	}
	turn := func(w *fits.WCS) float64 { return math.Atan2(w.CD[1][0], w.CD[0][0]) * 180 / math.Pi }
	if d := turn(w) - turn(a); math.Abs(d) > 1 {
		t.Errorf("turned %.2f° off", d)
	}
}

// the tank seen on that frame is told among other objects
func TestIdentify(t *testing.T) {
	h := header(t, m31)
	f, err := FrameFromHeader(h, "x.fit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Exposure != 30*time.Second || f.Site.Lat != 56.7973 || f.Start.Second() != 25 {
		t.Fatalf("%+v", f)
	}
	var text string
	for _, c := range sgp4Cases {
		text += tle(c.name, c.l1, c.l2)
	}
	orbits, _ := ParseTLE(strings.NewReader(text))
	track := Streak{X0: 107.9, Y0: 2203.4, X1: 424.0, Y1: 1698.2}
	ms := Identify(f, []Streak{track}, orbits)
	if len(ms) != 1 || ms[0].Orbit.NORAD != 39174 {
		t.Fatalf("%+v", ms)
	}
	m := ms[0]
	if math.Abs(m.Offset) > 10 || m.Cross > 0.05 || m.Angle > 1 || math.Abs(m.RangeKm-13290) > 100 || math.Abs(m.Alt-52.6) > 1 {
		t.Fatalf("%+v", m)
	}
	// turned across its path: nothing
	cross := Streak{X0: 266 - 250, Y0: 1950 - 160, X1: 266 + 250, Y1: 1950 + 160}
	if ms := Identify(f, []Streak{cross}, orbits); len(ms) != 0 {
		t.Fatalf("a track across the path told: %+v", ms[0])
	}
	// a minute later the tank is past the frame
	f.Start = f.Start.Add(3 * time.Minute)
	if ms := Identify(f, []Streak{track}, orbits); len(ms) != 0 {
		t.Fatalf("told 3 minutes off: %+v", ms[0])
	}
}

// synthImage: a mono preview with noise, stars and optionally a track.
func synthImage(withTrack bool) *fits.Image {
	const w, h = 1500, 1000
	rnd := rand.New(rand.NewSource(3))
	d := make([]float32, w*h)
	for i := range d {
		d[i] = 0.1 + 0.002*float32(rnd.NormFloat64()) + 0.02*float32(i%w)/w // a gradient too
	}
	star := func(cx, cy, amp, s float64) {
		for y := int(cy) - 10; y <= int(cy)+10; y++ {
			for x := int(cx) - 10; x <= int(cx)+10; x++ {
				if x >= 0 && y >= 0 && x < w && y < h {
					d[y*w+x] += float32(amp * math.Exp(-(sq(float64(x)-cx)+sq(float64(y)-cy))/(2*s*s)))
				}
			}
		}
	}
	for i := 0; i < 400; i++ {
		star(rnd.Float64()*w, rnd.Float64()*h, 0.01+0.3*math.Pow(rnd.Float64(), 3), 1.5)
	}
	if withTrack {
		// faint: 1.5 noise sigmas per pixel, from (200,800) to (900,300)
		for t := 0.0; t <= 1; t += 0.0005 {
			x, y := 200+700*t, 800-500*t
			star(x, y, 0.0007, 1.2)
		}
	}
	return &fits.Image{Width: w, Height: h, Channels: 1, Data: d, Binning: 2}
}

func TestFindStreaks(t *testing.T) {
	if st := FindStreaks(synthImage(false)); len(st) != 0 {
		t.Fatalf("tracks on stars only: %+v", st)
	}
	st := FindStreaks(synthImage(true))
	if len(st) != 1 {
		t.Fatalf("%d tracks: %+v", len(st), st)
	}
	s := st[0]
	// in pixels of the frame (binned by 2): (400,1600) to (1800,600)
	a, b := [2]float64{s.X0, s.Y0}, [2]float64{s.X1, s.Y1}
	if a[0] > b[0] {
		a, b = b, a
	}
	if math.Hypot(a[0]-400, a[1]-1600) > 30 || math.Hypot(b[0]-1800, b[1]-600) > 30 || s.Edge0 || s.Edge1 {
		t.Fatalf("%+v", s)
	}
}

func TestMerge(t *testing.T) {
	c := sgp4Cases[0]
	older := strings.Replace(c.l1, "26285.19178549", "26280.19178549", 1)
	text, n := Merge([]byte(tle(c.name, older, c.l2)), []byte(tle(c.name, c.l1, c.l2)+tle(sgp4Cases[1].name, sgp4Cases[1].l1, sgp4Cases[1].l2)))
	if n != 2 || !strings.Contains(string(text), c.l1) || strings.Contains(string(text), older) {
		t.Fatalf("%d\n%s", n, text)
	}
}

func TestStore(t *testing.T) {
	s := Store{Dir: t.TempDir(), Keep: 2}
	c := sgp4Cases[1]
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := s.Save([]byte(tle(c.name, c.l1, c.l2)), day.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	cats := s.Catalogs()
	if len(cats) != 2 || !cats[0].Time.Equal(day.Add(24*time.Hour)) {
		t.Fatalf("%+v", cats)
	}
	if n, _ := s.Nearest(day.Add(30 * time.Hour)); !n.Time.Equal(cats[0].Time) {
		t.Fatalf("nearest %v", n.Time)
	}
	o, err := Load(cats[1].Path)
	if err != nil || len(o) != 1 || o[0].NORAD != 64039 {
		t.Fatalf("%v %v", o, err)
	}
}
