package atlas

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
)

// State is what the atlas page of the sorter shows: the targets of the
// planner with their colours, the one chosen and the optics.
type State struct {
	Rev     uint64           `json:"rev"`
	Targets []planner.Target `json:"targets"`
	Colors  []string         `json:"colors"`
	Sel     int              `json:"sel"`
	Optics  *planner.Optics  `json:"optics"`
	Setups  []planner.Setup  `json:"setups"`
	// Photo: a frame laid over the sky (nil: none), its picture at
	// /photo.jpg (SetPhoto)
	Photo *Photo `json:"photo,omitempty"`
	// FrameReq: start the framing of target FrameI (once per value)
	FrameReq int64 `json:"frame_req,omitempty"`
	FrameI   int   `json:"frame_i"`
	// for the sky map: the observatory (TZ: hours of its clock), the sky of
	// the planner and the favorite objects
	Lat       float64            `json:"lat"`
	Lon       float64            `json:"lon"`
	SiteSet   bool               `json:"site_set,omitempty"`
	TZ        float64            `json:"tz"`
	Sky       *planner.Sky       `json:"sky,omitempty"`
	Favorites []planner.Favorite `json:"favorites"`
}

// Photo describes the frame over the sky: V changes with its picture.
type Photo struct {
	V      int64          `json:"v"`
	Name   string         `json:"name"`
	WCS    map[string]any `json:"wcs"`
	Width  float64        `json:"width"` // degrees
	Height float64        `json:"height"`
}

// Server serves the atlas page to the browser of this computer only
// (127.0.0.1, a random token in the link) and hands what is done there to
// the program. The callbacks are called from the request goroutines.
type Server struct {
	OnFrame  func(i int, ra, dec, angle float64) // the framing of target i applied
	OnOptics func(o planner.Optics)              // the optics changed
	OnSelect func(i int)                         // target i chosen
	// on the sky map: an object hearted or not, an object to the plan
	OnFavorite func(f planner.Favorite, on bool)
	OnAdd      func(f planner.Favorite)

	token string
	mu    sync.Mutex
	st    State
	wake  chan struct{} // closed when the state changes
	url   string
	photo []byte    // the picture of State.Photo
	seen  time.Time // the last request of the page
}

// Watched: a page of the atlas asked for the state lately (it is open).
func (s *Server) Watched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.seen) < 40*time.Second
}

// SetPhoto keeps the picture served at /photo.jpg.
func (s *Server) SetPhoto(jpg []byte) {
	s.mu.Lock()
	s.photo = jpg
	s.mu.Unlock()
}

// pollWait: how long a request for the state waits for a change.
var pollWait = 25 * time.Second

func NewServer() *Server {
	b := make([]byte, 16)
	rand.Read(b)
	return &Server{token: hex.EncodeToString(b), wake: make(chan struct{}), st: State{Sel: -1}}
}

// Publish gives the page a new state.
func (s *Server) Publish(st State) {
	s.mu.Lock()
	st.Rev = s.st.Rev + 1
	if st.Targets == nil {
		st.Targets = []planner.Target{}
	}
	if st.Setups == nil {
		st.Setups = []planner.Setup{}
	}
	if st.Favorites == nil {
		st.Favorites = []planner.Favorite{}
	}
	s.st = st
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

// Start listens on a free port of 127.0.0.1 (once) and returns the link of
// the page.
func (s *Server) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.url != "" {
		return s.url, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("Атлас: сбой: %v", p)
			}
		}()
		srv.Serve(ln)
	}()
	s.url = fmt.Sprintf("http://%s/?t=%s", ln.Addr(), s.token)
	return s.url, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("Атлас: сбой запроса %s: %v", r.URL.Path, p)
			http.Error(w, "сбой", http.StatusInternalServerError)
		}
	}()
	public := r.URL.Path == "/atlas.js" || r.URL.Path == "/skymap.js" || r.URL.Path == "/skydata.js" || r.URL.Path == "/skystars8.js" // no secret in them
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.token)) != 1 && !public {
		http.Error(w, "неверная ссылка", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		Static(w, "atlas.html")
	case "/atlas.js", "/skymap.js", "/skydata.js", "/skystars8.js":
		Static(w, r.URL.Path[1:], r)
	case "/objects":
		Objects(w)
	case "/find":
		Find(w, r.URL.Query().Get("q"))
	case "/state":
		s.handleState(w, r)
	case "/photo.jpg":
		s.mu.Lock()
		jpg := s.photo
		s.mu.Unlock()
		if jpg == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpg)
	case "/frame", "/optics", "/select", "/favorite", "/add":
		if r.Method != http.MethodPost {
			http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
			return
		}
		s.handlePost(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleState answers with the state newer than rev, waiting for it a while.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	rev, _ := strconv.ParseUint(r.URL.Query().Get("rev"), 10, 64)
	s.mu.Lock()
	st, wake := s.st, s.wake
	s.seen = time.Now()
	s.mu.Unlock()
	if st.Rev == rev {
		select {
		case <-wake:
		case <-time.After(pollWait):
		case <-r.Context().Done():
			return
		}
		s.mu.Lock()
		st = s.st
		s.seen = time.Now()
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(st)
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	var err error
	switch r.URL.Path {
	case "/frame":
		var v struct {
			I              int
			RA, Dec, Angle float64
		}
		if err = dec.Decode(&v); err == nil && s.OnFrame != nil {
			s.OnFrame(v.I, v.RA, v.Dec, v.Angle)
		}
	case "/optics":
		var o planner.Optics
		if err = dec.Decode(&o); err == nil && s.OnOptics != nil {
			s.OnOptics(o)
		}
	case "/select":
		var v struct{ I int }
		if err = dec.Decode(&v); err == nil && s.OnSelect != nil {
			s.OnSelect(v.I)
		}
	case "/favorite":
		var v struct {
			Fav planner.Favorite
			On  bool
		}
		if err = dec.Decode(&v); err == nil && v.Fav.Name != "" && s.OnFavorite != nil {
			s.OnFavorite(v.Fav, v.On)
		}
	case "/add":
		var f planner.Favorite
		if err = dec.Decode(&f); err == nil && f.Name != "" && s.OnAdd != nil {
			s.OnAdd(f)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
