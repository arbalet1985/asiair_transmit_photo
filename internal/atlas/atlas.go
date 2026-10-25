// Package atlas holds the atlas of the planner (web/atlas.js): the sky with
// the field of view of the targets and their framing, on the receiver page
// and on the sorter's atlas page (web/atlas.html).
package atlas

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
)

//go:generate go run gen.go

//go:embed web
var webFS embed.FS

// Static answers with a file of the atlas: "atlas.js", "skymap.js",
// "skydata.js" or "atlas.html"; r (may be nil) asked again gets "not
// modified" while the file is the same.
func Static(w http.ResponseWriter, name string, r ...*http.Request) {
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
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", tag)
	if len(r) > 0 && r[0] != nil && r[0].Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(b)
}
