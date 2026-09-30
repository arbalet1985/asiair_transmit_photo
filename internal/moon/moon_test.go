package moon

import (
	"math"
	"testing"
	"time"
)

func TestPhase(t *testing.T) {
	full := At(time.Date(2024, 1, 25, 17, 54, 0, 0, time.UTC), 0, 0, false)
	if full.Illum < 0.98 {
		t.Errorf("full moon lit %.2f", full.Illum)
	}
	if n := At(time.Date(2024, 1, 11, 11, 57, 0, 0, time.UTC), 0, 0, false); n.Illum > 0.02 {
		t.Errorf("new moon lit %.2f", n.Illum)
	}
	if q := At(time.Date(2024, 1, 18, 3, 53, 0, 0, time.UTC), 0, 0, false); !q.Waxing || math.Abs(q.Illum-0.5) > 0.05 {
		t.Errorf("first quarter: %+v", q)
	}
}

// Around the full moon it culminates near the south at midnight.
func TestPosition(t *testing.T) {
	// Moscow, full moon of 2024-01-25; local midnight = 21:00 UTC
	m := At(time.Date(2024, 1, 25, 21, 0, 0, 0, time.UTC), 55.75, 37.62, true)
	if !m.HasPos || m.Az < 150 || m.Az > 210 || m.Alt < 40 || m.Alt > 60 {
		t.Errorf("midnight full moon: %+v", m)
	}
}

func TestCompass(t *testing.T) {
	for az, want := range map[float64]string{0: "С", 350: "С", 44: "СВ", 90: "В", 225: "ЮЗ", 300: "СЗ"} {
		if got := Compass(az); got != want {
			t.Errorf("%v: %s, want %s", az, got, want)
		}
	}
}

// Polaris stays at the height of the latitude, in the north.
func TestObject(t *testing.T) {
	p := Object(time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC), 55.75, 37.62, 37.95, 89.26)
	if math.Abs(p.Alt-55.75) > 1.5 || p.Az > 3 && p.Az < 357 {
		t.Errorf("Polaris: %+v", p)
	}
	if s := Separation(Pos{0, 0}, Pos{0, 90}); math.Abs(s-90) > 1e-9 {
		t.Errorf("separation %v", s)
	}
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if ra, dec, ok := Target(get(map[string]string{"RA": "11.35", "DEC": "41.29"})); !ok || ra != 11.35 || dec != 41.29 {
		t.Errorf("RA/DEC: %v %v %v", ra, dec, ok)
	}
	if ra, dec, ok := Target(get(map[string]string{"OBJCTRA": "'00 42 44'", "OBJCTDEC": "'+41 16 09'"})); !ok || math.Abs(ra-10.68) > 0.01 || math.Abs(dec-41.27) > 0.01 {
		t.Errorf("OBJCTRA: %v %v %v", ra, dec, ok)
	}
}
