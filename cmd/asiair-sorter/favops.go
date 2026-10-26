package main

import (
	"sort"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// favOp is a change of the favorites not yet sent to the receiver.
type favOp struct {
	Add    *planner.Favorite `json:"add,omitempty"`
	Remove string            `json:"remove,omitempty"`
	// the note of the favorite NoteOf set to Note ("" takes it away)
	NoteOf string `json:"note_of,omitempty"`
	Note   string `json:"note,omitempty"`
}

// favOpsSum: the changes as what to add, what to remove and the notes to
// set, the last change of an object counting.
func favOpsSum(ops []favOp) (add []planner.Favorite, remove []string, notes map[string]string) {
	adds, removes := map[string]planner.Favorite{}, map[string]bool{}
	notes = map[string]string{}
	var order []string
	for _, o := range ops {
		if o.NoteOf != "" {
			if f, ok := adds[o.NoteOf]; ok {
				f.Note = o.Note
				adds[o.NoteOf] = f
			} else {
				notes[o.NoteOf] = o.Note
			}
			continue
		}
		if o.Add != nil {
			delete(notes, o.Add.Name) // the favorite added carries its note
			delete(removes, o.Add.Name)
			if _, ok := adds[o.Add.Name]; !ok {
				order = append(order, o.Add.Name)
			}
			adds[o.Add.Name] = *o.Add
		} else if o.Remove != "" {
			delete(adds, o.Remove)
			delete(notes, o.Remove)
			removes[o.Remove] = true
		}
	}
	for _, n := range order {
		if f, ok := adds[n]; ok {
			add = append(add, f)
		}
	}
	for n := range removes {
		remove = append(remove, n)
	}
	sort.Strings(remove)
	return add, remove, notes
}
