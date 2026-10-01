package planner

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

// Target is an object to shoot.
type Target struct {
	Name     string  `json:"name"`
	RA       float64 `json:"ra"`  // J2000, degrees
	Dec      float64 `json:"dec"` // J2000, degrees
	Exposure float64 `json:"exposure"`
	// WantSec: how much exposure is wanted this night, seconds; 0 = as
	// much as the night gives
	WantSec  float64 `json:"want_sec,omitempty"`
	Priority int     `json:"priority"` // 1 is the most important
	Filter   string  `json:"filter,omitempty"`
	Off      bool    `json:"off,omitempty"` // left out of the plan
}

// Params are the night and what the work costs. Durations in minutes
// unless said otherwise.
type Params struct {
	Night    string         `json:"-"` // the evening, YYYY-MM-DD (local date of the site)
	Zone     *time.Location `json:"-"` // the clock of the observatory; nil = this computer's
	Lat, Lon float64        `json:"-"`

	SunAlt             float64 `json:"sun_alt"`        // the night is when the Sun is lower: -18 astronomical, -12 nautical
	SwitchMin          float64 `json:"switch_min"`     // slew, plate solving, guiding start
	RefocusEveryMin    float64 `json:"refocus_every"`  // autofocus about this often (0 = never)
	RefocusMin         float64 `json:"refocus_min"`    // how long the autofocus takes
	RefocusAfterSwitch bool    `json:"refocus_switch"` // autofocus after every new target
	DitherEvery        int     `json:"dither_every"`   // dither every N frames (0 = never)
	DitherSec          float64 `json:"dither_sec"`     // settling after a dither, seconds
	DownloadSec        float64 `json:"download_sec"`   // between frames, seconds
	Flip               bool    `json:"flip"`           // equatorial mount: meridian flip
	FlipAfterMin       float64 `json:"flip_after"`     // the flip, this long after the meridian
	FlipMin            float64 `json:"flip_min"`       // how long the flip takes (with re-centering and guiding)
	MinBlockMin        float64 `json:"min_block"`      // do not go to a target seen for less than this
	MoonSep            float64 `json:"moon_sep"`       // degrees from the Moon at least (0 = no matter)
}

// DefaultParams are what is asked for at first.
func DefaultParams() Params {
	return Params{SunAlt: -18, SwitchMin: 5, RefocusEveryMin: 60, RefocusMin: 2, DitherEvery: 3, DitherSec: 15,
		DownloadSec: 2, Flip: true, FlipAfterMin: 5, FlipMin: 5, MinBlockMin: 20}
}

// Kinds of steps.
const (
	Shoot   = "shoot"
	Slew    = "slew"
	Flip    = "flip"
	Refocus = "refocus"
	Wait    = "wait"
)

// KindLabel is how a step is named.
var KindLabel = map[string]string{Shoot: "съёмка", Slew: "переход", Flip: "перекладка", Refocus: "фокусировка", Wait: "ожидание"}

// Step is a piece of the night.
type Step struct {
	Kind       string
	Start, End time.Time // UTC
	Target     int       // index in Plan.Targets, -1 = none
	Frames     int       // Shoot
	Note       string
}

// Plan is the night planned.
type Plan struct {
	Targets    []Target
	Sky        Sky
	Params     Params
	Dusk, Dawn time.Time // UTC
	Steps      []Step
	Frames     []int     // per target
	Seconds    []float64 // exposure per target
}

// ErrNoNight: the Sun does not go low enough.
var ErrNoNight = errors.New("в эту ночь Солнце не опускается достаточно низко")

// Night is when the Sun is below sunAlt in the night that begins in the
// evening of night (YYYY-MM-DD, at the site).
func Night(night string, lat, lon, sunAlt float64) (dusk, dawn time.Time, err error) {
	d, err := time.Parse("2006-01-02", night)
	if err != nil {
		return dusk, dawn, fmt.Errorf("дата ночи %q: нужна вида 2026-10-01", night)
	}
	// the local noon of the site, roughly by the longitude
	noon := d.Add(12*time.Hour - time.Duration(lon/15*float64(time.Hour)))
	for t := noon; t.Before(noon.Add(24 * time.Hour)); t = t.Add(time.Minute) {
		low := moon.Sun(t, lat, lon).Alt < sunAlt
		if low && dusk.IsZero() {
			dusk = t
		}
		if !low && !dusk.IsZero() {
			return dusk, t, nil
		}
	}
	if dusk.IsZero() {
		return dusk, dawn, ErrNoNight
	}
	return dusk, noon.Add(24 * time.Hour), nil
}

