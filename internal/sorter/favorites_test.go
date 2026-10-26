package sorter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

func TestFavorites(t *testing.T) {
	list := []planner.Favorite{{Name: "NGC 224", Label: "M 31 — Андромеда"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui/api/favorites" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Add    []planner.Favorite `json:"add"`
				Remove []string           `json:"remove"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if len(req.Remove) == 1 && req.Remove[0] == "NGC 224" {
				list = nil
			}
			list = append(list, req.Add...)
		}
		json.NewEncoder(w).Encode(map[string]any{"favorites": list})
	}))
	defer srv.Close()
	r, err := NewReceiver(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := r.Favorites(ctx); err != nil || len(got) != 1 || got[0].Name != "NGC 224" {
		t.Fatalf("favorites: %v %+v", err, got)
	}
	got, err := r.ChangeFavorites(ctx, []planner.Favorite{{Name: "IC 1805"}}, []string{"NGC 224"}, nil)
	if err != nil || len(got) != 1 || got[0].Name != "IC 1805" {
		t.Fatalf("changed: %v %+v", err, got)
	}

	old := httptest.NewServer(http.NotFoundHandler()) // a receiver of an older version
	defer old.Close()
	r, _ = NewReceiver(old.URL, "", "")
	if _, err := r.Favorites(ctx); !errors.Is(err, ErrNoFavorites) {
		t.Fatalf("old receiver: %v", err)
	}
}
