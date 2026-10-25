package atlas

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
)

// The objects of the sky map (web/skymap.js): the well known and the large
// ones of the catalog, as [name, label, short name, kind, ra, dec, size,
// fame]; the rest are found by name (Find).

var (
	objectsOnce sync.Once
	objectsJSON []byte
)

// short: the name written on the map, the Messier number else the
// catalog name.
func short(o catalog.Object) string {
	for _, a := range o.Aliases {
		if strings.HasPrefix(a, "M ") {
			return a
		}
	}
	return o.Name
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Objects answers with the objects of the sky map.
func Objects(w http.ResponseWriter) {
	objectsOnce.Do(func() {
		out := [][]any{}
		for _, o := range catalog.All() {
			if catalog.Group(o.Kind) == "" {
				continue
			}
			f := o.Fame()
			if f == 2 && o.Size < 10 {
				continue
			}
			out = append(out, []any{o.Name, o.Label(), short(o), o.Kind, r3(o.RA), r3(o.Dec), math.Round(o.Size*10) / 10, f})
		}
		objectsJSON, _ = json.Marshal(out)
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(objectsJSON)
}

// Found is an object found by name for the sky map.
type Found struct {
	Name  string  `json:"name"`
	Label string  `json:"label"`
	RA    float64 `json:"ra"`
	Dec   float64 `json:"dec"`
	Kind  string  `json:"kind"`
	Size  float64 `json:"size,omitempty"`
}

// Find answers with the objects of the catalog of that name.
func Find(w http.ResponseWriter, q string) {
	out := []Found{}
	add := func(o catalog.Object) {
		for _, f := range out {
			if f.Name == o.Name {
				return
			}
		}
		out = append(out, Found{o.Name, o.Label(), o.RA, o.Dec, o.Kind, o.Size})
	}
	if o, ok := catalog.Lookup(q); ok {
		add(o)
	}
	for _, o := range catalog.Search(q, 20) {
		add(o)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
