package receiver

import (
	"net/http"
	"os"
	"sync"

	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// tiltCache keeps the tilt measured of received frames (a measurement
// reads the frame at full resolution: about a second).
type tiltCache struct {
	mu    sync.Mutex
	items map[string]stars.Tilt
}

const tiltCacheMax = 2000 // the camera tilt takes up to 300 frames of each camera

// handleUITilt: GET ?path= measures the tilt of the sensor on a received
// FITS frame, as Siril's "tilt" command does.
func (s *Server) handleUITilt(w http.ResponseWriter, r *http.Request) {
	p, err := CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !isFITS(p) {
		http.Error(w, "не FITS-файл", http.StatusBadRequest)
		return
	}
	local := s.getStore().Local(p)
	if _, err := os.Stat(local); err != nil {
		http.Error(w, "файл не найден", http.StatusNotFound)
		return
	}
	t, err := s.tiltOf(p, local)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, t)
}
