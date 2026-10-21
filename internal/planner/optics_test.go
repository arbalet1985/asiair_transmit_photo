package planner

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestOpticsFOV(t *testing.T) {
	// ASI533MC on 400 mm with a 0.8× reducer
	o := Optics{Focal: 400, Reducer: 0.8, Pixel: 3.76, Width: 3008, Height: 3008}
	w, h := o.FOV()
	if math.Abs(w-2.0245) > 0.001 || w != h {
		t.Fatalf("%v × %v", w, h)
	}
	o.Reducer = 0 // none
	if w, _ := o.FOV(); math.Abs(w-1.6196) > 0.001 {
		t.Fatalf("%v", w)
	}
	if w, h := (Optics{Focal: 400}).FOV(); w != 0 || h != 0 {
		t.Fatal("no pixel: a field")
	}
}

func TestSetupsOf(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)
	fr := []SetupFrame{
		{Camera: "ASI533MC", Focal: 400, Pixel: 3.76, Width: 3008, Height: 3008, Time: t0},
		{Camera: "ASI533MC", Focal: 400, Pixel: 3.76, Width: 3008, Height: 3008, Time: t0.Add(time.Hour)},
		{Camera: "ASI2600MM", Focal: 800, Pixel: 3.76, Width: 6248, Height: 4176, Time: t0.Add(48 * time.Hour)},
		{Camera: "ASI533MC", Width: 3008, Height: 3008, Time: t0.Add(72 * time.Hour)}, // no focal length
	}
	s := SetupsOf(fr)
	if len(s) != 2 || s[0].Camera != "ASI2600MM" || s[1].Frames != 2 || !s[1].Last.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", s)
	}
}

// What was saved before the atlas is read as before: no angle, no optics.
func TestTargetAngleCompat(t *testing.T) {
	var tg Target
	if err := json.Unmarshal([]byte(`{"name":"M 31","ra":10.68,"dec":41.27,"exposure":300,"priority":1}`), &tg); err != nil || tg.Angle != 0 {
		t.Fatal(err, tg)
	}
	b, _ := json.Marshal(Target{Name: "M 31"})
	if string(b) != `{"name":"M 31","ra":0,"dec":0,"exposure":0,"priority":0}` {
		t.Fatal(string(b))
	}
}
