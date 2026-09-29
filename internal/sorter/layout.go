package sorter

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Top folders of the output.
const (
	ObjectsDir = "Objects"
	LibraryDir = "Library"
)

// Siril's folders inside a session (its standard scripts use these names).
var SessionDirs = []string{"lights", "darks", "flats", "biases", "process"}

// Group is the frames that go into one folder.
type Group struct {
	Kind string
	Dir  string // folder relative to the output, with /
	// what the frames share (from the first one)
	Object, Filter, Camera, Night string
	Exposure, Temp                float64
	Gain, Bin                     int
	Items                         []*Item

	// lights only: the library folders that fit this session
	Darks, Flats, Biases *Group
	Notes                []string // what does not fit well
}

// Item is one frame and where it goes.
type Item struct {
	Frame *Frame
	Group *Group
	Dest  string // file path relative to the output, with /
	// Dup: the same file (name and size) is already placed by another item,
	// e.g. a frame found both on the ASIAIR's memory and its USB drive
	Dup bool
	// Existing: the same frame is already in the output's library, in a
	// folder of an earlier layout; it is moved to Dest instead of being
	// copied or downloaded again
	Existing string
}

// Layout is the planned output.
type Layout struct {
	Items      []*Item
	Groups     []*Group // sorted by folder
	Sessions   []*Group // the light groups
	Unknown    []*Frame // frames of unknown kind: left where they are
	TotalBytes int64
}

// Seconds of light frames in a group.
func (g *Group) Seconds() float64 {
	var s float64
	for _, it := range g.Items {
		if !it.Dup {
			s += it.Frame.Exposure
		}
	}
	return s
}

// InPlace tells a duplicate that is the copy already in the output's
// library: nothing to show or do for it.
func (it *Item) InPlace() bool {
	return it.Dup && it.Frame.InLibrary
}

// Frames counts the frames found, the copies already in the library of
// the output left out.
func (l *Layout) Frames() int {
	n := len(l.Unknown)
	for _, it := range l.Items {
		if !it.InPlace() {
			n++
		}
	}
	return n
}

// Count of the frames placed (duplicates left out).
func (g *Group) Count() int {
	n := 0
	for _, it := range g.Items {
		if !it.Dup {
			n++
		}
	}
	return n
}

func gainPart(g int) string {
	if g < 0 {
		return ""
	}
	return "gain" + strconv.Itoa(g)
}

func binPart(b int) string {
	if b <= 1 {
		return ""
	}
	return "bin" + strconv.Itoa(b)
}

func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return SafeName(strings.Join(out, "_"))
}

// GroupDir is the folder a frame belongs to, relative to the output:
//
//	Objects/M 31/2026-09-26_Ha_300s_gain100      (lights, object/night/filter/exposure/gain)
//	Library/Flats/ZWO ASI2600MM Pro/2026-09-26_Ha_gain100_1.5s  (night/filter/gain/exposure)
//	Library/Darks/ZWO ASI2600MM Pro/gain100_300s_-10C
//	Library/DarkFlats/ZWO ASI2600MM Pro/gain100_1.5s
//	Library/Biases/ZWO ASI2600MM Pro/gain100
//
// The date of darks and biases does not matter: they are reused for months.
func GroupDir(f *Frame) string {
	cam := ""
	if f.Camera != "" {
		cam = SafeName(f.Camera) + "/"
	}
	exp := FormatExposure(f.Exposure)
	switch f.Kind {
	case Light:
		obj := f.Object
		if obj == "" {
			obj = "Unknown"
		}
		return ObjectsDir + "/" + SafeName(obj) + "/" + join(f.Night, f.Filter, exp, gainPart(f.Gain), binPart(f.Bin))
	case Flat:
		return LibraryDir + "/Flats/" + cam + join(f.Night, f.Filter, gainPart(f.Gain), exp, binPart(f.Bin))
	case Dark:
		return LibraryDir + "/Darks/" + cam + join(gainPart(f.Gain), exp, FormatTemp(f.Temp), binPart(f.Bin))
	case DarkFlat:
		return LibraryDir + "/DarkFlats/" + cam + join(gainPart(f.Gain), exp, binPart(f.Bin))
	case Bias:
		return LibraryDir + "/Biases/" + cam + join(gainPart(f.Gain), binPart(f.Bin))
	}
	return ""
}

