package receiver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
)

const (
	// a frame wants the orbits of its night from Space-Track when no
	// catalogue kept is nearer its time than this
	histGap = 24 * time.Hour
	// a night is one piece of the history: the frames within this of the first
	histNight = 18 * time.Hour
	// pieces fetched in one round (Space-Track allows 30 queries a minute,
	// 300 an hour; each piece is fetched once and kept)
	histPerRound = 6
	// a night that failed is asked again after this
	histRetry = 12 * time.Hour
)

// histNightOf: the frames of one night with their tracks not told well.
type histNightOf struct {
	first, last time.Time
	starts      []time.Time
}

// target: the time the orbits are wanted for (the middle frame).
func (n histNightOf) target() time.Time { return n.starts[len(n.starts)/2] }

// historyWanted: the nights of the frames with tracks that have no
// catalogue near their time, newest first.
func (s *Server) historyWanted() []histNightOf {
	st := s.sats
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
		if !moving {
			continue
		}
		t := time.UnixMilli(e.Start)
		if c, ok := st.store.Nearest(t); ok && absDur(c.Time.Sub(t)) <= histGap {
			continue
		}
		starts = append(starts, t)
	}
	s.quality.mu.Unlock()
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	var out []histNightOf
	for _, t := range starts {
		if n := len(out); n > 0 && t.Sub(out[n-1].first) <= histNight {
			out[n-1].last = t
			out[n-1].starts = append(out[n-1].starts, t)
			continue
		}
		out = append(out, histNightOf{first: t, last: t, starts: []time.Time{t}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].first.After(out[j].first) })
	return out
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
	var todo []histNightOf
	for _, n := range s.historyWanted() {
		key := n.first.UTC().Format("2006-01-02T15")
		st.mu.Lock()
		tried, ok := st.histTried[key]
		st.mu.Unlock()
		if ok && time.Since(tried) < histRetry {
			continue
		}
		todo = append(todo, n)
		if len(todo) == histPerRound {
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
		log.Printf("Приёмник: Space-Track: %v", err)
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
		key := n.first.UTC().Format("2006-01-02T15")
		st.mu.Lock()
		st.histTried[key] = time.Now()
		st.mu.Unlock()
		// the elements published a few days before the night (debris is
		// observed seldom) and the day after
		from, to := n.first.Add(-3*24*time.Hour), n.last.Add(24*time.Hour)
		qctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		text, cnt, err := sess.History(qctx, from, to, n.target())
		cancel()
		if err == nil && cnt < 1000 {
			err = errHist(cnt)
		}
		if err == nil {
			_, err = st.store.SaveHistory(text, n.target())
		}
		if err != nil {
			st.setHistErr(err.Error())
			log.Printf("Приёмник: орбиты за %s с Space-Track не скачаны: %v", n.target().Local().Format("02.01.2006"), err)
			return
		}
		st.setHistErr("")
		log.Printf("Приёмник: скачаны орбиты за %s с Space-Track: %d объектов (кадров с треками: %d)",
			n.target().Local().Format("02.01.2006 15:04"), cnt, len(n.starts))
	}
}

type errHist int

func (e errHist) Error() string {
	return fmt.Sprintf("в архиве всего %d объектов", int(e))
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
