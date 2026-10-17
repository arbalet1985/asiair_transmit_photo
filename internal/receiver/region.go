package receiver

import (
	"net/http"
	"os"
	"strconv"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
)

// handleUIRegion: GET ?path=&x=&y=&r= counts the statistics of a small area
// of a received FITS frame from the file itself, around the brightest pixel
// near x, y (pixels of the frame from the top left): is the star there
// saturated.
func (s *Server) handleUIRegion(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := CleanPath(q.Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !isFITS(p) {
		http.Error(w, "не FITS-файл", http.StatusBadRequest)
		return
	}
	x, errX := strconv.Atoi(q.Get("x"))
	y, errY := strconv.Atoi(q.Get("y"))
	if errX != nil || errY != nil {
		http.Error(w, "нужны x и y", http.StatusBadRequest)
		return
	}
	rad, err := strconv.Atoi(q.Get("r"))
	if err != nil || rad <= 0 {
		rad = 10
	}
	f, err := os.Open(s.getStore().Local(p))
	if err != nil {
		http.Error(w, "файл не найден", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reg, err := fits.ReadRegion(f, st.Size(), x, y, rad)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, reg)
}

// handleUISaturated: GET ?path= finds the saturated areas of a received
// FITS frame, read from the file itself at its full resolution.
func (s *Server) handleUISaturated(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !isFITS(p) {
		http.Error(w, "не FITS-файл", http.StatusBadRequest)
		return
	}
	f, err := os.Open(s.getStore().Local(p))
	if err != nil {
		http.Error(w, "файл не найден", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, err := fits.ReadSaturated(f, st.Size())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, a)
}