// Where is the target i at t.
func (p *Plan) Where(i int, t time.Time) Point {
	tg := p.Targets[i]
	q := moon.Object(t, p.Params.Lat, p.Params.Lon, tg.RA, tg.Dec)
	return Point{q.Az, q.Alt}
}

// open: the target can be shot at t.
func (p *Plan) open(i int, t time.Time) bool {
	w := p.Where(i, t)
	if !p.Sky.Visible(w.Az, w.Alt) {
		return false
	}
	if p.Params.MoonSep > 0 {
		m := moon.At(t, p.Params.Lat, p.Params.Lon, true)
		if moon.Separation(moon.Pos{Alt: w.Alt, Az: w.Az}, moon.Pos{Alt: m.Alt, Az: m.Az}) < p.Params.MoonSep {
			return false
		}
	}
	return true
}

// openFor: the target can be shot all the time from t for d (checked every
// minute), within the night.
func (p *Plan) openFor(i int, t time.Time, d time.Duration) bool {
	end := t.Add(d)
	if end.After(p.Dawn) {
		return false
	}
	for ; t.Before(end); t = t.Add(time.Minute) {
		if !p.open(i, t) {
			return false
		}
	}
	return p.open(i, end)
}

// opensWithin: the time, up to max from t, when the target is open again
// for the duration of a frame, or false.
func (p *Plan) opensWithin(i int, t time.Time, max, frame time.Duration) (time.Time, bool) {
	for u := t; !u.After(t.Add(max)); u = u.Add(time.Minute) {
		if p.openFor(i, u, frame) {
			return u, true
		}
	}
	return time.Time{}, false
}

