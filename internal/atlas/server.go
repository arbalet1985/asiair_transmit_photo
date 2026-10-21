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
}

// Server serves the atlas page to the browser of this computer only
// (127.0.0.1, a random token in the link) and hands what is done there to
// the program. The callbacks are called from the request goroutines.
type Server struct {
	OnFrame  func(i int, ra, dec, angle float64) // the framing of target i applied
	OnOptics func(o planner.Optics)              // the optics changed
	OnSelect func(i int)                         // target i chosen

	token string
	mu    sync.Mutex
	st    State
	wake  chan struct{} // closed when the state changes
	url   string
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
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.token)) != 1 && r.URL.Path != "/atlas.js" {
		http.Error(w, "неверная ссылка", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		Static(w, "atlas.html")
	case "/atlas.js":
		Static(w, "atlas.js")
	case "/state":
		s.handleState(w, r)
	case "/frame", "/optics", "/select":
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
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
