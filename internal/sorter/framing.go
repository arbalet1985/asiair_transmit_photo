package sorter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// FrameFraming is a Light frame for the planner: its target with the centre
// and the camera angle of the frame, and its picture with the WCS to lay
// it over the sky of the atlas.
type FrameFraming struct {
	Name          string         `json:"name"`
	RA, Dec       float64        `json:"-"`
	Angle         float64        `json:"angle"`
	Width, Height float64        `json:"-"` // degrees
	Approx        bool           `json:"approx"`
	Exposure      float64        `json:"exposure"`
	Filter        string         `json:"filter"`
	WCS           map[string]any `json:"wcs"`
	JPG           []byte         `json:"-"`
}

// framingWidth: the width of the picture over the atlas, pixels.
const framingWidth = 1600

// Framing reads the framing of a Light frame: from the file on disk (local,
// or the frame's own file), else from the receiver it is on.
func Framing(ctx context.Context, f *Frame, local string) (*FrameFraming, error) {
	if local == "" && f.Src == nil {
		local = f.Path
	}
	if local == "" {
		return f.Src.framing(ctx, f.Path)
	}
	fh, err := os.Open(local)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	img, err := fits.Preview(fh, framingWidth)
	if err != nil {
		return nil, err
	}
	fr, ok := fits.FramingOf(img.Header, filepath.Base(local), img.Width, img.Height, img.Binning)
	if !ok {
		return nil, errors.New("в кадре нет ни решения (CRVAL, CD), ни наведения с фокусом и пикселем (RA, DEC, FOCALLEN, XPIXSZ)")
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img.Stretched(), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	name := f.Object
	if name == "" {
		name = path.Base(path.Dir(f.Rel))
	}
	return &FrameFraming{Name: name, RA: fr.RA, Dec: fr.Dec, Angle: fr.Angle, Width: fr.Width, Height: fr.Height,
		Approx: fr.Approx, Exposure: f.Exposure, Filter: f.Filter, WCS: fr.WCS, JPG: b.Bytes()}, nil
}

// framing asks the receiver for the framing of a frame and its picture.
func (r *Receiver) framing(ctx context.Context, p string) (*FrameFraming, error) {
	resp, err := r.get(ctx, "/ui/api/framing", url.Values{"path": {p}}, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ответил 404: страницы нет") {
			return nil, errors.New("кадрирование по кадру приёмника: нужен приёмник новой версии")
		}
		return nil, err
	}
	var v struct {
		FrameFraming
		RA     float64 `json:"ra"`
		Dec    float64 `json:"dec"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Image  string  `json:"image"`
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	fr := v.FrameFraming
	fr.RA, fr.Dec, fr.Width, fr.Height = v.RA, v.Dec, v.Width, v.Height
	resp, err = r.get(ctx, "/ui/api/framejpg", url.Values{"path": {p}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if fr.JPG, err = io.ReadAll(io.LimitReader(resp.Body, 32<<20)); err != nil {
		return nil, err
	}
	return &fr, nil
}

// ReceiverSky is the sky of the observatory kept on a receiver: its
// horizon and the zones hidden by wires, and the site.
type ReceiverSky struct {
	Sky     planner.Sky
	Lat     float64
	Lon     float64
	SiteSet bool
	TZ      *float64 // hours from UTC of the observatory clock (nil: not set there)
	Edited  bool     // corrected on the page, not only guessed from the frames
}

// PlannerSky asks the receiver for the sky of its planner.
func (r *Receiver) PlannerSky(ctx context.Context) (*ReceiverSky, error) {
	resp, err := r.get(ctx, "/ui/api/planner", nil, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ответил 404: страницы нет") {
			return nil, errors.New("на приёмнике нет планировщика: нужен приёмник новой версии")
		}
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		State struct {
			Sky    planner.Sky `json:"sky"`
			SkySet bool        `json:"sky_set"`
			TZ     *float64    `json:"tz"`
		} `json:"state"`
		Site struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
			Set bool    `json:"set"`
		} `json:"site"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &ReceiverSky{Sky: v.State.Sky, Lat: v.Site.Lat, Lon: v.Site.Lon, SiteSet: v.Site.Set, TZ: v.State.TZ, Edited: v.State.SkySet}, nil
}

// ErrNoFavorites: the receiver is older than the favorites of the planner.
var ErrNoFavorites = errors.New("на приёмнике нет избранных объектов: нужен приёмник новой версии")

// Favorites asks the receiver for the favorite objects of its planner.
func (r *Receiver) Favorites(ctx context.Context) ([]planner.Favorite, error) {
	resp, err := r.get(ctx, "/ui/api/favorites", nil, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ответил 404") {
			return nil, ErrNoFavorites
		}
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Favorites []planner.Favorite `json:"favorites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return v.Favorites, nil
}

// ChangeFavorites adds and removes (by name) favorite objects on the
// receiver, sets their notes (name → note) and gives its list after that.
func (r *Receiver) ChangeFavorites(ctx context.Context, add []planner.Favorite, remove []string, notes map[string]string) ([]planner.Favorite, error) {
	var v struct {
		Favorites []planner.Favorite `json:"favorites"`
	}
	if err := r.post(ctx, "/ui/api/favorites", map[string]any{"add": add, "remove": remove, "notes": notes}, &v); err != nil {
		if strings.Contains(err.Error(), "ответил 404") {
			return nil, ErrNoFavorites
		}
		return nil, err
	}
	return v.Favorites, nil
}