func minutes(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// Schedule plans the night: at every moment the most important target
// that can be shot and still lacks exposure. A target is gone to only if
// it stays open for MinBlockMin; a target hidden for a short time (less
// than going away and back) is waited for.
func Schedule(targets []Target, sky Sky, prm Params) (*Plan, error) {
	if prm.Zone == nil {
		prm.Zone = time.Local
	}
	p := &Plan{Targets: targets, Sky: sky, Params: prm, Frames: make([]int, len(targets)), Seconds: make([]float64, len(targets))}
	var err error
	if p.Dusk, p.Dawn, err = Night(prm.Night, prm.Lat, prm.Lon, prm.SunAlt); err != nil {
		return p, err
	}
	// by priority, the order of the list among equals
	order := make([]int, len(targets))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return targets[order[a]].Priority < targets[order[b]].Priority })
	left := func(i int) bool {
		tg := targets[i]
		return !tg.Off && tg.Exposure > 0 && (tg.WantSec <= 0 || p.Seconds[i]+tg.Exposure/2 < tg.WantSec)
	}
	frameDur := func(i int) time.Duration { return seconds(targets[i].Exposure + prm.DownloadSec) }
	switchDur := minutes(prm.SwitchMin)
	block := minutes(prm.MinBlockMin)

	// pick: the target to shoot from t; cur is shot now
	pick := func(t time.Time, cur int) int {
		for _, i := range order {
			if !left(i) {
				continue
			}
			if i == cur {
				if p.openFor(i, t, frameDur(i)) {
					return i
				}
				continue
			}
			need := block
			if w := targets[i].WantSec - p.Seconds[i]; targets[i].WantSec > 0 && seconds(w) < need {
				need = seconds(w) + frameDur(i)
			}
			if p.openFor(i, t.Add(switchDur), need) {
				return i
			}
		}
		return -1
	}

	cur, sinceFocus, inBatch := -1, time.Duration(0), 0
	west := false // after the flip
	t := p.Dusk
	add := func(s Step) {
		if n := len(p.Steps); n > 0 {
			last := &p.Steps[n-1]
			if last.Kind == s.Kind && last.Target == s.Target && last.End.Equal(s.Start) && (s.Kind == Shoot || s.Kind == Wait) {
				last.End, last.Frames = s.End, last.Frames+s.Frames
				return
			}
		}
		p.Steps = append(p.Steps, s)
	}
	for t.Before(p.Dawn) {
		next := pick(t, cur)
		if next != cur && cur >= 0 && left(cur) && (next < 0 || rank(order, cur) < rank(order, next)) {
			// the target shot is hidden: wait if it comes back sooner
			// than going elsewhere and back
			if at, ok := p.opensWithin(cur, t, 2*switchDur, frameDur(cur)); ok && at.After(t) {
				add(Step{Kind: Wait, Start: t, End: at, Target: cur, Note: "объект закрыт"})
				t = at
				continue
			}
		}
		if next < 0 {
			// nothing to shoot: the telescope stays where it is
			if cur >= 0 && !left(cur) {
				cur = -1
			}
			add(Step{Kind: Wait, Start: t, End: t.Add(time.Minute), Target: cur, Note: "нечего снимать"})
			t = t.Add(time.Minute)
			continue
		}
		if next != cur {
			add(Step{Kind: Slew, Start: t, End: t.Add(switchDur), Target: next})
			t, cur, inBatch = t.Add(switchDur), next, 0
			west = prm.Flip && moon.HourAngle(t, prm.Lon, targets[cur].RA) >= prm.FlipAfterMin/60
			if prm.RefocusAfterSwitch || sinceFocus == 0 && prm.RefocusEveryMin > 0 {
				add(Step{Kind: Refocus, Start: t, End: t.Add(minutes(prm.RefocusMin)), Target: cur})
				t, sinceFocus = t.Add(minutes(prm.RefocusMin)), time.Nanosecond
			}
			continue
		}
		ha := moon.HourAngle(t, prm.Lon, targets[cur].RA)
		if prm.Flip && !west && ha >= prm.FlipAfterMin/60 && ha < 6 {
			add(Step{Kind: Flip, Start: t, End: t.Add(minutes(prm.FlipMin)), Target: cur})
			t, west, inBatch = t.Add(minutes(prm.FlipMin)), true, 0
			continue
		}
		if prm.RefocusEveryMin > 0 && sinceFocus >= minutes(prm.RefocusEveryMin) {
			add(Step{Kind: Refocus, Start: t, End: t.Add(minutes(prm.RefocusMin)), Target: cur})
			t, sinceFocus = t.Add(minutes(prm.RefocusMin)), time.Nanosecond
			continue
		}
		d := frameDur(cur)
		inBatch++
		if prm.DitherEvery > 0 && inBatch%prm.DitherEvery == 0 {
			d += seconds(prm.DitherSec)
		}
		add(Step{Kind: Shoot, Start: t, End: t.Add(d), Target: cur, Frames: 1})
		p.Frames[cur]++
		p.Seconds[cur] += targets[cur].Exposure
		t = t.Add(d)
		sinceFocus += d
	}
	if n := len(p.Steps); n > 0 && p.Steps[n-1].End.After(p.Dawn) && p.Steps[n-1].Kind == Wait {
		p.Steps[n-1].End = p.Dawn
	}
	// a wait at the end of the night is no step
	for len(p.Steps) > 0 && p.Steps[len(p.Steps)-1].Kind == Wait {
		p.Steps = p.Steps[:len(p.Steps)-1]
	}
	return p, nil
}

func rank(order []int, i int) int {
	for r, j := range order {
		if j == i {
			return r
		}
	}
	return len(order)
}

// State is the telescope at a moment of the plan.
type State struct {
	Step   int   // index in Steps, -1 before or after
	Point  Point // where it points
	Target int   // -1 = none
	West   bool  // after the meridian flip
}

// Park is where the telescope waits before the night: at the pole.
func (p *Plan) Park() Point {
	if p.Params.Lat < 0 {
		return Point{180, -p.Params.Lat}
	}
	return Point{0, p.Params.Lat}
}

