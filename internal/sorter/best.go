package sorter

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StarsDir is the top folder of the best frames picked (Layout.Best): the
// same folders as Objects, with only the best lights of the sessions chosen.
const StarsDir = "Stars"

// BestFile is the note of the last pick of a filter, in the object's
// folder of Stars.
func BestFile(filter string) string {
	f := NoFilter
	if strings.TrimSpace(filter) != "" {
		f = SafeName(filter)
	}
	return "отбор_" + f + ".txt"
}

// BestQuery: which frames to pick from.
type BestQuery struct {
	Object   string  // folder of the object: Objects/<object>
	Filter   string  // "" = lights without a filter
	Sessions []int   // numbers of the sessions (nights); none = all
	Percent  float64 // share of the best frames to keep, 0..100
}

// BestPick is the result of Layout.Best.
type BestPick struct {
	Keep       []*Item // the best frames, best first
	All        int     // frames of the sessions chosen
	Unmeasured int     // of them without stars measured: not picked
	// Score of the worst frame kept (lower is better), Nmax the most
	// stars on a frame
	Threshold float64
	Nmax      int
	Sessions  []*Group // the session folders the frames come from
}

// BestScore rates a frame as Siril's weighted FWHM does: the size of the
// stars, raised when stars are missing (clouds, haze) against the frame
// with the most stars. Lower is better.
func BestScore(hfr float64, nstars, nmax int) float64 {
	if nmax <= 0 {
		return hfr
	}
	return hfr * (1 + 2*float64(nmax-nstars)/float64(nmax))
}

// ObjectDir is the folder of the object a session belongs to.
func (g *Group) ObjectDir() string { return path.Dir(g.Dir) }

// BestSessions lists the light sessions of the object with the filter.
func (l *Layout) BestSessions(object, filter string) []*Group {
	var out []*Group
	for _, g := range l.Sessions {
		if g.ObjectDir() == object && strings.EqualFold(g.Filter, filter) {
			out = append(out, g)
		}
	}
	return out
}

// Best picks the best frames of the sessions chosen, all of them rated
// together (one threshold, as Siril's filters do).
func (l *Layout) Best(q BestQuery) BestPick {
	var p BestPick
	want := map[int]bool{}
	for _, n := range q.Sessions {
		want[n] = true
	}
	var items []*Item
	for _, g := range l.BestSessions(q.Object, q.Filter) {
		if len(want) > 0 && !want[g.Session] {
			continue
		}
		p.Sessions = append(p.Sessions, g)
		for _, it := range g.Items {
			if it.Dup {
				continue
			}
			p.All++
			if !it.Frame.Measured || it.Frame.Stars == 0 || it.Frame.HFR <= 0 {
				p.Unmeasured++
				continue
			}
			items = append(items, it)
			p.Nmax = max(p.Nmax, it.Frame.Stars)
		}
	}
	score := func(it *Item) float64 { return BestScore(it.Frame.HFR, it.Frame.Stars, p.Nmax) }
	sort.SliceStable(items, func(i, j int) bool { return score(items[i]) < score(items[j]) })
	n := int(math.Ceil(math.Min(math.Max(q.Percent, 0), 100) / 100 * float64(len(items))))
	p.Keep = items[:n]
	if n > 0 {
		p.Threshold = score(items[n-1])
	}
	return p
}

// BestDir is where the session folder goes in Stars.
func BestDir(sessionDir string) string {
	if rest, ok := strings.CutPrefix(sessionDir, ObjectsDir+"/"); ok {
		return StarsDir + "/" + rest
	}
	return StarsDir + "/" + sessionDir
}

// BestResult counts what ApplyBest did.
type BestResult struct {
	Placed      int // best lights put into Stars
	NotLaidOut  int // best lights not in the output yet (not laid out or downloaded)
	Calibration int
	Errors      []error
}

