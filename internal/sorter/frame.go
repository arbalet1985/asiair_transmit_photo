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
// under out: its darks, flats and biases fit the new lights too, and stay
// where they are. The output folder is left out of the input when inside it.
func ScanFor(in, out string, progress func(n int)) ([]Frame, []error) {
	frames, errs := Scan(in, func(dir string) bool { return out != "" && samePath(dir, out) }, progress)
	lib := filepath.Join(out, LibraryDir)
	if st, err := os.Stat(lib); out != "" && err == nil && st.IsDir() && !Inside(in, lib) {
		base := len(frames)
		more, e := Scan(lib, nil, func(n int) {
			if progress != nil {
				progress(base + n)
			}
		})
		for i := range more {
			more[i].Rel = LibraryDir + "/" + more[i].Rel
		}
		frames, errs = append(frames, more...), append(errs, e...)
	}
	return frames, errs
}
