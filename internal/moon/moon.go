// Package moon tells where the Moon was and how much of it was lit, with
// low precision (after V. Agafonkin's SunCalc, from Meeus): about 0.5° for
// the position and a percent for the illumination. The web page of the
// receiver does the same in JavaScript.
package moon

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Info is the Moon at a moment.
type Info struct {
	Illum  float64 // lit fraction, 0..1
	Waxing bool
	// with the coordinates of the observatory
	HasPos bool
	Alt    float64 // degrees above the horizon (refraction included)
	Az     float64 // degrees from the north through the east
}

const rad = math.Pi / 180

// At computes the Moon at t; without a site (hasSite false) only the phase.
func At(t time.Time, lat, lon float64, hasSite bool) Info {
	d := days(t)
	e := rad * 23.4397
	ra := func(l, b float64) float64 {
		return math.Atan2(math.Sin(l)*math.Cos(e)-math.Tan(b)*math.Sin(e), math.Cos(l))
	}
	dec := func(l, b float64) float64 {
		return math.Asin(math.Sin(b)*math.Cos(e) + math.Cos(b)*math.Sin(e)*math.Sin(l))
	}
	sra, sdec := sun(d)
	// the Moon
	ml, mm, mf := rad*(218.316+13.176396*d), rad*(134.963+13.064993*d), rad*(93.272+13.229350*d)
	l, b := ml+rad*6.289*math.Sin(mm), rad*5.128*math.Sin(mf)
	mra, mdec, mdist := ra(l, b), dec(l, b), 385001-20905*math.Cos(mm)

	const sdist = 149598000
	phi := math.Acos(math.Sin(sdec)*math.Sin(mdec) + math.Cos(sdec)*math.Cos(mdec)*math.Cos(sra-mra))
	inc := math.Atan2(sdist*math.Sin(phi), mdist-sdist*math.Cos(phi))
	angle := math.Atan2(math.Cos(sdec)*math.Sin(sra-mra), math.Sin(sdec)*math.Cos(mdec)-math.Cos(sdec)*math.Sin(mdec)*math.Cos(sra-mra))
	in := Info{Illum: (1 + math.Cos(inc)) / 2, Waxing: angle < 0}
	if !hasSite {
		return in
	}
	in.HasPos = true
	in.Alt, in.Az = altAz(d, lat, lon, mra, mdec)
	return in
}

// sun: right ascension and declination of the Sun (radians) d days after J2000.
func sun(d float64) (ra, dec float64) {
	e := rad * 23.4397
	M := rad * (357.5291 + 0.98560028*d)
	L := M + rad*(1.9148*math.Sin(M)+0.02*math.Sin(2*M)+0.0003*math.Sin(3*M)) + rad*102.9372 + math.Pi
	return math.Atan2(math.Sin(L)*math.Cos(e), math.Cos(L)), math.Asin(math.Sin(e) * math.Sin(L))
}

// Sun is where the Sun is at t seen from lat, lon: below -18° the night
// is astronomical.
func Sun(t time.Time, lat, lon float64) Pos {
	d := days(t)
	ra, dec := sun(d)
	alt, az := altAz(d, lat, lon, ra, dec)
	return Pos{alt, az}
}

// HourAngle of the point raDeg at t from the longitude lon, in hours from
// -12 to 12: negative east of the meridian, 0 on it, positive after it.
func HourAngle(t time.Time, lon, raDeg float64) float64 {
	h := math.Mod(280.16+360.9856235*days(t)+lon-raDeg, 360)
	if h < 0 {
		h += 360
	}
	if h >= 180 {
		h -= 360
	}
	return h / 15
}

// days since J2000 (as SunCalc counts them)
func days(t time.Time) float64 { return float64(t.UnixMilli())/864e5 - 0.5 + 2440588 - 2451545 }

// altAz: altitude (refraction included) and azimuth from the north, in
// degrees, of a point at ra, dec (radians).
func altAz(d, lat, lon, ra, dec float64) (alt, az float64) {
	H := rad*(280.16+360.9856235*d) + rad*lon - ra
	p := rad * lat
	h := math.Asin(math.Sin(p)*math.Sin(dec) + math.Cos(p)*math.Cos(dec)*math.Cos(H))
	hr := math.Max(h, 0)
	h += 0.0002967 / math.Tan(hr+0.00312536/(hr+0.08901179)) // refraction
	a := math.Atan2(math.Sin(H), math.Cos(H)*math.Sin(p)-math.Tan(dec)*math.Cos(p))/rad + 180
	return h / rad, math.Mod(a+360, 360)
}

// Pos is a point of the sky seen from the observatory, in degrees.
type Pos struct{ Alt, Az float64 }

// Object is where the point raDeg, decDeg (J2000, degrees) was at t.
func Object(t time.Time, lat, lon, raDeg, decDeg float64) Pos {
	alt, az := altAz(days(t), lat, lon, raDeg*rad, decDeg*rad)
	return Pos{alt, az}
}

// Separation is the angle between two points of the sky, in degrees.
func Separation(a, b Pos) float64 {
	c := math.Sin(a.Alt*rad)*math.Sin(b.Alt*rad) + math.Cos(a.Alt*rad)*math.Cos(b.Alt*rad)*math.Cos((a.Az-b.Az)*rad)
	return math.Acos(math.Max(-1, math.Min(1, c))) / rad
}

// Target reads where the telescope pointed from a FITS header: RA and DEC
// in degrees (ASIAIR), or OBJCTRA in hours and OBJCTDEC.
func Target(get func(string) string) (ra, dec float64, ok bool) {
	if r, err := strconv.ParseFloat(strings.TrimSpace(get("RA")), 64); err == nil {
		if d, err := strconv.ParseFloat(strings.TrimSpace(get("DEC")), 64); err == nil {
			return r, d, true
		}
	}
	r, ok1 := ParseAngle(get("OBJCTRA"))
	d, ok2 := ParseAngle(get("OBJCTDEC"))
	return r * 15, d, ok1 && ok2
}

// Site reads the coordinates ASIAIR writes into its FITS headers
// (SITELAT, SITELONG).
func Site(get func(string) string) (lat, lon float64, ok bool) {
	lat, ok1 := ParseAngle(get("SITELAT"))
	lon, ok2 := ParseAngle(get("SITELONG"))
	if !ok1 || !ok2 || lat == 0 && lon == 0 || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// ParseAngle reads 55.75, "55.75", "55 45 00", "+55:45:00" or "55d45m00s".
func ParseAngle(v string) (float64, bool) {
	v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "'"))
	if v == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f, true
	}
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimLeft(v, "+-")
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == ':' || r == 'd' || r == 'm' || r == 's' || r == '°' || r == '\'' || r == '"'
	})
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	deg := 0.0
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		deg += f / math.Pow(60, float64(i))
	}
	if neg {
		deg = -deg
	}
	return deg, true
}

// Compass is the name of the direction az (degrees): С, СВ, В, …
func Compass(az float64) string {
	names := []string{"С", "СВ", "В", "ЮВ", "Ю", "ЮЗ", "З", "СЗ"}
	return names[int(math.Mod(az+22.5+360, 360)/45)%8]
}
