package satellites

import (
	"context"
	"fmt"
	"sort"
	"time"
)

const (
	// HistoryGap: a frame wants the orbits of its night from Space-Track
	// when no catalogue kept is nearer its time than this
	HistoryGap = 24 * time.Hour
	// a night is one piece of the history: the frames within this of the first
	historyNight = 18 * time.Hour
	// HistoryPerRound: pieces fetched at a time (Space-Track allows 30
	// queries a minute, 300 an hour; each piece is fetched once and kept)
	HistoryPerRound = 6
)

// HistoryNight: the frames of one night that want its orbits.
type HistoryNight struct {
	First, Last time.Time
	Starts      []time.Time
}

// Target: the time the orbits are wanted for (the middle frame).
func (n HistoryNight) Target() time.Time { return n.Starts[len(n.Starts)/2] }

// Key names the night (its first hour, UTC).
func (n HistoryNight) Key() string { return n.First.UTC().Format("2006-01-02T15") }

// HistoryWanted: the nights of the frames starting at starts that have no
// catalogue of the store near their time, newest first.
func (s Store) HistoryWanted(starts []time.Time) []HistoryNight {
	cats := s.Catalogs()
	var want []time.Time
	for _, t := range starts {
		near := false
		for _, c := range cats {
			near = near || absDur(c.Time.Sub(t)) <= HistoryGap
		}
		if !near {
			want = append(want, t)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Before(want[j]) })
	var out []HistoryNight
	for _, t := range want {
		if n := len(out); n > 0 && t.Sub(out[n-1].First) <= historyNight {
			out[n-1].Last = t
			out[n-1].Starts = append(out[n-1].Starts, t)
			continue
		}
		out = append(out, HistoryNight{First: t, Last: t, Starts: []time.Time{t}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First.After(out[j].First) })
	return out
}

// FetchHistory fetches the orbits of a night with a session logged in and
// keeps them in the store; it returns the number of objects.
func (s Store) FetchHistory(ctx context.Context, sess *SpaceTrack, n HistoryNight) (int, error) {
	// the elements published a few days before the night (debris is
	// observed seldom) and the day after
	from, to := n.First.Add(-3*24*time.Hour), n.Last.Add(24*time.Hour)
	qctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	text, cnt, err := sess.History(qctx, from, to, n.Target())
	cancel()
	if err != nil {
		return 0, err
	}
	if cnt < 1000 {
		return 0, fmt.Errorf("в архиве всего %d объектов", cnt)
	}
	if _, err := s.SaveHistory(text, n.Target()); err != nil {
		return 0, err
	}
	return cnt, nil
}
