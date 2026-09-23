package gui

import (
	"path/filepath"
	"testing"
)

func TestLogBuffer(t *testing.T) {
	b := NewLogBuffer(3)
	var got []string
	b.OnLine(func(l string) { got = append(got, l) })
	b.Write([]byte("one\ntw"))
	b.Write([]byte("o\r\nthree\nfour\n"))
	if len(got) != 4 || got[1] != "two" {
		t.Fatalf("callback lines: %q", got)
	}
	if l := b.Lines(); len(l) != 3 || l[0] != "two" || l[2] != "four" {
		t.Fatalf("buffered lines: %q", l)
	}
}

func TestSettings(t *testing.T) {
	p := SettingsPath(filepath.Join(t.TempDir(), "agent.yaml"))
	if LoadSettings(p).StartMinimized {
		t.Fatal("default must be false")
	}
	if err := SaveSettings(p, Settings{StartMinimized: true}); err != nil {
		t.Fatal(err)
	}
	if !LoadSettings(p).StartMinimized {
		t.Fatal("setting not saved")
	}
	if err := SaveSettings(p, Settings{Paused: true}); err != nil {
		t.Fatal(err)
	}
	if s := LoadSettings(p); !s.Paused || s.StartMinimized {
		t.Fatalf("pause not saved: %+v", s)
	}
}

func TestIconImage(t *testing.T) {
	for _, st := range []string{StateStopped, StateError, StateIdle, StateUploading, StatePaused} {
		img := IconImage(st, 32)
		if img.Bounds().Dx() != 32 {
			t.Fatal("wrong size")
		}
		if img.RGBAAt(0, 0).A != 0 {
			t.Fatal("corner must be transparent")
		}
		want := stateColors[st]
		if c := img.RGBAAt(6, 16); c.A != 255 || c != want {
			t.Fatalf("%s: disc colour %v, want %v", st, c, want)
		}
	}
}

func TestShorten(t *testing.T) {
	if Shorten("короткий", 10) != "короткий" {
		t.Fatal("short text changed")
	}
	if got := Shorten("абвгдежзик", 5); got != "абвг…" {
		t.Fatalf("Shorten = %q", got)
	}
	if got := ShortenTail("a/b/c/file.fit", 8); got != "…ile.fit" {
		t.Fatalf("ShortenTail = %q", got)
	}
}

func TestFormatETA(t *testing.T) {
	for sec, want := range map[float64]string{0: "", 41: "~41 с", 720: "~12 мин", 5520: "~1 ч 32 мин"} {
		if got := formatETA(sec); got != want {
			t.Errorf("formatETA(%v) = %q, want %q", sec, got, want)
		}
	}
}
