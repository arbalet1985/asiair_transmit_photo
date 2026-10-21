// Package atlas holds the atlas of the planner (web/atlas.js): the sky with
// the field of view of the targets and their framing, on the receiver page
// and on the sorter's atlas page (web/atlas.html).
package atlas

import (
	"embed"
	"net/http"
	"path"
)

//go:embed web
var webFS embed.FS

// Static answers with a file of the atlas: "atlas.js" or "atlas.html".
func Static(w http.ResponseWriter, name string) {
	b, err := webFS.ReadFile("web/" + path.Base(name))
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	switch path.Ext(name) {
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}
