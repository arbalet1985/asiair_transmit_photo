package planner

import (
	"sort"
	"strings"
	"time"
)

// Optics is the telescope and the camera that make the field of view drawn
// on the atlas: the focal length times the reducer, the image in pixels and
// the size of one of its pixels (as XPIXSZ: with the binning).
type Optics struct {
	Camera  string  `json:"camera,omitempty"`
	Focal   float64 `json:"focal,omitempty"`   // mm, as in the frames (FOCALLEN)
	Reducer float64 `json:"reducer,omitempty"` // 0.8 for a 0.8× reducer; 0 = none (1)
	Pixel   float64 `json:"pixel,omitempty"`   // µm
	Width   int     `json:"width,omitempty"`   // pixels
	Height  int     `json:"height,omitempty"`
}

// FOV is the field of view, degrees (0 when the optics are not known).
func (o Optics) FOV() (w, h float64) {
	r := o.Reducer
	if r <= 0 {
		r = 1
	}
	f := o.Focal * r
	if f <= 0 || o.Pixel <= 0 {
		return 0, 0
	}
	scale := o.Pixel / 1000 / f * 180 / 3.141592653589793 // degrees per pixel
	return float64(o.Width) * scale, float64(o.Height) * scale
}

// InEffect: the optics the atlas draws the frame with: the ones chosen
// when complete, else the last setup of the frames with the reducer chosen.
func InEffect(chosen *Optics, setups []Setup) Optics {
	var o Optics
	if chosen != nil {
		o = *chosen
	}
	if o.Focal > 0 && o.Pixel > 0 && o.Width > 0 && o.Height > 0 || len(setups) == 0 {
		return o
	}
	s := setups[0].Optics
	s.Reducer = o.Reducer
	return s
}

// Setup is the optics of frames taken: how many and the last one.
type Setup struct {
	Optics
	Frames int       `json:"frames"`
	Last   time.Time `json:"last"`
}

// SetupFrame is what a frame tells of its optics.
type SetupFrame struct {
	Camera        string
	Focal, Pixel  float64
	Width, Height int
	Time          time.Time
}

// SetupsOf: the different optics of the frames (the camera, the focal
// length, the pixel and the image), the last used first. Frames without a
// focal length or a pixel size are left out.
func SetupsOf(frames []SetupFrame) []Setup {
	idx := map[Optics]int{}
	var out []Setup
	for _, f := range frames {
		if f.Focal <= 0 || f.Pixel <= 0 || f.Width <= 0 || f.Height <= 0 {
			continue
		}
		o := Optics{Camera: strings.TrimSpace(f.Camera), Focal: f.Focal, Pixel: f.Pixel, Width: f.Width, Height: f.Height}
		i, ok := idx[o]
		if !ok {
			i = len(out)
			idx[o] = i
			out = append(out, Setup{Optics: o})
		}
		out[i].Frames++
		if f.Time.After(out[i].Last) {
			out[i].Last = f.Time
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}
