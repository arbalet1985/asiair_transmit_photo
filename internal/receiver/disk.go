package receiver

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
)

// diskInfo is the free space on the disk with the photos, for the page.
type diskInfo struct {
	Free    uint64 `json:"free"`
	Total   uint64 `json:"total"`
	Reserve uint64 `json:"reserve"`  // bytes kept free
	MinFree string `json:"min_free"` // the setting, e.g. "5%"
	Full    string `json:"full"`     // why uploads are refused, "" = accepted
	Error   string `json:"error,omitempty"`
}

// disk reports the free space. Uploads stop when accepting the last refused
// file (need) would eat into the reserve.
func (s *Server) disk() diskInfo {
	s.mu.RLock()
	res, minFree, root, need := s.reserve, s.minFree, s.store.root, s.spaceNeed
	s.mu.RUnlock()
	d := diskInfo{MinFree: minFree}
	free, total, err := diskSpace(root)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	d.Free, d.Total, d.Reserve = free, total, res.Of(total)
	if free < d.Reserve+need {
		d.Full = diskFullMsg(free, d.Reserve)
	}
	return d
}

func diskFullMsg(free, reserve uint64) string {
	return fmt.Sprintf("на приёмнике мало места: свободно %s, запас %s — освободите место или уменьшите запас на странице приёмника",
		config.FormatBytes(int64(free)), config.FormatBytes(int64(reserve)))
}

// checkSpace reports whether need more bytes fit on the disk above the
// reserve. A refused size is remembered so the agent stays paused until
// enough space is freed.
func (s *Server) checkSpace(need int64) string {
	s.mu.RLock()
	res, root := s.reserve, s.store.root
	s.mu.RUnlock()
	free, total, err := diskSpace(root)
	if err != nil {
		return "" // cannot tell: let the write itself fail if it must
	}
	reserve := res.Of(total)
	s.mu.Lock()
	defer s.mu.Unlock()
	if need < 0 {
		need = 0
	}
	if free < reserve+uint64(need) {
		if s.spaceNeed == 0 {
			log.Printf("Приёмник: приём остановлен, мало места на диске: свободно %s, запас %s, файлу нужно %s",
				config.FormatBytes(int64(free)), config.FormatBytes(int64(reserve)), config.FormatBytes(need))
		}
		s.spaceNeed = uint64(need)
		return diskFullMsg(free, reserve)
	}
	if s.spaceNeed != 0 {
		log.Printf("Приёмник: места на диске достаточно, приём продолжен")
	}
	s.spaceNeed = 0
	return ""
}

// SetMinFree changes the reserve and saves it in receiver.yaml.
func (s *Server) SetMinFree(v string) error {
	v = strings.TrimSpace(v)
	res, err := config.ParseReserve(v)
	if err != nil {
		return err
	}
	if s.cfg.Path != "" {
		if err := config.UpdateReceiverFile(s.cfg.Path, map[string]string{"min_free": v}); err != nil {
			return fmt.Errorf("не удалось сохранить настройки: %w", err)
		}
	}
	s.mu.Lock()
	s.reserve, s.minFree, s.spaceNeed = res, v, 0
	s.mu.Unlock()
	log.Printf("Приёмник: запас свободного места: %s", res)
	return nil
}

func (s *Server) handleUIMinFree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MinFree string `json:"min_free"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.SetMinFree(req.MinFree); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.disk())
}
