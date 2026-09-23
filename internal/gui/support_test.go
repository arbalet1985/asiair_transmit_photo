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
}

func TestIconImage(t *testing.T) {
	for _, st := range []string{StateStopped, StateError, StateIdle, StateUploading} {
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
