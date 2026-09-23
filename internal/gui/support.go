// Package gui is the windowed mode of the agent: a small status window with
// a tray icon, autostart and "start minimized" options (Windows only). The
// helpers in this file are portable so they can be tested anywhere.
package gui

import (
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Settings are the window options, stored next to the agent configuration.
type Settings struct {
	StartMinimized bool `json:"start_minimized"`
}

// SettingsPath returns the settings file for a given agent config file.
func SettingsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "agent-ui.json")
}

func LoadSettings(path string) Settings {
	var s Settings
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func SaveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LogBuffer keeps the last lines of the log and reports new ones.
type LogBuffer struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
	onLine  func(string)
}

func NewLogBuffer(max int) *LogBuffer { return &LogBuffer{max: max} }

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.partial = append(b.partial, p...)
	var fresh []string
	for {
		i := strings.IndexByte(string(b.partial), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(b.partial[:i]), "\r")
		b.partial = b.partial[i+1:]
		b.lines = append(b.lines, line)
		fresh = append(fresh, line)
	}
	if over := len(b.lines) - b.max; over > 0 {
		b.lines = append([]string(nil), b.lines[over:]...)
	}
	cb := b.onLine
	b.mu.Unlock()
	if cb != nil {
		for _, l := range fresh {
			cb(l)
		}
	}
	return len(p), nil
}

// Lines returns the buffered lines.
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

// OnLine sets the callback for new lines.
func (b *LogBuffer) OnLine(f func(string)) {
	b.mu.Lock()
	b.onLine = f
	b.mu.Unlock()
}

// Tray icon states.
const (
	StateStopped   = "stopped"   // agent not running (config error)
	StateError     = "error"     // no connection to the ASIAIR or receiver
	StateIdle      = "idle"      // connected, nothing to send
	StateUploading = "uploading" // sending a file
)

var stateColors = map[string]color.RGBA{
	StateStopped:   {128, 128, 128, 255},
	StateError:     {214, 64, 58, 255},
	StateIdle:      {46, 160, 90, 255},
	StateUploading: {47, 111, 222, 255},
}

// IconImage draws the tray icon: a coloured disc with a small white
// telescope-like mark, size×size pixels, antialiased.
func IconImage(state string, size int) *image.RGBA {
	c, ok := stateColors[state]
	if !ok {
		c = stateColors[StateStopped]
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	const sub = 4 // supersampling per axis
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var disc, mark int
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/sub) / s // 0..1
					py := (float64(y) + (float64(sy)+0.5)/sub) / s
					dx, dy := px-0.5, py-0.5
					if dx*dx+dy*dy <= 0.49*0.49 {
						disc++
						if inMark(px, py) {
							mark++
						}
					}
				}
			}
			n := sub * sub
			if disc == 0 {
				continue
			}
			// blend white mark over the coloured disc, premultiplied alpha
			a := float64(disc) / float64(n)
			m := float64(mark) / float64(disc)
			r := (float64(c.R)*(1-m) + 255*m) * a
			g := (float64(c.G)*(1-m) + 255*m) * a
			bl := (float64(c.B)*(1-m) + 255*m) * a
			img.SetRGBA(x, y, color.RGBA{uint8(r), uint8(g), uint8(bl), uint8(math.Round(a * 255))})
		}
	}
	return img
}

// inMark reports whether a point (0..1 coordinates) is on the white mark:
// a tilted telescope tube on a small tripod.
func inMark(x, y float64) bool {
	// tube: segment from (0.28,0.62) to (0.74,0.30), half-width 0.075
	ax, ay, bx, by := 0.28, 0.62, 0.74, 0.30
	vx, vy := bx-ax, by-ay
	t := ((x-ax)*vx + (y-ay)*vy) / (vx*vx + vy*vy)
	if t >= 0 && t <= 1 {
		cx, cy := ax+t*vx-x, ay+t*vy-y
		if cx*cx+cy*cy <= 0.075*0.075 {
			return true
		}
	}
	// tripod legs from the mount point (0.50,0.50)
	for _, leg := range [][2]float64{{0.36, 0.80}, {0.64, 0.80}} {
		lx, ly := leg[0]-0.50, leg[1]-0.50
		t := ((x-0.50)*lx + (y-0.50)*ly) / (lx*lx + ly*ly)
		if t >= 0 && t <= 1 {
			cx, cy := 0.50+t*lx-x, 0.50+t*ly-y
			if cx*cx+cy*cy <= 0.035*0.035 {
				return true
			}
		}
	}
	return false
}
