package receiver

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// The favorite objects of the planner (kept in its state, also changed by
// the sorter through /ui/api/favorites) and how objects are seen in a
// night with the filter of ASIAIR (planner.See, planner.Best).

const maxVisObjects = 200

func (s *State) Favorites() []planner.Favorite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]planner.Favorite{}, s.p.Planner.Favorites...)
}

// ChangeFavorites adds and removes favorites (by name) and then sets the
// notes of favorites (by name; "" takes the note away), in one step so that
// a page and a sorter changing them at once do not undo each other.
func (s *State) ChangeFavorites(add []planner.Favorite, remove []string, notes map[string]string) ([]planner.Favorite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := map[string]bool{}
	for _, n := range remove {
		gone[n] = true
	}
	out := []planner.Favorite{}
	for _, f := range s.p.Planner.Favorites {
		if !gone[f.Name] {
			out = append(out, f)
		}
	}
	for _, f := range add {
		if f.Name = strings.TrimSpace(f.Name); f.Name == "" {
			continue
		}
		dup := false
		for _, g := range out {
			dup = dup || g.Name == f.Name
		}
		if !dup {
			f.Note = planner.CleanNote(f.Note)
			out = append(out, f)
		}
	}
	for i := range out {
		if n, ok := notes[out[i].Name]; ok {
			out[i].Note = planner.CleanNote(n)
		}
	}
	s.p.Planner.Favorites = out
	return append([]planner.Favorite{}, out...), s.save()
}

func (s *Server) handleUIFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Add    []planner.Favorite `json:"add"`
			Remove []string           `json:"remove"`
			Notes  map[string]string  `json:"notes,omitempty"` // name → note
		}
		if !readJSON(w, r, &req) {
			return
		}
		out, err := s.state.ChangeFavorites(req.Add, req.Remove, req.Notes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"favorites": out})
		return
	}
	writeJSON(w, map[string]any{"favorites": s.state.Favorites()})
}

func (s *Server) handleUIVisibility(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Night   string             `json:"night"`
		SunAlt  float64            `json:"sun_alt"`
		Sky     planner.Sky        `json:"sky"`
		Objects []planner.Favorite `json:"objects"`
		Filter  *planner.Filter    `json:"filter,omitempty"`
		Best    bool               `json:"best,omitempty"` // the objects: the best of the catalog in the filter
	}
	if !readJSON(w, r, &req) {
		return
	}
	site := s.state.Snapshot().Site
	if !site.Set {
		http.Error(w, "задайте координаты обсерватории (блок «Координаты обсерватории» в настройках)", http.StatusBadRequest)
		return
	}
	if len(req.Objects) > maxVisObjects {
		req.Objects = req.Objects[:maxVisObjects]
	}
	if req.SunAlt == 0 {
		req.SunAlt = planner.DefaultParams().SunAlt
	}
	if req.Best {
		if req.Filter == nil {
			req.Filter = &planner.Filter{AltMin: 30, AltMax: 90}
		}
		var err error
		if req.Objects, err = planner.Best(req.Night, site.Lat, site.Lon, req.SunAlt, req.Sky, req.Filter); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	v, err := planner.See(req.Night, site.Lat, site.Lon, req.SunAlt, req.Sky, req.Objects, req.Filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := map[string]any{"from": v.From.UnixMilli(), "to": v.To.UnixMilli(), "every": planner.VisEvery.Milliseconds(), "objects": v.Objects}
	if !v.Dusk.IsZero() {
		res["dusk"], res["dawn"] = v.Dusk.UnixMilli(), v.Dawn.UnixMilli()
	}
	if req.Filter != nil {
		res["filter_from"], res["filter_to"] = v.FilterFrom.UnixMilli(), v.FilterTo.UnixMilli()
	}
	if req.Best {
		res["list"] = req.Objects
	}
	writeJSON(w, res)
}

// handleUIObjectPic: the picture of an object (catalog.Thumb), kept in
// receiver-data/thumbs; size (arcminutes) or else the name gives its field.
// With fw and fh (degrees): the field of the camera (catalog.View).
func (s *Server) handleUIObjectPic(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ra, err1 := strconv.ParseFloat(q.Get("ra"), 64)
	dec, err2 := strconv.ParseFloat(q.Get("dec"), 64)
	if err1 != nil || err2 != nil || ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
		http.Error(w, "нужны ra и dec в градусах", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "thumbs")
	var b []byte
	var err error
	fw, _ := strconv.ParseFloat(q.Get("fw"), 64)
	fh, _ := strconv.ParseFloat(q.Get("fh"), 64)
	if fw > 0 && fh > 0 {
		b, err = catalog.View(r.Context(), dir, ra, dec, fw, fh)
	} else {
		size, _ := strconv.ParseFloat(q.Get("size"), 64)
		if size <= 0 {
			size = catalog.SizeOf(q.Get("name"))
		}
		b, err = catalog.Thumb(r.Context(), dir, ra, dec, catalog.ThumbFOV(size))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=2592000")
	w.Write(b)
}
