package catalog

import (
	"bufio"
	"math"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, c := range []struct {
		q, name string
		ra, dec float64
	}{
		{"M31", "NGC 224", 10.68, 41.27},
		{"m 031", "NGC 224", 10.68, 41.27},
		{"Messier 31", "NGC 224", 10.68, 41.27},
		{"NGC0224", "NGC 224", 10.68, 41.27},
		{"Андромеда", "NGC 224", 10.68, 41.27},
		{"NGC 7000", "NGC 7000", 314.82, 44.53},
		{"North America Nebula", "NGC 7000", 314.82, 44.53},
		{"Медуза", "Abell 21", 112.26, 13.25},
		{"Туманность Медуза", "Abell 21", 112.26, 13.25},
		{"Sh2-274", "Sh2-274", 112.27, 13.26},
		{"SH 2-274", "Sh2-274", 112.27, 13.26},
		{"Abell 21", "Abell 21", 112.26, 13.25},
		{"M45", "Mel 22", 56.87, 24.11},
		{"Сердце", "IC 1805", 38.17, 61.46},
		{"C 20", "NGC 7000", 314.82, 44.53},
	} {
		o, ok := Lookup(c.q)
		if !ok || o.Name != c.name || math.Abs(o.RA-c.ra) > 0.05 || math.Abs(o.Dec-c.dec) > 0.05 {
			t.Errorf("%q: %+v %v, want %s", c.q, o, ok, c.name)
		}
	}
	if o, ok := Lookup("не объект"); ok {
		t.Errorf("unknown found: %+v", o)
	}
	// every Russian name points to an object of the catalog
	for n := range russian {
		if _, ok := Lookup(n); !ok {
			t.Errorf("%s is not in the catalog", n)
		}
	}
}

func TestSearch(t *testing.T) {
	r := Search("m3", 5)
	if len(r) != 5 || r[0].Label() != "M 3" {
		t.Errorf("m3: %+v", r)
	}
	if r := Search("мед", 3); len(r) == 0 || r[0].Name != "Abell 21" && r[0].Name != "Sh2-274" {
		t.Errorf("мед: %+v", r)
	}
	if o, _ := Lookup("M31"); o.Label() != "M 31 — Andromeda Galaxy" {
		t.Errorf("label %q", o.Label())
	}
}

func TestParseSesame(t *testing.T) {
	in := "# Abell 21\n%C.0 PN\n%J 112.26125000 +13.24694444 = 07 29 02.70   +13 14 49.0\n"
	o, err := parseSesame("Abell 21", bufio.NewScanner(strings.NewReader(in)))
	if err != nil || math.Abs(o.RA-112.26) > 0.01 || math.Abs(o.Dec-13.247) > 0.01 {
		t.Errorf("%+v %v", o, err)
	}
	if _, err := parseSesame("x", bufio.NewScanner(strings.NewReader("#! *** Nothing found ***\n"))); err == nil {
		t.Error("nothing found: no error")
	}
}
