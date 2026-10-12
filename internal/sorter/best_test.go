package sorter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bestFrames: lights of M 31 in Ha on two nights with the HFR and stars
// given, one in OIII and one not measured.
func bestFrames() []Frame {
	var frames []Frame
	add := func(night, filter string, i int, hfr float64, stars int, measured bool) {
		f := frame(Light, "M 31", filter, night, 300, 100, -10)
		f.Rel = fmt.Sprintf("Light_%s_%s_%02d.fit", night, filter, i)
		f.Measured, f.HFR, f.Stars = measured, hfr, stars
		frames = append(frames, f)
	}
	for i, q := range []struct {
		hfr   float64
		stars int
	}{{2.0, 100}, {2.1, 100}, {3.0, 100}, {2.0, 40}} {
		add("2026-09-26", "Ha", i, q.hfr, q.stars, true)
	}
	add("2026-09-27", "Ha", 0, 1.9, 90, true)
	add("2026-09-27", "Ha", 1, 0, 0, false)
	add("2026-09-27", "OIII", 0, 1.5, 100, true)
	frames = append(frames, frame(Dark, "", "", "2026-01-01", 300, 100, -10))
	return frames
}

func TestBestScore(t *testing.T) {
	if BestScore(2, 100, 100) != 2 || BestScore(2, 50, 100) != 4 || BestScore(2, 0, 0) != 2 {
		t.Fatal(BestScore(2, 50, 100))
	}
}

func TestBest(t *testing.T) {
	l := Plan(bestFrames())
	obj := "Objects/M 31"
	if s := l.BestSessions(obj, "ha"); len(s) != 2 {
		t.Fatalf("sessions %d", len(s))
	}
	p := l.Best(BestQuery{Object: obj, Filter: "Ha", Percent: 40})
	// 6 frames, one not measured; of 5 the best 40% = 2: 1.9 (90 of 100
	// stars: 2.28) loses to 2.0 and 2.1
	if p.All != 6 || p.Unmeasured != 1 || p.Nmax != 100 || len(p.Keep) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Keep[0].Frame.HFR != 2.0 || p.Keep[1].Frame.HFR != 2.1 || p.Threshold != 2.1 {
		t.Fatalf("kept %v %v, threshold %v", p.Keep[0].Frame.HFR, p.Keep[1].Frame.HFR, p.Threshold)
	}
	// only the second night
	p = l.Best(BestQuery{Object: obj, Filter: "Ha", Sessions: []int{2}, Percent: 100})
	if p.All != 2 || len(p.Keep) != 1 || len(p.Sessions) != 1 || p.Keep[0].Frame.HFR != 1.9 {
		t.Fatalf("%+v", p)
	}
	if p := l.Best(BestQuery{Object: obj, Filter: "Ha", Percent: 0}); len(p.Keep) != 0 {
		t.Fatal("0% kept frames")
	}
	if BestDir("Objects/M 31/x_SESSION_1") != "Stars/M 31/x_SESSION_1" {
		t.Fatal(BestDir("Objects/M 31/x_SESSION_1"))
	}
}

func TestApplyBest(t *testing.T) {
	out := t.TempDir()
	frames := bestFrames()
	l := Plan(frames)
	// lay out the frames: files in their places of the output
	for _, it := range l.Items {
		p := filepath.Join(out, filepath.FromSlash(it.Dest))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, it.Frame.Size), 0o644)
	}
	obj := "Objects/M 31"
	// a pick made before, of a session not chosen now: removed
	old := filepath.Join(out, filepath.FromSlash(BestDir(l.BestSessions(obj, "Ha")[1].Dir)), "lights", "old.fit")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("x"), 0o644)
	// the pick of another filter stays
	oiii := filepath.Join(out, filepath.FromSlash(BestDir(l.BestSessions(obj, "OIII")[0].Dir)), "lights", "keep.fit")
	os.MkdirAll(filepath.Dir(oiii), 0o755)
	os.WriteFile(oiii, []byte("x"), 0o644)

	q := BestQuery{Object: obj, Filter: "Ha", Sessions: []int{1}, Percent: 50}
	p := l.Best(q)
	r := l.ApplyBest(context.Background(), out, q, p, nil)
	if len(r.Errors) > 0 || r.Placed != 2 || r.NotLaidOut != 0 || r.Calibration != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the pick before stays")
	}
	if _, err := os.Stat(oiii); err != nil {
		t.Fatal("the pick of another filter was removed")
	}
	sess := filepath.Join(out, filepath.FromSlash(BestDir(p.Sessions[0].Dir)))
	lights, _ := os.ReadDir(filepath.Join(sess, "lights"))
	darks, _ := os.ReadDir(filepath.Join(sess, "darks"))
	if len(lights) != 2 || len(darks) != 1 {
		t.Fatalf("lights %d darks %d", len(lights), len(darks))
	}
	for _, d := range append(SessionDirs, CalibrationFile) {
		if _, err := os.Stat(filepath.Join(sess, d)); err != nil {
			t.Error(err)
		}
	}
	note, err := os.ReadFile(filepath.Join(out, StarsDir, "M 31", BestFile("Ha")))
	if err != nil || !strings.Contains(string(note), "Взято кадров: 2 из 4") {
		t.Fatalf("%s %v", note, err)
	}
	// not laid out yet: counted, nothing placed, the pick before stays
	os.RemoveAll(filepath.Join(out, ObjectsDir))
	if r := l.ApplyBest(context.Background(), out, q, p, nil); r.Placed != 0 || r.NotLaidOut != 2 {
		t.Fatalf("%+v", r)
	}
	if lights, _ := os.ReadDir(filepath.Join(sess, "lights")); len(lights) != 2 {
		t.Fatal("the pick before was removed")
	}
}

func TestToMeasure(t *testing.T) {
	dir := t.TempDir()
	disk := frame(Light, "M 31", "Ha", "2026-09-26", 300, 100, -10)
	disk.Path = filepath.Join(dir, "a.fit")
	done := disk
	done.Rel, done.Measured = "b.fit", true
	remote := disk
	remote.Rel, remote.Path, remote.Src = "c.fit", "Udisk/c.fit", &Receiver{}
	far := remote
	far.Rel = "d.fit"
	l := Plan([]Frame{disk, done, remote, far})
	// the frame of the receiver downloaded already
	for _, it := range l.Items {
		if it.Frame.Rel == "c.fit" {
			p := filepath.Join(dir, filepath.FromSlash(it.Dest))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, make([]byte, 10), 0o644)
		}
	}
	frames, paths := l.ToMeasure(dir)
	if len(frames) != 2 || paths[0] != disk.Path || !strings.HasSuffix(paths[1], "c.fit") {
		t.Fatalf("%v", paths)
	}
}

// TestRemoteQuality: the stars the receiver measured reach the frame.
func TestRemoteQuality(t *testing.T) {
	var f RemoteFile
	if err := json.Unmarshal([]byte(`{"path":"Udisk/Light_M 31_0001.fit","size":10,"local":true,"q":true,"hfr":2.35,"stars":120}`), &f); err != nil {
		t.Fatal(err)
	}
	fr := remoteFrame(&Receiver{}, f, nil)
	if !fr.Measured || fr.HFR != 2.35 || fr.Stars != 120 {
		t.Fatalf("%+v", fr)
	}
}