// Plan works out where every frame goes. Frames already in their place (a
// library in the output that was scanned again) stay and still count for
// matching calibration to the lights.
func Plan(frames []Frame) *Layout {
	l := &Layout{}
	byDir := map[string]*Group{}
	placed := map[string]*Item{} // lower-case destination → first item
	for i := range frames {
		f := &frames[i]
		dir := GroupDir(f)
		if dir == "" {
			l.Unknown = append(l.Unknown, f)
			continue
		}
		g := byDir[dir]
		if g == nil {
			g = &Group{Kind: f.Kind, Dir: dir, Object: f.Object, Filter: f.Filter, Camera: f.Camera,
				Night: f.Night, Exposure: f.Exposure, Temp: f.Temp, Gain: f.Gain, Bin: f.Bin}
			byDir[dir] = g
			l.Groups = append(l.Groups, g)
		}
		sub := dir
		if f.Kind == Light {
			sub += "/lights"
		}
		name := SafeName(path.Base(f.Rel))
		it := &Item{Frame: f, Group: g, Dest: sub + "/" + name}
		if first := placed[strings.ToLower(it.Dest)]; first != nil {
			if first.Frame.Size == f.Size {
				it.Dup = true
				if f.InLibrary && !first.Frame.InLibrary && first.Existing == "" {
					first.Existing = f.Path
				}
			} else {
				it.Dest = uniqueName(sub, name, placed)
			}
		}
		if !it.Dup {
			placed[strings.ToLower(it.Dest)] = it
			l.TotalBytes += f.Size
		}
		g.Items = append(g.Items, it)
		l.Items = append(l.Items, it)
	}
	sort.Slice(l.Groups, func(i, j int) bool { return l.Groups[i].Dir < l.Groups[j].Dir })
	for _, g := range l.Groups {
		if g.Kind == Light {
			l.Sessions = append(l.Sessions, g)
			l.match(g)
		}
	}
	return l
}

func uniqueName(dir, name string, placed map[string]*Item) string {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		p := fmt.Sprintf("%s/%s_%d%s", dir, base, i, ext)
		if placed[strings.ToLower(p)] == nil {
			return p
		}
	}
}

// sameCamera: frames without INSTRUME fit any camera.
func sameCamera(a, b string) bool {
	return a == "" || b == "" || strings.EqualFold(a, b)
}

func sameExposure(a, b float64) bool {
	return math.Abs(a-b) <= 0.005*math.Max(a, b)+1e-6
}

func tempDiff(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return 0
	}
	return math.Abs(a - b)
}

func nightDays(a, b string) float64 {
	ta, err1 := time.Parse("2006-01-02", a)
	tb, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return 1e6
	}
	return math.Abs(ta.Sub(tb).Hours() / 24)
}

// match finds the library folders for a session of lights: darks of the
// same camera, gain, exposure and binning at the nearest temperature; flats
// through the same filter from the nearest night; and for the flats their
// dark flats (same gain and exposure) or else biases of their gain.
func (l *Layout) match(s *Group) {
	var bestDark *Group
	for _, g := range l.Groups {
		if g.Kind != Dark || g.Gain != s.Gain || g.Bin != s.Bin || !sameCamera(g.Camera, s.Camera) || !sameExposure(g.Exposure, s.Exposure) {
			continue
		}
		if bestDark == nil || tempDiff(g.Temp, s.Temp) < tempDiff(bestDark.Temp, s.Temp) ||
			tempDiff(g.Temp, s.Temp) == tempDiff(bestDark.Temp, s.Temp) && g.Count() > bestDark.Count() {
			bestDark = g
		}
	}
	s.Darks = bestDark
	switch {
	case bestDark == nil:
		s.Notes = append(s.Notes, fmt.Sprintf("нет дарков: %s, %s, %s", gainText(s.Gain), FormatExposure(s.Exposure), tempText(s.Temp)))
	case tempDiff(bestDark.Temp, s.Temp) > 2:
		s.Notes = append(s.Notes, fmt.Sprintf("дарки при %s, лайты при %s", tempText(bestDark.Temp), tempText(s.Temp)))
	}

	var bestFlat *Group
	better := func(g, b *Group) bool {
		dg, db := nightDays(g.Night, s.Night), nightDays(b.Night, s.Night)
		if dg != db {
			return dg < db
		}
		// as far before as after: flats are usually shot after the lights
		// (in the morning, or the next day without moving the camera)
		if g.Night != b.Night {
			return g.Night > b.Night
		}
		if (g.Gain == s.Gain) != (b.Gain == s.Gain) {
			return g.Gain == s.Gain
		}
		return g.Count() > b.Count()
	}
	for _, g := range l.Groups {
		if g.Kind != Flat || !strings.EqualFold(g.Filter, s.Filter) || g.Bin != s.Bin || !sameCamera(g.Camera, s.Camera) {
			continue
		}
		if bestFlat == nil || better(g, bestFlat) {
			bestFlat = g
		}
	}
	s.Flats = bestFlat
	if bestFlat == nil {
		f := s.Filter
		if f == "" {
			f = "без фильтра"
		}
		s.Notes = append(s.Notes, "нет флетов: "+f)
		return
	}
	if bestFlat.Night != s.Night {
		s.Notes = append(s.Notes, "флеты другой ночи: "+bestFlat.Night)
	}

	// for the flats: dark flats of their gain and exposure, else biases
	var bias *Group
	for _, g := range l.Groups {
		if g.Kind == DarkFlat && g.Gain == bestFlat.Gain && g.Bin == bestFlat.Bin && sameCamera(g.Camera, bestFlat.Camera) &&
			sameExposure(g.Exposure, bestFlat.Exposure) && (bias == nil || g.Count() > bias.Count()) {
			bias = g
		}
	}
	if bias == nil {
		for _, g := range l.Groups {
			if g.Kind == Bias && g.Gain == bestFlat.Gain && g.Bin == bestFlat.Bin && sameCamera(g.Camera, bestFlat.Camera) &&
				(bias == nil || g.Count() > bias.Count()) {
				bias = g
			}
		}
	}
	s.Biases = bias
	if bias == nil {
		s.Notes = append(s.Notes, fmt.Sprintf("нет биасов (или дарк-флетов) для флетов: %s", gainText(bestFlat.Gain)))
	}
}

