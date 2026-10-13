// Package sorter lays out astro frames for processing in Siril: the lights
// of every object go into session folders (object, night, filter, exposure,
// gain) with the lights/darks/flats/biases subfolders Siril's scripts expect,
// and calibration frames go into libraries — darks and biases by camera,
// gain, exposure and temperature, flats by night, filter, gain and exposure.
// The package is portable; the window lives in cmd/asiair-sorter.
package sorter

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// Frame kinds.
const (
	Light    = "light"
	Dark     = "dark"
	Flat     = "flat"
	Bias     = "bias"
	DarkFlat = "darkflat"
)

// KindLabel is how a kind is shown.
var KindLabel = map[string]string{Light: "Light", Dark: "Dark", Flat: "Flat", Bias: "Bias", DarkFlat: "Dark Flat"}

// Frame is one FITS file with what matters for sorting it.
type Frame struct {
	Path     string // full path on disk
	Rel      string // path relative to the folder that was scanned, with /
	Size     int64
	Kind     string
	Object   string  // lights only
	Filter   string  // "" for no filter wheel
	Exposure float64 // seconds, 0 = unknown
	Gain     int     // -1 = unknown
	Temp     float64 // sensor temperature (°C, rounded); NaN = unknown
	Bin      int
	Camera   string
	Time     time.Time // local time at the observatory (the UTC fields hold it)
	Night    string    // YYYY-MM-DD of the evening the night began
	// UTC is the moment of the frame: DATE-OBS, else Time in this
	// computer's zone
	UTC time.Time
	// where the telescope pointed (J2000, degrees) and the observatory,
	// from the header (RA/DEC or OBJCTRA/OBJCTDEC, SITELAT/SITELONG)
	RA, Dec   float64
	HasTarget bool
	Lat, Lon  float64
	HasSite   bool

	// Src is the receiver the frame is downloaded from (nil: a file on
	// disk); Path is then its path there, MTime its file time and Cards
	// its header.
	Src   *Receiver
	MTime time.Time
	Cards []Card
	// InLibrary: the frame is in the library of the output (ScanLibrary);
	// when the layout changed it is moved to its new place
	InLibrary bool

	// the stars of a light frame (package stars): measured by the receiver
	// or by the sorter (MeasureFrames); HFR in pixels, 0 without stars
	Measured bool
	HFR      float64
	Stars    int
	// the shape of the stars (stars.Result: roundness, the share stretched
	// one way) and the saturated pixels of the frame with their share
	// (percent); 0 when not measured
	Round   float64
	Aligned float64
	SatPx   int64
	SatPct  float64
	// the straight tracks on a light frame (satellites, planes, meteors;
	// not the ones fixed in the sky) and the objects that left them: told
	// by the receiver or by the sorter (IdentifySatellites). SatTold: the
	// objects were looked for, with orbits SatAge days from the frame;
	// FlipRows: the rows of the file go bottom up (the tracks are in them)
	Tracks   []Track
	SatTold  bool
	SatAge   float64
	SatErr   string
	FlipRows bool
}

// IsFITS tells FITS files by their extension.
func IsFITS(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".fit", ".fits", ".fts":
		return true
	}
	return false
}

