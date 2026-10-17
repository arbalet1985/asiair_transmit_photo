package sorter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/arbalet1985/asiair_transmit_photo/internal/satinfo"
)

// SatInfo asks the receiver what open sources know about a satellite (the
// receiver asks them once and keeps the answer). An older receiver
// without it answers with an error.
func (r *Receiver) SatInfo(ctx context.Context, norad int, name string) (*satinfo.Info, error) {
	resp, err := r.get(ctx, "/ui/api/satinfo", url.Values{"norad": {strconv.Itoa(norad)}, "name": {name}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var i satinfo.Info
	if err := json.NewDecoder(resp.Body).Decode(&i); err != nil {
		return nil, fmt.Errorf("ответ приёмника не прочитан: %w", err)
	}
	return &i, nil
}

// SatPhoto reads the picture of a satellite the receiver keeps (JPEG).
func (r *Receiver) SatPhoto(ctx context.Context, norad int) ([]byte, error) {
	resp, err := r.get(ctx, "/ui/api/satinfo/photo", url.Values{"norad": {strconv.Itoa(norad)}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}
