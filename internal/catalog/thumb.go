package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
// brightened as the atlas shows it (a faint nebula is black as it comes) and
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
	key := fmt.Sprintf("%.3f_%+.3f_%.2f_g", ra, dec, fov) // _g: brightened as in the atlas
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
	px := fmt.Sprint(ThumbPx * thumbScale)
	q := url.Values{"hips": {"CDS/P/DSS2/color"}, "width": {px}, "height": {px},
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
	if s, err := Brighten(b, thumbScale); err == nil {
		b = s
	}
	return b, nil
}

// thumbScale: the picture is asked this many times larger and made smaller
// here: the noise and the seams of the DSS plates brightened smooth out.
const thumbScale = 3

// Brighten brings out a faint picture of the sky as the atlas shows it in
// Aladin: a gamma of 0.6 and the colours a little stronger (saturation
// 1.3), then made scale times smaller.
func Brighten(jpg []byte, scale int) ([]byte, error) {
	im, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, err
	}
	r := im.Bounds()
	scale = max(scale, 1)
	w, h := r.Dx()/scale, r.Dy()/scale
	if w == 0 || h == 0 {
		return nil, errors.New("пустая картинка")
	}
	var lut [256]float64
	for v := range lut {
		lut[v] = 255 * math.Pow(float64(v)/255, 0.6)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c [3]float64
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					cr, cg, cb, _ := im.At(r.Min.X+x*scale+dx, r.Min.Y+y*scale+dy).RGBA()
					c[0] += lut[cr>>8]
					c[1] += lut[cg>>8]
					c[2] += lut[cb>>8]
				}
			}
			n := float64(scale * scale)
			l := (c[0] + c[1] + c[2]) / 3 / n
			o := y*out.Stride + x*4
			for k := range c {
				out.Pix[o+k] = uint8(math.Min(255, math.Max(0, l+(c[k]/n-l)*1.3)))
			}
			out.Pix[o+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