// ApplyBest makes the Stars folders of the pick in out: the session folders
// of the object with the filter made before are removed first (a pick
// replaces the one before), then the best lights and the calibration of
// each session go in as hard links (copies on another disk).
func (l *Layout) ApplyBest(ctx context.Context, out string, q BestQuery, p BestPick, progress func(done, total int)) BestResult {
	var r BestResult
	fail := func(err error) { r.Errors = append(r.Errors, err) }
	laidOut := func(it *Item) bool {
		st, err := os.Stat(filepath.Join(out, filepath.FromSlash(it.Dest)))
		return err == nil && st.Size() == it.Frame.Size
	}
	some := false
	for _, it := range p.Keep {
		some = some || laidOut(it)
	}
	if !some {
		// nothing to put in: the pick made before stays
		r.NotLaidOut = len(p.Keep)
		return r
	}
	stars := filepath.Join(out, StarsDir)
	for _, g := range l.BestSessions(q.Object, q.Filter) {
		dir := filepath.Join(out, filepath.FromSlash(BestDir(g.Dir)))
		if !Inside(dir, stars) || samePath(dir, stars) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fail(err)
		}
	}
	total := len(p.Keep)
	for _, g := range p.Sessions {
		for _, c := range []*Group{g.Darks, g.Flats, g.Biases} {
			if c != nil {
				total += c.Count()
			}
		}
	}
	done := 0
	step := func() {
		done++
		if progress != nil {
			progress(done, total)
		}
	}
	kept := map[*Group]int{}
	for _, it := range p.Keep {
		if ctx.Err() != nil {
			return r
		}
		src := filepath.Join(out, filepath.FromSlash(it.Dest))
		if !laidOut(it) {
			r.NotLaidOut++
			step()
			continue
		}
		dst := filepath.Join(out, filepath.FromSlash(BestDir(it.Group.Dir)), "lights", path.Base(it.Dest))
		if _, err := place(src, dst, Link); err != nil {
			fail(fmt.Errorf("%s: %w", it.Frame.Rel, err))
		} else {
			r.Placed++
			kept[it.Group]++
		}
		step()
	}
	for _, g := range p.Sessions {
		if kept[g] == 0 {
			continue // no frame of the session picked: no folder
		}
		dir := filepath.Join(out, filepath.FromSlash(BestDir(g.Dir)))
		for _, d := range SessionDirs {
			if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
				fail(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, CalibrationFile), []byte(g.CalibrationText()), 0o644); err != nil {
			fail(err)
		}
		for _, c := range []struct {
			sub string
			g   *Group
		}{{"darks", g.Darks}, {"flats", g.Flats}, {"biases", g.Biases}} {
			if c.g == nil {
				continue
			}
			for _, it := range c.g.Items {
				if it.Dup {
					continue
				}
				if ctx.Err() != nil {
					return r
				}
				src := filepath.Join(out, filepath.FromSlash(it.Dest))
				if _, err := os.Stat(src); err != nil {
					if it.Frame.Src != nil {
						step() // not downloaded
						continue
					}
					src = it.Frame.Path
				}
				if placed, err := place(src, filepath.Join(dir, c.sub, path.Base(it.Dest)), Link); err != nil {
					fail(fmt.Errorf("%s: %w", it.Frame.Rel, err))
				} else if placed {
					r.Calibration++
				}
				step()
			}
		}
	}
	if r.Placed > 0 {
		obj := filepath.Join(out, filepath.FromSlash(BestDir(q.Object)))
		if err := os.WriteFile(filepath.Join(obj, BestFile(q.Filter)), []byte(p.Text(q, kept)), 0o644); err != nil {
			fail(err)
		}
	}
	return r
}

// Text describes the pick, for the note in Stars.
func (p BestPick) Text(q BestQuery, kept map[*Group]int) string {
	var b strings.Builder
	filter := q.Filter
	if filter == "" {
		filter = "без фильтра"
	}
	fmt.Fprintf(&b, "Отбор лучших кадров, %s\r\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Объект: %s, фильтр: %s, лучшие %g%%\r\n", path.Base(q.Object), filter, q.Percent)
	fmt.Fprintf(&b, "Оценка как взвешенный FWHM в Siril: HFR·(1 + 2·(Nmax − N)/Nmax), Nmax = %d звёзд; меньше — лучше.\r\n", p.Nmax)
	fmt.Fprintf(&b, "Взято кадров: %d из %d (порог оценки %.2f)", len(p.Keep), p.All, p.Threshold)
	if p.Unmeasured > 0 {
		fmt.Fprintf(&b, ", без оценки звёзд и не взяты: %d", p.Unmeasured)
	}
	b.WriteString("\r\n\r\nСессии:\r\n")
	for _, g := range p.Sessions {
		fmt.Fprintf(&b, "  %s — %d из %d\r\n", path.Base(g.Dir), kept[g], g.Count())
	}
	return b.String()
}