// At is the telescope at t: on its target, on the way between two during
// a slew (straight in azimuth and altitude), parked before and after.
func (p *Plan) At(t time.Time) State {
	s := State{Step: -1, Point: p.Park(), Target: -1}
	prev := -1
	for k, st := range p.Steps {
		if t.Before(st.Start) {
			break
		}
		if st.Target >= 0 && st.Kind != Slew {
			prev = st.Target
		}
		if t.Before(st.End) {
			s.Step, s.Target = k, st.Target
			if st.Kind == Slew {
				from := p.Park()
				if prev >= 0 {
					from = p.Where(prev, st.Start)
				}
				to := p.Where(st.Target, st.End)
				f := float64(t.Sub(st.Start)) / float64(st.End.Sub(st.Start))
				s.Point = Point{normAz(from.Az + wrap180(to.Az-from.Az)*f), from.Alt + (to.Alt-from.Alt)*f}
			} else if st.Target >= 0 {
				s.Point = p.Where(st.Target, t)
			} else if prev >= 0 {
				s.Point = p.Where(prev, t)
			}
			break
		}
		if st.Kind == Slew {
			prev = st.Target
		}
	}
	if s.Target >= 0 && p.Params.Flip {
		// west after the flip of this target's block
		for k := s.Step; k >= 0 && p.Steps[k].Target == s.Target && p.Steps[k].Kind != Slew; k-- {
			if p.Steps[k].Kind == Flip && t.After(p.Steps[k].End.Add(-time.Second)) {
				s.West = true
				break
			}
		}
		if !s.West {
			for k := s.Step; k >= 0; k-- {
				if p.Steps[k].Kind == Slew && p.Steps[k].Target == s.Target {
					s.West = moon.HourAngle(p.Steps[k].End, p.Params.Lon, p.Targets[s.Target].RA) >= p.Params.FlipAfterMin/60
					break
				}
			}
		}
	}
	return s
}

// Local is t on the clock of the observatory.
func (p *Plan) Local(t time.Time) time.Time { return t.In(p.Params.Zone) }

// Text is the plan to type into the ASIAIR: the targets with their times.
func (p *Plan) Text() string {
	var b strings.Builder
	hm := func(t time.Time) string { return p.Local(t).Format("15:04") }
	fmt.Fprintf(&b, "План на ночь %s (время обсерватории %s)\n", p.Params.Night, p.Params.Zone)
	fmt.Fprintf(&b, "Ночь (Солнце ниже %g°): %s – %s\n\n", p.Params.SunAlt, hm(p.Dusk), hm(p.Dawn))
	for _, s := range p.Steps {
		name := ""
		if s.Target >= 0 {
			name = p.Targets[s.Target].Name
		}
		line := fmt.Sprintf("%s–%s  %-12s %s", hm(s.Start), hm(s.End), KindLabel[s.Kind], name)
		if s.Kind == Shoot {
			tg := p.Targets[s.Target]
			line += fmt.Sprintf(": %d × %g с", s.Frames, tg.Exposure)
			if tg.Filter != "" {
				line += ", фильтр " + tg.Filter
			}
		}
		if s.Note != "" {
			line += " (" + s.Note + ")"
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	b.WriteString("\nИтого:\n")
	for i, tg := range p.Targets {
		if tg.Off {
			continue
		}
		fmt.Fprintf(&b, "  %s: %d кадров, %s", tg.Name, p.Frames[i], Hours(p.Seconds[i]))
		if tg.WantSec > 0 {
			fmt.Fprintf(&b, " из %s", Hours(tg.WantSec))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// CSV is the plan as a table (separated by semicolons, as Excel opens it).
func (p *Plan) CSV() string {
	var b strings.Builder
	b.WriteString("начало;конец;действие;объект;кадров;выдержка, с;фильтр;примечание\n")
	for _, s := range p.Steps {
		name, exp, filter := "", "", ""
		if s.Target >= 0 {
			tg := p.Targets[s.Target]
			name, filter = tg.Name, tg.Filter
			if s.Kind == Shoot {
				exp = fmt.Sprint(tg.Exposure)
			}
		}
		fmt.Fprintf(&b, "%s;%s;%s;%s;%d;%s;%s;%s\n", p.Local(s.Start).Format("2006-01-02 15:04"), p.Local(s.End).Format("2006-01-02 15:04"),
			KindLabel[s.Kind], name, s.Frames, exp, filter, s.Note)
	}
	return b.String()
}

// Hours shows seconds as "2 ч 05 мин".
func Hours(sec float64) string {
	m := int(math.Round(sec / 60))
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	return fmt.Sprintf("%d ч %02d мин", m/60, m%60)
}
