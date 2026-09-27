package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/source"
)

func TestFrameKind(t *testing.T) {
	for name, want := range map[string]string{
		"EMMC Images/Autorun/Light/M31/Light_M31_300.0s_Bin1_0001.fit": "light",
		"Dark_300.0s_Bin1_0001.fit":                                    "dark",
		"FLAT_1.2ms_0001.fit":                                          "flat",
		"x/Bias_0.0s.fit":                                              "bias",
		"DarkFlat_1.2ms_0001.fit":                                      "darkflat",
		"Lighthouse_M42.jpg":                                           "",
		"Light/Stack_M31.fit":                                          "", // the folder does not count
	} {
		if got := FrameKind(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestNextOrder(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC)
	q := []source.File{ // oldest first, as scan leaves it
		{Path: "Dark_1.fit", ModTime: t0},
		{Path: "Light_M31_1.fit", ModTime: t0.Add(time.Minute)},
		{Path: "Flat_1.fit", ModTime: t0.Add(2 * time.Minute)},
		{Path: "Light_M31_2.fit", ModTime: t0.Add(3 * time.Minute)},
	}
	a := &Agent{queue: q, active: map[string]*upload{}, sent: map[string]sentInfo{}, retryAt: map[string]time.Time{},
		fetch: map[string]bool{}, deleted: map[string]bool{}}
	for order, want := range map[string]string{
		protocol.OrderOldest: "Dark_1.fit",
		protocol.OrderNewest: "Light_M31_2.fit",
		protocol.OrderLights: "Light_M31_1.fit",
	} {
		a.order = order
		if f, ok := a.next(false); !ok || f.Path != want {
			t.Errorf("order %q: %s", order, f.Path)
		}
	}
	// no Light left: calibration frames go
	a.order = protocol.OrderLights
	a.active["Light_M31_1.fit"], a.active["Light_M31_2.fit"] = &upload{}, &upload{}
	if f, _ := a.next(false); f.Path != "Dark_1.fit" {
		t.Errorf("after lights: %s", f.Path)
	}
	// requested files still go first
	a.fetch["Flat_1.fit"] = true
	if f, _ := a.next(false); f.Path != "Flat_1.fit" {
		t.Errorf("fetch: %s", f.Path)
	}
}

func TestUpcoming(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC)
	q := []source.File{
		{Path: "Dark_1.fit", ModTime: t0},
		{Path: "Light_M31_1.fit", ModTime: t0.Add(time.Minute)},
		{Path: "Flat_1.fit", ModTime: t0.Add(2 * time.Minute)},
		{Path: "Light_M31_2.fit", ModTime: t0.Add(3 * time.Minute)},
		{Path: "Light_M31_3.fit", ModTime: t0.Add(4 * time.Minute)},
	}
	a := &Agent{queue: q, active: map[string]*upload{}, sent: map[string]sentInfo{}, retryAt: map[string]time.Time{},
		fetch: map[string]bool{}, deleted: map[string]bool{}}
	names := func(fs []source.File) string {
		var s []string
		for _, f := range fs {
			s = append(s, f.Path)
		}
		return strings.Join(s, " ")
	}
	for order, want := range map[string]string{
		protocol.OrderOldest: "Dark_1.fit Light_M31_1.fit Flat_1.fit",
		protocol.OrderNewest: "Light_M31_3.fit Light_M31_2.fit Flat_1.fit",
		protocol.OrderLights: "Light_M31_1.fit Light_M31_2.fit Light_M31_3.fit",
	} {
		a.order = order
		if got := names(a.upcomingLocked(false, 3)); got != want {
			t.Errorf("order %q: %s", order, got)
		}
	}
	// files being sent, waiting for a retry or requested
	a.order = protocol.OrderLights
	a.active["Light_M31_1.fit"] = &upload{}
	a.retryAt["Light_M31_2.fit"] = time.Now().Add(time.Minute)
	a.fetch["Flat_1.fit"] = true
	if got := names(a.upcomingLocked(false, 3)); got != "Flat_1.fit Light_M31_3.fit Dark_1.fit" {
		t.Errorf("mixed: %s", got)
	}
	if got := names(a.upcomingLocked(true, 3)); got != "Flat_1.fit" {
		t.Errorf("paused: %s", got)
	}
}
