package main

import (
	"testing"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// TestFavOpsNotes: a note goes with an object added in the same changes,
// alone for one already there, and not for one removed.
func TestFavOpsNotes(t *testing.T) {
	ops := []favOp{
		{Add: &planner.Favorite{Name: "A"}}, {NoteOf: "A", Note: "a"},
		{NoteOf: "B", Note: "b"},
		{NoteOf: "C", Note: "c"}, {Remove: "C"},
	}
	add, remove, notes := favOpsSum(ops)
	if len(add) != 1 || add[0].Note != "a" || len(remove) != 1 || remove[0] != "C" || len(notes) != 1 || notes["B"] != "b" {
		t.Fatalf("%+v %v %v", add, remove, notes)
	}
}