func gainText(g int) string {
	if g < 0 {
		return "gain ?"
	}
	return "gain " + strconv.Itoa(g)
}

func tempText(t float64) string {
	if math.IsNaN(t) {
		return "температура ?"
	}
	return strconv.FormatFloat(t, 'f', -1, 64) + " °C"
}

// FormatDuration: "1 ч 20 мин", "45 мин", "30 с".
func FormatDuration(sec float64) string {
	switch {
	case sec >= 3600:
		h, m := int(sec/3600), int(math.Round(math.Mod(sec, 3600)/60))
		if m == 60 {
			h, m = h+1, 0
		}
		if m == 0 {
			return fmt.Sprintf("%d ч", h)
		}
		return fmt.Sprintf("%d ч %d мин", h, m)
	case sec >= 60:
		return fmt.Sprintf("%d мин", int(math.Round(sec/60)))
	}
	return strconv.FormatFloat(math.Round(sec*10)/10, 'f', -1, 64) + " с"
}

// CalibrationText is the note put into every session folder: which library
// folders to take darks, flats and biases from.
func (s *Group) CalibrationText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Объект: %s\r\nНочь: %s\r\nФильтр: %s\r\nВыдержка: %s, %s, %s\r\nКадров: %d, накопление %s\r\n\r\n",
		orDash(s.Object), s.Night, orDash(s.Filter), FormatExposure(s.Exposure), gainText(s.Gain), tempText(s.Temp),
		s.Count(), FormatDuration(s.Seconds()))
	b.WriteString("Подходящая калибровка (положите кадры в одноимённые папки рядом):\r\n")
	line := func(sub string, g *Group) {
		if g == nil {
			fmt.Fprintf(&b, "  %-7s —\r\n", sub)
			return
		}
		fmt.Fprintf(&b, "  %-7s %s  (%d)\r\n", sub, winPath(g.Dir), g.Count())
	}
	line("darks", s.Darks)
	line("flats", s.Flats)
	line("biases", s.Biases)
	if len(s.Notes) > 0 {
		b.WriteString("\r\nВнимание:\r\n")
		for _, n := range s.Notes {
			b.WriteString("  • " + n + "\r\n")
		}
	}
	return b.String()
}

// winPath shows a path the way Explorer does.
func winPath(p string) string { return strings.ReplaceAll(p, "/", `\`) }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Summary describes the layout: sessions with the calibration found for
// them, then the libraries.
func (l *Layout) Summary() string {
	var b strings.Builder
	var lights, calib int
	for _, g := range l.Groups {
		if g.Kind == Light {
			lights += g.Count()
		} else {
			calib += g.Count()
		}
	}
	fmt.Fprintf(&b, "Лайтов: %d в %d сессиях, калибровочных: %d, всего %s\r\n", lights, len(l.Sessions), calib, FormatBytes(l.TotalBytes))
	if len(l.Unknown) > 0 {
		fmt.Fprintf(&b, "Тип кадра не распознан (останутся на месте): %d\r\n", len(l.Unknown))
	}
	b.WriteString("\r\n=== Объекты ===\r\n")
	obj := ""
	for _, s := range l.Sessions {
		if s.Object != obj {
			obj = s.Object
			var total float64
			for _, x := range l.Sessions {
				if x.Object == obj {
					total += x.Seconds()
				}
			}
			fmt.Fprintf(&b, "\r\n%s — всего %s\r\n", orDash(obj), FormatDuration(total))
		}
		fmt.Fprintf(&b, "  %s — %d кадров, %s\r\n", path.Base(s.Dir), s.Count(), FormatDuration(s.Seconds()))
		for _, c := range []struct {
			n string
			g *Group
		}{{"darks", s.Darks}, {"flats", s.Flats}, {"biases", s.Biases}} {
			if c.g != nil {
				fmt.Fprintf(&b, "      %-7s ← %s (%d)\r\n", c.n, winPath(strings.TrimPrefix(c.g.Dir, LibraryDir+"/")), c.g.Count())
			}
		}
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "      ⚠ %s\r\n", n)
		}
	}
	b.WriteString("\r\n=== Библиотека ===\r\n")
	for _, g := range l.Groups {
		if g.Kind != Light {
			fmt.Fprintf(&b, "  %s — %d\r\n", winPath(strings.TrimPrefix(g.Dir, LibraryDir+"/")), g.Count())
		}
	}
	return b.String()
}

// FormatBytes: 1.2 ГБ.
func FormatBytes(n int64) string {
	units := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[0])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
