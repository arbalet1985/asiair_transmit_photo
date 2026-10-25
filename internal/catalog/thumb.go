package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A picture of an object, as the lists of ASIAIR show: the sky around it
// (DSS2 in colour) from the hips2fits service of CDS over the internet,
// kept on disk so that each is asked once.

// ThumbPx: the side of the picture, pixels.
const ThumbPx = 160

var (
	thumbSem    = make(chan struct{}, 4) // asked of CDS at once
	thumbMu     sync.Mutex
	thumbFailed = map[string]time.Time{} // not asked again for a while
)

// ThumbFOV: the field of the picture of an object of size arcminutes
// across (0: unknown), degrees.
func ThumbFOV(size float64) float64 {
	if size <= 0 {
		size = 15
	}
	return math.Min(math.Max(size*1.6/60, 0.15), 6)
}

// SizeOf: the size of the object of that name in the catalog, arcminutes
// (0 when not known).
func SizeOf(name string) float64 {
	if o, ok := Lookup(name); ok {
		return o.Size
	}
	return 0
}

// Thumb is the picture (JPEG) of the sky around ra, dec (J2000, degrees)
// of fov degrees; dir keeps the pictures ("" = not kept).
func Thumb(ctx context.Context, dir string, ra, dec, fov float64) ([]byte, error) {
	key := fmt.Sprintf("%.3f_%+.3f_%.2f", ra, dec, fov)
	file := ""
	if dir != "" {
		file = filepath.Join(dir, key+".jpg")
		if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	thumbMu.Lock()
	failed := time.Since(thumbFailed[key]) < 10*time.Minute
	thumbMu.Unlock()
	if failed {
		return nil, errors.New("картинка недавно не получена, повтор позже")
	}
	select {
	case thumbSem <- struct{}{}:
		defer func() { <-thumbSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b, err := fetchThumb(ctx, ra, dec, fov)
	if err != nil {
		if ctx.Err() == nil {
			thumbMu.Lock()
			thumbFailed[key] = time.Now()
			thumbMu.Unlock()
		}
		return nil, err
	}
	if file != "" && os.MkdirAll(dir, 0o755) == nil {
		tmp := file + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, file)
		}
	}
	return b, nil
}

func fetchThumb(ctx context.Context, ra, dec, fov float64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := url.Values{"hips": {"CDS/P/DSS2/color"}, "width": {fmt.Sprint(ThumbPx)}, "height": {fmt.Sprint(ThumbPx)},
		"fov": {fmt.Sprintf("%.3f", fov)}, "projection": {"TAN"}, "ra": {fmt.Sprintf("%.5f", ra)}, "dec": {fmt.Sprintf("%.5f", dec)}, "format": {"jpg"}}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://alasky.cds.unistra.fr/hips-image-services/hips2fits?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CDS hips2fits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDS hips2fits: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 3 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, errors.New("CDS hips2fits: не картинка")
	}
	return b, nil
}
