package receiver

import (
	"fmt"
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

const tiltCacheMax = 500

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
	st, err := os.Stat(local)
	if err != nil {
		http.Error(w, "файл не найден", http.StatusNotFound)
		return
	}
	key := fmt.Sprintf("%s|%d|%d", local, st.Size(), st.ModTime().UnixNano())
	c := &s.tilts
	c.mu.Lock()
	t, ok := c.items[key]
	c.mu.Unlock()
	if !ok {
		t, err = stars.TiltFile(local)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		c.mu.Lock()
		if c.items == nil || len(c.items) >= tiltCacheMax {
			c.items = map[string]stars.Tilt{}
		}
		c.items[key] = t
		c.mu.Unlock()
	}
	writeJSON(w, t)
}
