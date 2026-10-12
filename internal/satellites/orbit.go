// Package satellites tells which satellite left a track on a frame: it
// finds the tracks (streaks) on the image and compares them with the paths
// of the catalogued objects (TLE orbits, SGP4) across the frame during the
// exposure, using the astrometric solution ASIAIR writes into the header.
package satellites

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Orbit is one catalogued object.
type Orbit struct {
	Name    string
	NORAD   int
	IntlID  string    // international designator, e.g. 2013-026C
	Epoch   time.Time // of the elements
	el      elements
	jdEpoch float64
}

// PerigeeKm and ApogeeKm: heights above the Earth.
func (o *Orbit) PerigeeKm() float64 { return o.el.altp * earthR }
func (o *Orbit) ApogeeKm() float64  { return o.el.alta * earthR }

// PeriodMin: minutes per revolution.
func (o *Orbit) PeriodMin() float64 { return twoPi / o.el.noUnk }

// ParseTLE reads orbits in the three line format (a name line, then lines
// 1 and 2). Objects whose elements cannot be propagated are left out.
func ParseTLE(r io.Reader) ([]*Orbit, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024), 1024)
	var out []*Orbit
	var name, l1 string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \r")
		switch {
		case strings.HasPrefix(line, "1 ") && len(line) >= 69:
			l1 = line
		case strings.HasPrefix(line, "2 ") && len(line) >= 69 && l1 != "":
			if o, err := parseOrbit(name, l1, line); err == nil {
				out = append(out, o)
			}
			name, l1 = "", ""
		default:
			name, l1 = strings.TrimSpace(strings.TrimPrefix(line, "0 ")), ""
		}
	}
	return out, sc.Err()
}

// field returns columns a..b (1-based, inclusive) of a TLE line.
func field(l string, a, b int) string { return strings.TrimSpace(l[a-1 : b]) }

// expField reads the TLE notation of "12345-3" = 0.12345e-3.
func expField(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	sign := 1.0
	if s[0] == '-' || s[0] == '+' {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	i := strings.LastIndexAny(s, "+-")
	if i <= 0 {
		v, err := strconv.ParseFloat("0."+s, 64)
		return sign * v, err
	}
	m, err := strconv.ParseFloat("0."+strings.TrimSpace(s[:i]), 64)
	if err != nil {
		return 0, err
	}
	e, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0, err
	}
	return sign * m * math.Pow(10, float64(e)), nil
}

func parseOrbit(name, l1, l2 string) (*Orbit, error) {
	num := func(l string, a, b int) float64 {
		v, err := strconv.ParseFloat(field(l, a, b), 64)
		if err != nil {
			panic(err)
		}
		return v
	}
	var o *Orbit
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("TLE: %v", r)
			}
		}()
		cat := field(l1, 3, 7)
		var norad int
		if cat != "" && cat[0] >= 'A' && cat[0] <= 'Z' {
			norad, err = alpha5(cat)
		} else {
			norad, err = strconv.Atoi(cat)
		}
		if err != nil {
			return err
		}
		yy := int(num(l1, 19, 20))
		year := 1900 + yy
		if yy < 57 {
			year = 2000 + yy
		}
		days := num(l1, 21, 32)
		bstar, err := expField(l1[53:61])
		if err != nil {
			return err
		}
		// Julian date of the epoch
		jd := julian(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)) + days - 1
		const deg = math.Pi / 180
		xpdotp := 1440.0 / twoPi
		o = &Orbit{
			Name:    name,
			NORAD:   norad,
			IntlID:  intlID(field(l1, 10, 17)),
			Epoch:   fromJulian(jd),
			jdEpoch: jd,
		}
		o.el = elements{
			epoch:   jd - 2433281.5,
			bstar:   bstar,
			inclo:   num(l2, 9, 16) * deg,
			nodeo:   num(l2, 18, 25) * deg,
			ecco:    num(l2, 27, 33) * 1e-7,
			argpo:   num(l2, 35, 42) * deg,
			mo:      num(l2, 44, 51) * deg,
			noKozai: num(l2, 53, 63) / xpdotp,
		}
		if o.Name == "" {
			o.Name = strconv.Itoa(norad)
		}
		return o.el.init()
	}()
	if err != nil {
		return nil, err
	}
	return o, nil
}

// alpha5 reads the catalogue numbers above 99999 ("A0001" = 100001).
func alpha5(s string) (int, error) {
	if len(s) != 5 || s[0] < 'A' || s[0] > 'Z' || s[0] == 'I' || s[0] == 'O' {
		return 0, fmt.Errorf("номер %q", s)
	}
	c := int(s[0]-'A') + 10
	if s[0] > 'I' {
		c--
	}
	if s[0] > 'O' {
		c--
	}
	n, err := strconv.Atoi(s[1:])
	return c*10000 + n, err
}

// intlID: "13026C" → "2013-026C".
func intlID(s string) string {
	if len(s) < 5 {
		return s
	}
	yy, err := strconv.Atoi(s[:2])
	if err != nil {
		return s
	}
	y := 1900 + yy
	if yy < 57 {
		y = 2000 + yy
	}
	return fmt.Sprintf("%d-%s", y, s[2:])
}

// julian: the Julian date of a moment (UTC taken as UT1).
func julian(t time.Time) float64 {
	return float64(t.UnixNano())/864e11 + 2440587.5
}

func fromJulian(jd float64) time.Time {
	return time.Unix(0, int64((jd-2440587.5)*864e11)).UTC()
}

// TEME returns the position of the object at t, km in the TEME frame.
func (o *Orbit) TEME(t time.Time) ([3]float64, error) {
	return o.el.propagate((julian(t) - o.jdEpoch) * 1440)
}
