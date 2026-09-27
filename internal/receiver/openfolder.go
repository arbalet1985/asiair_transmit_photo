package receiver

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// openInFolder shows a received file or folder in the file manager of this
// computer (Explorer selects the file). Tests replace it.
var openInFolder = func(local string, isDir bool) error {
	switch runtime.GOOS {
	case "windows":
		if isDir {
			return exec.Command("explorer.exe", local).Start()
		}
		return exec.Command("explorer.exe", "/select,", local).Start()
	case "darwin":
		if isDir {
			return exec.Command("open", local).Start()
		}
		return exec.Command("open", "-R", local).Start()
	}
	if !isDir {
		local = filepath.Dir(local)
	}
	return exec.Command("xdg-open", local).Start()
}

// handleUIOpen: {"path": "EMMC Images/Autorun/Light/M31/x.fit"} — only on
// this computer.
func (s *Server) handleUIOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, err := CleanPath(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	local := s.getStore().Local(p)
	st, err := os.Stat(local)
	if err != nil {
		http.Error(w, "на этом компьютере такого файла или папки нет", http.StatusNotFound)
		return
	}
	if err := openInFolder(local, st.IsDir()); err != nil {
		http.Error(w, "не удалось открыть: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