var (
	stampRe = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})`)
	expRe   = regexp.MustCompile(`(?i)(?:^|_)(\d+(?:\.\d+)?)(ms|s)(?:_|\.|$)`)
	gainRe  = regexp.MustCompile(`(?i)(?:^|_)gain(\d+)(?:_|\.|$)`)
	tempRe  = regexp.MustCompile(`(?i)_(-?\d+(?:\.\d+)?)C(?:_|\.|$)`)
	binRe   = regexp.MustCompile(`(?i)(?:^|_)bin(\d)(?:_|\.|$)`)
	lightRe = regexp.MustCompile(`(?i)^light_([^_]+)_`)
)

// ReadFrame reads the header of a FITS file and tells what the frame is.
// Where the header says nothing, the ASIAIR file name and folders help
// ("Autorun/Light/M 31/Light_M 31_300.0s_Bin1_gain100_20260926-221000_-10.0C_0001.fit").
func ReadFrame(p, rel string) (Frame, error) {
	f, err := os.Open(p)
	if err != nil {
		return Frame{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Frame{}, err
	}
	h, err := fits.ReadHeader(f)
	if err != nil {
		return Frame{}, err
	}
	fr := describe(h.Get, rel, st.ModTime())
	fr.Path, fr.Size = p, st.Size()
	return fr, nil
}

// describe works out a frame from its header keywords (get) and its path.
func describe(get func(string) string, rel string, mtime time.Time) Frame {
	rel = filepath.ToSlash(rel)
	name := path.Base(rel)
	fr := Frame{Rel: rel, Gain: -1, Temp: math.NaN(), Bin: 1}

	fr.Kind = kindOf(get("IMAGETYP"), rel)

	if v, ok := num(get("EXPTIME")); ok {
		fr.Exposure = v
	} else if v, ok := num(get("EXPOSURE")); ok {
		fr.Exposure = v
	} else if m := expRe.FindStringSubmatch(name); m != nil {
		fr.Exposure, _ = strconv.ParseFloat(m[1], 64)
		if strings.EqualFold(m[2], "ms") {
			fr.Exposure /= 1000
		}
	}

	if v, ok := num(get("GAIN")); ok {
		fr.Gain = int(math.Round(v))
	} else if m := gainRe.FindStringSubmatch(name); m != nil {
		fr.Gain, _ = strconv.Atoi(m[1])
	}

	// the set point groups darks better than the measured temperature
	if v, ok := num(get("SET-TEMP")); ok {
		fr.Temp = math.Round(v)
	} else if v, ok := num(get("CCD-TEMP")); ok {
		fr.Temp = math.Round(v)
	} else if m := tempRe.FindStringSubmatch(name); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		fr.Temp = math.Round(v)
	}
	if fr.Temp == 0 {
		fr.Temp = 0 // no "-0"
	}

	if v, ok := num(get("XBINNING")); ok && v >= 1 {
		fr.Bin = int(v)
	} else if m := binRe.FindStringSubmatch(name); m != nil {
		fr.Bin, _ = strconv.Atoi(m[1])
	}

	fr.Filter = strings.TrimSpace(get("FILTER"))
	fr.Camera = strings.TrimSpace(get("INSTRUME"))

	if fr.Kind == Light {
		fr.Object = objectOf(get("OBJECT"), rel)
	}

	fr.Time = timeOf(get, name, mtime)
	fr.Night = fr.Time.Add(-12 * time.Hour).Format("2006-01-02")
	fr.UTC = utcOf(get, fr.Time)
	fr.RA, fr.Dec, fr.HasTarget = moon.Target(get)
	fr.Lat, fr.Lon, fr.HasSite = moon.Site(get)
	return fr
}

func num(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Replace(s, "D", "E", 1), 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// kindOf: dark flats only show in the name; then IMAGETYP ("Light Frame",
// "Flat Field", …), then the name, then the folders.
func kindOf(imagetyp, rel string) string {
	byName := protocol.FrameKind(rel)
	if byName == DarkFlat {
		return DarkFlat
	}
	t := strings.ToLower(imagetyp)
	switch {
	case strings.Contains(t, "light"):
		return Light
	case strings.Contains(t, "dark"):
		return Dark
	case strings.Contains(t, "flat"):
		return Flat
	case strings.Contains(t, "bias"), strings.Contains(t, "offset"):
		return Bias
	}
	if byName != "" {
		return byName
	}
	parts := strings.Split(rel, "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if k := protocol.FrameKind(parts[i]); k != "" {
			return k
		}
		switch strings.ToLower(parts[i]) {
		case "lights":
			return Light
		case "darks":
			return Dark
		case "flats":
			return Flat
		case "biases", "offsets":
			return Bias
		}
	}
	return ""
}

// objectOf: the folder after "Light" (ASIAIR puts the frames of a plan in
// Plan/Light/<object>/ and Autorun/Light/<object>/), else OBJECT, else the name.
func objectOf(header, rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) >= 5 && parts[0] == ObjectsDir && parts[3] == "lights" {
		return parts[1] // laid out here before: Objects/<object>/<session>/lights
	}
	for i := 0; i < len(parts)-2; i++ {
		if strings.EqualFold(parts[i], "light") {
			return strings.TrimSpace(parts[i+1])
		}
	}
	if o := strings.TrimSpace(header); o != "" {
		return o
	}
	if m := lightRe.FindStringSubmatch(path.Base(rel)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// timeOf: the ASIAIR name holds the local time of the observatory; then
// DATE-LOC; then DATE-OBS (UTC) in this computer's zone; then the file time.
func timeOf(get func(string) string, name string, mtime time.Time) time.Time {
	if m := stampRe.FindStringSubmatch(name); m != nil {
		t, err := time.Parse("20060102150405", m[1]+m[2]+m[3]+m[4]+m[5]+m[6])
		if err == nil {
			return t
		}
	}
	layouts := []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02"}
	if s := strings.TrimSpace(get("DATE-LOC")); s != "" {
		for _, l := range layouts {
			if t, err := time.Parse(l, s); err == nil {
				return t
			}
		}
	}
	if s := strings.TrimSpace(strings.TrimSuffix(get("DATE-OBS"), "Z")); s != "" {
		for _, l := range layouts {
			if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
				return wall(t.In(time.Local))
			}
		}
	}
	return wall(mtime.In(time.Local))
}

// utcOf: DATE-OBS (UTC), else the clock reading wall in this computer's zone.
func utcOf(get func(string) string, wall time.Time) time.Time {
	if s := strings.TrimSuffix(strings.Trim(get("DATE-OBS"), "' "), "Z"); s != "" {
		for _, l := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
			if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
				return t
			}
		}
	}
	if wall.Year() < 2000 {
		return time.Time{}
	}
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.Local).UTC()
}

// wall keeps the clock reading of t in the UTC fields.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// FormatExposure: 300s, 1.5s, 20ms.
func FormatExposure(sec float64) string {
	if sec <= 0 {
		return "?s"
	}
	if sec < 1 {
		return strconv.FormatFloat(math.Round(sec*1e6)/1e3, 'f', -1, 64) + "ms"
	}
	return strconv.FormatFloat(math.Round(sec*1000)/1000, 'f', -1, 64) + "s"
}

// FormatTemp: -10C, "" when unknown.
func FormatTemp(t float64) string {
	if math.IsNaN(t) {
		return ""
	}
	return strconv.FormatFloat(t, 'f', -1, 64) + "C"
}

// SafeName makes s usable as a Windows folder name.
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(strings.TrimSpace(s), ". ")
	if s == "" {
		return "_"
	}
	switch strings.ToUpper(strings.SplitN(s, ".", 2)[0]) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "LPT1", "LPT2", "LPT3":
		s = "_" + s
	}
	return s
}

// Scan finds the FITS files under root and reads their headers. skip, if
// set, leaves out a folder (the output folder when it is inside root).
// progress is called now and then with the number of files read.
func Scan(root string, skip func(dir string) bool, progress func(n int)) ([]Frame, []error) {
	var frames []Frame
	var errs []error
	n := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() {
			if p != root && skip != nil && skip(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if !IsFITS(p) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		fr, err := ReadFrame(p, rel)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			return nil
		}
		frames = append(frames, fr)
		n++
		if progress != nil && n%25 == 0 {
			progress(n)
		}
		return nil
	})
	if progress != nil {
		progress(n)
	}
	return frames, errs
}

// ScanFor reads the frames under in, and also the library already built
// under out (ScanLibrary). The output folder is left out of the input when
// inside it.
func ScanFor(in, out string, progress func(n int)) ([]Frame, []error) {
	frames, errs := Scan(in, func(dir string) bool { return out != "" && samePath(dir, out) }, progress)
	if out != "" && Inside(in, filepath.Join(out, LibraryDir)) {
		return frames, errs // the library is the input
	}
	base := len(frames)
	more, e := ScanLibrary(out, func(n int) {
		if progress != nil {
			progress(base + n)
		}
	})
	return append(frames, more...), append(errs, e...)
}

// ScanLibrary reads what was laid out under out before: the library (its
// darks, flats and biases fit the new lights too) and the lights of the
// sessions. They stay where they are, or move when the layout changed.
func ScanLibrary(out string, progress func(n int)) ([]Frame, []error) {
	if out == "" {
		return nil, nil
	}
	var frames []Frame
	var errs []error
	for _, top := range []string{LibraryDir, ObjectsDir} {
		dir := filepath.Join(out, top)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		skip := func(string) bool { return false }
		if top == ObjectsDir {
			// only <object>/<session>/lights: not the calibration copies,
			// Siril's results in process nor PixInsight's in <object>/PI
			skip = func(d string) bool {
				rel, err := filepath.Rel(dir, d)
				if err != nil {
					return false
				}
				switch strings.Count(filepath.ToSlash(rel), "/") {
				case 1:
					return strings.EqualFold(filepath.Base(d), PIDir)
				case 2:
					return filepath.Base(d) != "lights"
				}
				return false
			}
		}
		base := len(frames)
		more, e := Scan(dir, skip, func(n int) {
			if progress != nil {
				progress(base + n)
			}
		})
		errs = append(errs, e...)
		for _, f := range more {
			f.Rel = top + "/" + f.Rel
			f.InLibrary = true
			// of the sessions only their lights: the calibration in them is a
			// copy of the library
			if top == ObjectsDir && (f.Kind != Light || path.Base(path.Dir(f.Rel)) != "lights") {
				continue
			}
			frames = append(frames, f)
		}
	}
	return frames, errs
}
