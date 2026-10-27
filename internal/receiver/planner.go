package receiver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The planner of the page: the sky of the observatory guessed from the
// Light frames received (the horizon, zones hidden by wires) and edited on
// the page, the targets of a night by priority, the plan and what the page
// needs to animate it. The planning itself is internal/planner, as in the
// sorter.

// PlannerState is what the page keeps of the planner.
type PlannerState struct {
	Sky     planner.Sky      `json:"sky"`
	SkySet  bool             `json:"sky_set,omitempty"` // edited on the page: the frames do not replace it
	Targets []planner.Target `json:"targets"`
	Params  *planner.Params  `json:"params,omitempty"`
	TZ      *float64         `json:"tz,omitempty"` // hours from UTC of the observatory clock
	// Optics: of the frames of the atlas (nil: the last setup of the frames)
	Optics *planner.Optics `json:"optics,omitempty"`
	// Favorites: the objects hearted on the page or in the sorter; changed
	// only by /ui/api/favorites, so that neither undoes the other
	Favorites []planner.Favorite `json:"favorites,omitempty"`
}

// skyEntry is where a Light frame was taken, read from its header.
type skyEntry struct {
	Object string  `json:"o,omitempty"`
	Night  string  `json:"n,omitempty"`
	UTC    int64   `json:"t"` // unix milliseconds
	Wall   int64   `json:"w"` // the clock of the observatory read as UTC, unix milliseconds
	Exp    float64 `json:"e,omitempty"`
	RA     float64 `json:"ra"`
	Dec    float64 `json:"dec"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	// the optics of the frame (for the atlas): the camera, FOCALLEN (mm),
	// XPIXSZ (µm) and the image (W×H); W = 0: read before they were kept
	Cam   string  `json:"c,omitempty"`
	Focal float64 `json:"f,omitempty"`
	Pix   float64 `json:"px,omitempty"`
	W     int     `json:"wd,omitempty"`
	H     int     `json:"ht,omitempty"`
}

// readSky reads the header of the frame at file (rel: its path here).
func readSky(file, rel string) (*skyEntry, bool) {
	f, err := sorter.ReadFrame(file, rel)
	if err != nil || !f.HasTarget || !f.HasSite || f.UTC.IsZero() {
		return nil, true
	}
	return &skyEntry{Object: f.Object, Night: f.Night, UTC: f.UTC.UnixMilli(), Wall: f.Time.UnixMilli(), Exp: f.Exposure,
		RA: f.RA, Dec: f.Dec, Lat: f.Lat, Lon: f.Lon, Cam: f.Camera, Focal: f.Focal, Pix: f.PixelUm, W: f.Width, H: f.Height}, false
}

func (s *State) Planner() PlannerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p.Planner
}

func (s *State) SetPlanner(p PlannerState) error {
	if p.TZ != nil && (*p.TZ < -12 || *p.TZ > 14) {
		return errors.New("часовой пояс: от −12 до +14")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Favorites = s.p.Planner.Favorites // changed by /ui/api/favorites only
	s.p.Planner = p
	return s.save()
}

// observations are the frames received with where they were taken.
func (s *Server) observations() []planner.Obs {
	sk := s.quality.skies()
	obs := make([]planner.Obs, 0, len(sk))
	for _, e := range sk {
		obs = append(obs, planner.Obs{Object: e.Object, Night: e.Night, UTC: time.UnixMilli(e.UTC).UTC(), Exposure: e.Exp,
			RA: e.RA, Dec: e.Dec, Lat: e.Lat, Lon: e.Lon})
	}
	return obs
}

// tzGuess: the clock of the observatory from the frames (the time in the
// name against DATE-OBS), else this computer's.
func tzGuess(sk []skyEntry) float64 {
	count := map[int64]int{}
	best, n := int64(0), 0
	for _, e := range sk {
		off := int64(math.Round(float64(e.Wall-e.UTC)/(15*60e3))) * 15
		count[off]++
		if count[off] > n {
			best, n = off, count[off]
		}
	}
	if n == 0 || best < -12*60 || best > 14*60 {
		_, o := time.Now().Zone()
		return float64(o) / 3600
	}
	return float64(best) / 60
}

type plannerObject struct {
	Name  string  `json:"name"`
	Label string  `json:"label,omitempty"`
	RA    float64 `json:"ra"`
	Dec   float64 `json:"dec"`
	Kind  string  `json:"kind,omitempty"`
	Size  float64 `json:"size,omitempty"` // arcminutes across
	// of the frames: the exposure of the last one, how many and their
	// exposure in all (seconds), the last night
	Exp    float64 `json:"exp,omitempty"`
	Frames int     `json:"frames,omitempty"`
	Total  float64 `json:"total,omitempty"`
	Night  string  `json:"night,omitempty"`
}

// frameObjects: the objects of the frames received, where they were last
// pointed at and how much was shot.
func frameObjects(sk []skyEntry) []plannerObject {
	idx := map[string]int{}
	var out []plannerObject
	sort.Slice(sk, func(i, j int) bool { return sk[i].UTC > sk[j].UTC }) // the last pointing
	for _, e := range sk {
		if e.Object == "" {
			continue
		}
		i, ok := idx[e.Object]
		if !ok {
			i = len(out)
			idx[e.Object] = i
			out = append(out, plannerObject{Name: e.Object, RA: e.RA, Dec: e.Dec, Kind: "кадры", Exp: e.Exp, Night: e.Night})
		}
		out[i].Frames++
		out[i].Total += e.Exp
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

const maxPoints = 4000 // frames shown on the sky of the page

type plannerGuess struct {
	Objects []string        `json:"objects"`
	Points  [][3]float64    `json:"points"` // azimuth, altitude, index in Objects
	Horizon planner.Horizon `json:"horizon"`
	Zones   []planner.Zone  `json:"zones"`
	Maybe   []planner.Zone  `json:"maybe"`
}

func (s *Server) handleUIPlanner(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var st PlannerState
		if !readJSON(w, r, &st) {
			return
		}
		if err := s.state.SetPlanner(st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	snap := s.state.Snapshot()
	st := snap.Planner
	if st.Params == nil {
		d := planner.DefaultParams()
		st.Params = &d
	}
	if st.Targets == nil {
		st.Targets = []planner.Target{}
	}
	sk := s.quality.skies()
	minAlt := st.Sky.MinAlt
	if minAlt == 0 && !st.SkySet {
		minAlt = 15
	}
	g := planner.Infer(s.observations(), minAlt)
	out := plannerGuess{Objects: []string{}, Points: [][3]float64{}, Horizon: g.Horizon, Zones: g.Zones, Maybe: g.Maybe}
	if out.Horizon == nil {
		out.Horizon = planner.Horizon{}
	}
	if out.Zones == nil {
		out.Zones = []planner.Zone{}
	}
	if out.Maybe == nil {
		out.Maybe = []planner.Zone{}
	}
	idx := map[string]int{}
	step := len(g.Points)/maxPoints + 1
	for i, o := range g.Points {
		if i%step != 0 {
			continue
		}
		k, ok := idx[o.Object]
		if !ok {
			k = len(out.Objects)
			idx[o.Object] = k
			out.Objects = append(out.Objects, o.Object)
		}
		out.Points = append(out.Points, [3]float64{round1(o.Az), round1(o.Alt), float64(k)})
	}
	if !st.SkySet {
		st.Sky = planner.Sky{Horizon: g.Horizon, Zones: g.Zones, MinAlt: minAlt}
	}
	if st.Sky.Horizon == nil {
		st.Sky.Horizon = planner.Horizon{}
	}
	if st.Sky.Zones == nil {
		st.Sky.Zones = []planner.Zone{}
	}
	writeJSON(w, map[string]any{
		"state":    st,
		"site":     snap.Site,
		"tz_guess": tzGuess(sk),
		"guess":    out,
		"objects":  frameObjects(sk),
		"frames":   len(sk),
		"setups":   frameSetups(sk),
	})
}

// frameSetups: the optics of the frames received, the last used first.
func frameSetups(sk []skyEntry) []planner.Setup {
	fr := make([]planner.SetupFrame, 0, len(sk))
	for _, e := range sk {
		fr = append(fr, planner.SetupFrame{Camera: e.Cam, Focal: e.Focal, Pixel: e.Pix, Width: e.W, Height: e.H, Time: time.UnixMilli(e.UTC)})
	}
	out := planner.SetupsOf(fr)
	if out == nil {
		out = []planner.Setup{}
	}
	return out
}

func tzName(h float64) string {
	m := int(math.Round(h * 60))
	sign := "+"
	if m < 0 {
		sign, m = "−", -m
	}
	if m%60 == 0 {
		return fmt.Sprintf("UTC%s%d", sign, m/60)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, m/60, m%60)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// handleUICatalog finds objects by name: the frames received, the catalog
// and, asked with resolve=1, SIMBAD.
func (s *Server) handleUICatalog(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []plannerObject{}
	if q == "" {
		writeJSON(w, out)
		return
	}
	if r.URL.Query().Get("resolve") != "" {
		o, err := catalog.Resolve(r.Context(), q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, []plannerObject{{Name: o.Name, Label: o.Label(), RA: o.RA, Dec: o.Dec, Kind: o.Kind}})
		return
	}
	nq := catalog.Normalize(q)
	for _, f := range frameObjects(s.quality.skies()) {
		if strings.Contains(catalog.Normalize(f.Name), nq) {
			out = append(out, f)
		}
	}
	seen := map[string]bool{}
	add := func(o catalog.Object) {
		if !seen[o.Name] {
			seen[o.Name] = true
			out = append(out, plannerObject{Name: o.Name, Label: o.Label(), RA: o.RA, Dec: o.Dec, Kind: o.Kind, Size: o.Size})
		}
	}
	if o, ok := catalog.Lookup(q); ok {
		add(o)
	}
	for _, o := range catalog.Search(q, 30) {
		add(o)
	}
	writeJSON(w, out)
}

// planStep is a step of the plan for the page.
type planStep struct {
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Start  int64   `json:"start"` // unix milliseconds
	End    int64   `json:"end"`
	Target int     `json:"target"`
	Frames int     `json:"frames,omitempty"`
	Alt1   float64 `json:"alt1"`
	Alt2   float64 `json:"alt2"`
	Moon   float64 `json:"moon"` // degrees from the Moon in the middle
	Note   string  `json:"note,omitempty"`
}

// sampleEvery: the page animates between these samples.
const sampleEvery = 2 * time.Minute

func (s *Server) handleUIPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Night string       `json:"night"`
		Start string       `json:"start,omitempty"` // "ЧЧ:ММ" on the clock of the observatory
		State PlannerState `json:"state"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	site := s.state.Snapshot().Site
	if !site.Set {
		http.Error(w, "задайте координаты обсерватории (блок «Координаты обсерватории» в настройках)", http.StatusBadRequest)
		return
	}
	prm := planner.DefaultParams()
	if req.State.Params != nil {
		prm = *req.State.Params
	}
	tz := tzGuess(s.quality.skies())
	if req.State.TZ != nil {
		tz = *req.State.TZ
	}
	prm.Night, prm.StartAt, prm.Lat, prm.Lon = req.Night, req.Start, site.Lat, site.Lon
	prm.Zone = time.FixedZone(tzName(tz), int(math.Round(tz*3600)))
	targets := req.State.Targets
	active := 0
	for _, t := range targets {
		if !t.Off && t.Exposure > 0 {
			active++
		}
	}
	var plan *planner.Plan
	var err error
	if active > 0 {
		plan, err = planner.Schedule(targets, req.State.Sky, prm)
	} else {
		plan = &planner.Plan{Targets: targets, Sky: req.State.Sky, Params: prm}
		plan.Dusk, plan.Dawn, err = planner.Night(prm.Night, prm.Lat, prm.Lon, prm.SunAlt)
		if err == nil {
			plan.Start, err = planner.Start(prm.Night, prm.StartAt, prm.Zone, plan.Dusk, plan.Dawn)
		}
		plan.Frames, plan.Seconds = make([]int, len(targets)), make([]float64, len(targets))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := r.Context().Err(); err != nil {
		return
	}
	writeJSON(w, planResponse(r.Context(), plan, active > 0))
}

func planResponse(ctx context.Context, p *planner.Plan, planned bool) map[string]any {
	lat, lon := p.Params.Lat, p.Params.Lon
	pos := func(q moon.Pos) [2]float64 { return [2]float64{round1(q.Az), round1(q.Alt)} }
	steps := []planStep{}
	for _, st := range p.Steps {
		ps := planStep{Kind: st.Kind, Label: planner.KindLabel[st.Kind], Start: st.Start.UnixMilli(), End: st.End.UnixMilli(),
			Target: st.Target, Frames: st.Frames, Note: st.Note}
		if st.Target >= 0 {
			a, b := p.Where(st.Target, st.Start), p.Where(st.Target, st.End)
			mid := st.Start.Add(st.End.Sub(st.Start) / 2)
			m, wp := moon.At(mid, lat, lon, true), p.Where(st.Target, mid)
			ps.Alt1, ps.Alt2 = round1(a.Alt), round1(b.Alt)
			ps.Moon = math.Round(moon.Separation(moon.Pos{Alt: wp.Alt, Az: wp.Az}, moon.Pos{Alt: m.Alt, Az: m.Az}))
		}
		steps = append(steps, ps)
	}
	// samples over the night for the animation
	var sun, mn [][2]float64
	var illum []float64
	tracks := make([][][2]float64, len(p.Targets))
	var scope [][4]float64 // azimuth, altitude, step, after the flip
	for t := p.Dusk; !t.After(p.Dawn); t = t.Add(sampleEvery) {
		if ctx.Err() != nil {
			break
		}
		sun = append(sun, pos(moon.Sun(t, lat, lon)))
		m := moon.At(t, lat, lon, true)
		mn = append(mn, pos(moon.Pos{Alt: m.Alt, Az: m.Az}))
		ill := m.Illum
		if !m.Waxing {
			ill = -ill // the sign tells the waning Moon
		}
		illum = append(illum, math.Round(ill*1000)/1000)
		for i, tg := range p.Targets {
			tracks[i] = append(tracks[i], pos(moon.Object(t, lat, lon, tg.RA, tg.Dec)))
		}
		if planned {
			st := p.At(t)
			west := 0.0
			if st.West {
				west = 1
			}
			scope = append(scope, [4]float64{round1(st.Point.Az), round1(st.Point.Alt), float64(st.Step), west})
		}
	}
	totals := make([]map[string]any, len(p.Targets))
	for i := range p.Targets {
		totals[i] = map[string]any{"frames": p.Frames[i], "seconds": p.Seconds[i], "time": planner.Hours(p.Seconds[i])}
	}
	// when each target crosses the meridian in the night (0: it does not)
	meridian := make([]int64, len(p.Targets))
	for i, tg := range p.Targets {
		if m, ok := planner.Meridian(tg, lon, p.Dusk, p.Dawn); ok {
			meridian[i] = m.UnixMilli()
		}
	}
	_, off := p.Dusk.In(p.Params.Zone).Zone()
	out := map[string]any{
		"dusk": p.Dusk.UnixMilli(), "dawn": p.Dawn.UnixMilli(), "start": p.Start.UnixMilli(), "tz": off, "every": sampleEvery.Milliseconds(),
		"sun": sun, "moon": mn, "illum": illum, "tracks": tracks, "steps": steps, "totals": totals, "meridian": meridian,
		"park": [2]float64{p.Park().Az, p.Park().Alt}, "planned": planned,
	}
	if planned {
		out["scope"], out["text"], out["csv"] = scope, p.Text(), p.CSV()
	}
	return out
}
