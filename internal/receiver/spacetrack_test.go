package receiver

import (
	"testing"
	"time"
)

func TestHistoryWanted(t *testing.T) {
	dir := t.TempDir()
	s := &Server{quality: newQuality(""), sats: newSatState(dir, "", "")}
	track := []trackEntry{{X1: 100, Y1: 100}}
	add := func(p string, at time.Time, tr []trackEntry) {
		s.quality.m[p] = qualityEntry{Start: at.UnixMilli(), TV: trackVersion, Tracks: tr}
	}
	n1 := time.Date(2026, 9, 12, 19, 0, 0, 0, time.UTC)
	add("a", n1, track)
	add("b", n1.Add(3*time.Hour), track)
	add("c", n1.Add(6*time.Hour), track)
	add("static", n1.Add(time.Hour), []trackEntry{{Static: true}})
	add("clean", n1.Add(time.Hour), nil)
	n2 := time.Date(2026, 9, 20, 20, 0, 0, 0, time.UTC)
	add("d", n2, track)
	// a frame near a catalogue downloaded: no history wanted
	recent := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	add("e", recent, track)
	s.sats.store.Save([]byte("x"), recent.Add(-4*time.Hour))

	w := s.historyWanted()
	if len(w) != 2 || !w[0].first.Equal(n2) || !w[1].first.Equal(n1) || len(w[1].starts) != 3 || !w[1].target().Equal(n1.Add(3*time.Hour)) {
		t.Fatalf("%+v", w)
	}
	// once the night of n1 is kept, only n2 is wanted
	s.sats.store.SaveHistory([]byte("x"), w[1].target())
	if w = s.historyWanted(); len(w) != 1 || !w[0].first.Equal(n2) {
		t.Fatalf("%+v", w)
	}
}
