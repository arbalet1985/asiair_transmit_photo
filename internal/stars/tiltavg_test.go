package stars

import (
	"math"
	"testing"
)

func TestSummarizeTilt(t *testing.T) {
	// the bottom right corner 20% bigger on every frame, the focus changing
	// from frame to frame, one frame spoilt by the wind
	var ts []Tilt
	for i := 0; i < 10; i++ {
		k := 2 + 0.1*float64(i)
		ts = append(ts, Tilt{Corners: [4]float64{k, k, k, 1.2 * k}, Center: k, Edge: 1.1 * k, Percent: 19})
	}
	ts = append(ts, Tilt{Corners: [4]float64{4, 2, 2, 2}, Percent: 80})
	s, ok := SummarizeTilt(ts)
	if !ok || s.Frames != 11 || s.Used != 10 {
		t.Fatalf("%v %+v", ok, s)
	}
	if r := s.Corners[3] / s.Corners[0]; math.Abs(r-1.2) > 0.01 {
		t.Fatalf("ratio %v: %+v", r, s)
	}
	if b := s.Bigger(); b != "справа снизу" {
		t.Fatalf("bigger %q", b)
	}
	if _, ok := SummarizeTilt(ts[:2]); ok {
		t.Fatal("two frames summed up")
	}
	flat := []Tilt{{Corners: [4]float64{2, 2, 2, 2}}, {Corners: [4]float64{2, 2.02, 2, 2}}, {Corners: [4]float64{2, 2, 2.01, 2}}}
	if s, _ := SummarizeTilt(flat); s.Bigger() != "" {
		t.Fatalf("flat: %q", s.Bigger())
	}
}
