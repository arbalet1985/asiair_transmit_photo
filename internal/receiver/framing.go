package receiver

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"path"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// A frame to the planner: its target with the same centre and camera
// angle, and its picture laid over the sky of the atlas.

// framingWidth: the width of the picture over the atlas, pixels.
const framingWidth = 1600

// framingPicture reads a received frame into its stretched picture (kept
// with the thumbnails) and its framing.
func (s *Server) framingPicture(p string) ([]byte, *fits.Framing, error) {
	local, key, err := s.framingKey(p)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	img, err := fits.Preview(f, framingWidth)
	if err != nil {
		return nil, nil, err
	}
	fr, ok := fits.FramingOf(img.Header, path.Base(p), img.Width, img.Height, img.Binning)
	if !ok {
		return nil, nil, errors.New("в кадре нет ни решения (CRVAL, CD), ни наведения с фокусом и пикселем (RA, DEC, FOCALLEN, XPIXSZ)")
	}
	jpg := s.thumbs.get(key)
	if jpg == nil {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img.Stretched(), &jpeg.Options{Quality: 85}); err != nil {
			return nil, nil, err
		}
		jpg = b.Bytes()
		s.thumbs.put(key, jpg)
	}
	return jpg, fr, nil
}

// framingKey: the file of a frame here and the key of its picture.
func (s *Server) framingKey(p string) (local, key string, err error) {
	local = s.getStore().Local(p)
	st, err := os.Stat(local)
	if err != nil {
		return "", "", errors.New("файл не найден")
	}
	return local, fmt.Sprintf("frame|%s|%d|%d", local, st.Size(), st.ModTime().UnixNano()), nil
}

// handleUIFraming: /ui/api/framing?path=… — the target of a Light frame
// (name, centre, camera angle, exposure, filter) and its picture for the
// atlas with the WCS of the picture.
func (s *Server) handleUIFraming(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil || !isFITS(p) {
		http.Error(w, "нужен FITS-кадр", http.StatusBadRequest)
		return
	}
	_, fr, err := s.framingPicture(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	info, _ := sorter.ReadFrame(s.getStore().Local(p), p)
	name := info.Object
	if name == "" {
		name = path.Base(path.Dir(p))
	}
	writeJSON(w, map[string]any{
		"name": name, "ra": fr.RA, "dec": fr.Dec, "angle": fr.Angle, "width": fr.Width, "height": fr.Height,
		"approx": fr.Approx, "exposure": info.Exposure, "filter": info.Filter, "night": info.Night,
		"image": "/ui/api/framejpg?path=" + url.QueryEscape(p), "wcs": fr.WCS,
	})
}

// handleUIFrameJPG: /ui/api/framejpg?path=… — the picture of the framing.
func (s *Server) handleUIFrameJPG(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil || !isFITS(p) {
		http.Error(w, "нужен FITS-кадр", http.StatusBadRequest)
		return
	}
	var jpg []byte
	if _, key, err := s.framingKey(p); err == nil {
		jpg = s.thumbs.get(key)
	}
	if jpg == nil {
		if jpg, _, err = s.framingPicture(p); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(jpg)
}
