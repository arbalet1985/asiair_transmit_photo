package receiver

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

// a night that failed is asked again after this
const histRetry = 12 * time.Hour

// historyWanted: the nights of the frames with moving tracks that have no
// catalogue near their time, newest first.
func (s *Server) historyWanted() []satellites.HistoryNight {
	var starts []time.Time
	s.quality.mu.Lock()
	for _, e := range s.quality.m {
		if e.Start == 0 {
			continue
		}
		moving := false
		for _, t := range e.Tracks {
			moving = moving || !t.Static
		}
		if moving {
			starts = append(starts, time.UnixMilli(e.Start))
		}
	}
	s.quality.mu.Unlock()
	return s.sats.store.HistoryWanted(starts)
}

// fetchHistory fetches from Space-Track the orbits of the nights of the
// frames that have no catalogue near their time (taken before the
// receiver began to download the catalogues, or while it was off).
func (s *Server) fetchHistory(ctx context.Context) {
	st := s.sats
	st.mu.Lock()
	user, pass := st.stUser, st.stPass
	st.mu.Unlock()
	if user == "" || pass == "" {
		return
	}
	var todo []satellites.HistoryNight
	for _, n := range s.historyWanted() {
		st.mu.Lock()
		tried, ok := st.histTried[n.Key()]
		st.mu.Unlock()
		if ok && time.Since(tried) < histRetry {
			continue
		}
		todo = append(todo, n)
		if len(todo) == satellites.HistoryPerRound {
			break
		}
	}
	if len(todo) == 0 {
		return
	}
	sess := &satellites.SpaceTrack{User: user, Password: pass, UserAgent: "asiair-receiver/" + protocol.Version}
	lctx, cancel := context.WithTimeout(ctx, time.Minute)
	err := sess.Login(lctx)
	cancel()
	if err != nil {
		st.setHistErr(err.Error())
		log.Printf("Приёмник: %v", err)
		return
	}
	defer sess.Logout(context.Background())
	for i, n := range todo {
		if ctx.Err() != nil {
			return
		}
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		st.mu.Lock()
		st.histTried[n.Key()] = time.Now()
		st.mu.Unlock()
		cnt, err := st.store.FetchHistory(ctx, sess, n)
		if err != nil {
			st.setHistErr(err.Error())
			log.Printf("Приёмник: орбиты за %s с Space-Track не скачаны: %v", n.Target().Local().Format("02.01.2006"), err)
			return
		}
		st.setHistErr("")
		log.Printf("Приёмник: скачаны орбиты за %s с Space-Track: %d объектов (кадров с треками: %d)",
			n.Target().Local().Format("02.01.2006 15:04"), cnt, len(n.Starts))
	}
}

func (st *satState) setHistErr(e string) {
	st.mu.Lock()
	st.histErr = e
	st.mu.Unlock()
}

// handleUISpaceTrack saves the login of Space-Track (only on the computer
// of the receiver: the password is not sent over remote access).
func (s *Server) handleUISpaceTrack(w http.ResponseWriter, r *http.Request) {
	if isRemote(r) {
		http.Error(w, "доступно только на компьютере с приёмником", http.StatusForbidden)
		return
	}
	var req struct {
		User     string  `json:"user"`
		Password *string `json:"password"` // nil: kept
	}
	if !readJSON(w, r, &req) {
		return
	}
	user := strings.TrimSpace(req.User)
	values := map[string]string{"spacetrack_user": user}
	if req.Password != nil {
		values["spacetrack_password"] = *req.Password
	}
	if user == "" {
		values["spacetrack_password"] = ""
	}
	if s.cfg.Path != "" {
		if err := config.UpdateReceiverFile(s.cfg.Path, values); err != nil {
			http.Error(w, "не удалось сохранить настройки: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	st := s.sats
	st.mu.Lock()
	st.stUser = user
	if p, ok := values["spacetrack_password"]; ok {
		st.stPass = p
	}
	st.histErr = ""
	st.histTried = map[string]time.Time{}
	set := st.stUser != "" && st.stPass != ""
	st.mu.Unlock()
	if set {
		log.Printf("Приёмник: задан вход в Space-Track (%s) — архив орбит для старых кадров", user)
		st.poke()
	} else {
		log.Printf("Приёмник: вход в Space-Track удалён")
	}
	writeJSON(w, s.satInfo())
}
