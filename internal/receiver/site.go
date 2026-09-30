package receiver

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
)

// Site is where the observatory is: the web page uses it to tell how high
// the Moon was when a frame was taken.
type Site struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"` // east positive
	Set    bool    `json:"set"`
	Source string  `json:"source,omitempty"` // file the coordinates were read from
}

func (s *State) SetSite(site Site) error {
	if site.Set && (math.Abs(site.Lat) > 90 || math.Abs(site.Lon) > 180) {
		return errors.New("широта от -90 до 90, долгота от -180 до 180")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p.Site = site
	return s.save()
}

func parseAngle(v string) (float64, bool) { return moon.ParseAngle(v) }

// siteFromHeader reads the coordinates ASIAIR writes into its FITS files.
func siteFromHeader(h *fits.Header) (Site, bool) {
	lat, ok1 := parseAngle(h.Get("SITELAT"))
	lon, ok2 := parseAngle(h.Get("SITELONG"))
	if !ok1 || !ok2 || lat == 0 && lon == 0 || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return Site{}, false
	}
	return Site{Lat: lat, Lon: lon, Set: true}, true
}

// detectSite looks for the coordinates in the newest FITS files received.
func (s *Server) detectSite() (Site, error) {
	snap := s.state.Snapshot()
	shares := map[string]bool{"EMMC Images": true, "Udisk": true}
	for _, f := range snap.Inventory {
		if sh, _, ok := strings.Cut(f.Path, "/"); ok {
			shares[sh] = true
		}
	}
	var list []string
	for sh := range shares {
		list = append(list, sh)
	}
	store := s.getStore()
	local, err := store.List(list)
	if err != nil {
		return Site{}, err
	}
	var fitsFiles []LocalFile
	for _, f := range local {
		if n := strings.ToLower(f.Path); strings.HasSuffix(n, ".fit") || strings.HasSuffix(n, ".fits") {
			fitsFiles = append(fitsFiles, f)
		}
	}
	if len(fitsFiles) == 0 {
		return Site{}, errors.New("здесь ещё нет ни одного FITS-файла")
	}
	sort.Slice(fitsFiles, func(i, j int) bool { return fitsFiles[i].ModTime.After(fitsFiles[j].ModTime) })
	for i, f := range fitsFiles {
		if i >= 20 {
			break
		}
		fh, err := os.Open(store.Local(f.Path))
		if err != nil {
			continue
		}
		h, err := fits.ReadHeader(fh)
		fh.Close()
		if err != nil {
			continue
		}
		if site, ok := siteFromHeader(h); ok {
			site.Source = f.Path
			return site, nil
		}
	}
	return Site{}, errors.New("в последних FITS-файлах нет координат (SITELAT, SITELONG): введите их вручную")
}

func (s *Server) handleUISite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lat    *float64 `json:"lat"`
		Lon    *float64 `json:"lon"`
		Detect bool     `json:"detect"`
		Clear  bool     `json:"clear"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var site Site
	switch {
	case req.Clear:
	case req.Detect:
		var err error
		if site, err = s.detectSite(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case req.Lat != nil && req.Lon != nil:
		site = Site{Lat: *req.Lat, Lon: *req.Lon, Set: true}
	default:
		http.Error(w, "нужны широта и долгота", http.StatusBadRequest)
		return
	}
	if err := s.state.SetSite(site); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if site.Set {
		log.Printf("Приёмник: координаты обсерватории: %s", fmtSite(site))
	}
	writeJSON(w, site)
}

func fmtSite(s Site) string {
	t := fmt.Sprintf("%.4f°, %.4f°", s.Lat, s.Lon)
	if s.Source != "" {
		t += " (из " + s.Source + ")"
	}
	return t
}
