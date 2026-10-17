package sorter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Deleting a frame everywhere: on the ASIAIR and on the receiver. The
// receiver is asked the way its page does it: a frame still on the ASIAIR
// gets a delete command with the receiver's copy (the agent deletes it on
// the ASIAIR, then the receiver its copy), a frame the ASIAIR has no more
// has only its copy on the receiver deleted.

// DeleteResult tells what the receiver did with the frames.
type DeleteResult struct {
	Queued  int // to be deleted on the ASIAIR (and then on the receiver)
	Deleted int // copies on the receiver deleted (the ASIAIR has them no more)
	Absent  int // neither on the ASIAIR nor on the receiver
	// Failed: the frames not deleted and why
	Failed map[string]string
}

// Done reports whether the frame p is deleted or will be.
func (d DeleteResult) Done(p string) bool {
	_, failed := d.Failed[p]
	return !failed
}

// post sends a JSON request to the page API and reads the answer into out.
func (r *Receiver) post(ctx context.Context, p string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url(p, nil), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("ответ приёмника не прочитан: %w", err)
	}
	return nil
}

// DeleteEverywhere deletes the files (paths on the receiver) on the ASIAIR
// and on the receiver. err: the receiver was not reached, nothing is known
// to be deleted.
func (r *Receiver) DeleteEverywhere(ctx context.Context, paths []string) (DeleteResult, error) {
	res := DeleteResult{Failed: map[string]string{}}
	resp, err := r.get(ctx, "/ui/api/state", nil, nil)
	if err != nil {
		return res, err
	}
	var st struct {
		Files []struct {
			Path    string `json:"path"`
			Local   bool   `json:"local"`
			Remote  bool   `json:"remote"`
			Pending bool   `json:"pending"`
		} `json:"files"`
		Commands []deleteCmd `json:"commands"`
	}
	err = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if err != nil {
		return res, fmt.Errorf("ответ приёмника не прочитан: %w", err)
	}
	type file struct{ local, remote, pending bool }
	files := make(map[string]file, len(st.Files))
	for _, f := range st.Files {
		files[f.Path] = file{f.Local, f.Remote, f.Pending}
	}
	var queue, here []string
	for _, p := range paths {
		f, ok := files[p]
		switch {
		case !ok || !f.remote && !f.local:
			res.Absent++
		case f.remote && f.pending:
			// a command is there already: with the copy of the receiver or not
			dropped := false
			for _, c := range st.Commands {
				if c.dropsLocal(p) {
					dropped = true
					break
				}
			}
			if dropped || !f.local {
				res.Queued++
			} else {
				res.Failed[p] = "уже в очереди на удаление на ASIAIR без копии приёмника: копию удалите на странице приёмника, когда файл удалится на ASIAIR"
			}
		case f.remote:
			queue = append(queue, p)
		default:
			here = append(here, p)
		}
	}
	if len(queue) > 0 {
		var out struct {
			Queued int `json:"queued"`
		}
		if err := r.post(ctx, "/ui/api/delete", map[string]any{"paths": queue, "also_local": true}, &out); err != nil {
			for _, p := range queue {
				res.Failed[p] = err.Error()
			}
		} else {
			res.Queued += len(queue) // those queued before are not counted by the receiver
		}
	}
	if len(here) > 0 {
		var out struct {
			Deleted int      `json:"deleted"`
			Errors  []string `json:"errors"`
		}
		if err := r.post(ctx, "/ui/api/delete_local", map[string]any{"paths": here}, &out); err != nil {
			for _, p := range here {
				res.Failed[p] = err.Error()
			}
		} else {
			res.Deleted += out.Deleted
			// the errors are "path: why"
			for _, e := range out.Errors {
				for _, p := range here {
					if why, ok := strings.CutPrefix(e, p+": "); ok {
						res.Failed[p] = why
						break
					}
				}
			}
		}
	}
	return res, nil
}
