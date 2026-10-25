package planner

import "testing"

func TestSide(t *testing.T) {
	for az, want := range map[float64]uint8{0: 1, 350: 1, 23: 2, 90: 4, 180: 16, 270: 64, 314: 128, 338: 1} {
		if got := Side(az); got != want {
			t.Errorf("Side(%v) = %d, want %d", az, got, want)
		}
	}
}
