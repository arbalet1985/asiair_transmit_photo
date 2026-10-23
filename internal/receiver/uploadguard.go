package receiver

import (
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// An upload whose connection died without a word (the agent lost its
// network) would keep reading for minutes, until Windows gives the
// connection up, and hold the file meanwhile: the agent's next try waited
// for it. Now an upload that brings no data for putIdle is ended, and a new
// try of the same file ends the old one at once.

// putIdle: an upload bringing no data for this long is ended (tests shorten it)
var putIdle = time.Minute

// errTakenOver ends an upload replaced by a new try of the same file.
var errTakenOver = errors.New("агент начал передачу файла заново")

// activePut is an upload being received.
type activePut struct {
	mu      sync.Mutex
	rc      *http.ResponseController
	aborted bool
	done    bool
}

// abort ends the reading of the upload at once.
func (a *activePut) abort() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done || a.aborted {
		return
	}
	a.aborted = true
	a.rc.SetReadDeadline(time.Now())
}

func (a *activePut) finish() {
	a.mu.Lock()
	a.done = true
	a.mu.Unlock()
}

// reader reads the body, giving every read putIdle.
func (a *activePut) reader(body io.Reader) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		a.mu.Lock()
		if a.aborted {
			a.mu.Unlock()
			return 0, errTakenOver
		}
		a.rc.SetReadDeadline(time.Now().Add(putIdle))
		a.mu.Unlock()
		n, err := body.Read(p)
		if err != nil {
			a.mu.Lock()
			if a.aborted {
				err = errTakenOver
			}
			a.mu.Unlock()
		}
		return n, err
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// beginPut registers the upload of p, ending one still open for it.
func (s *Server) beginPut(p string, w http.ResponseWriter) *activePut {
	put := &activePut{rc: http.NewResponseController(w)}
	if old, ok := s.puts.Swap(p, put); ok {
		log.Printf("Приёмник: %s: прежняя передача ещё открыта — закрываю её, агент начал заново", p)
		old.(*activePut).abort()
	}
	return put
}

// endPut forgets the upload of p once its handler is done.
func (s *Server) endPut(p string, put *activePut) {
	put.finish()
	put.rc.SetReadDeadline(time.Time{})
	s.puts.CompareAndDelete(p, put)
}

// takeOver ends the upload of p still open: the agent asks for the file
// again, so it gave that one up.
func (s *Server) takeOver(p string) {
	if old, ok := s.puts.Load(p); ok {
		log.Printf("Приёмник: %s: прежняя передача ещё открыта — закрываю её, агент начал заново", p)
		old.(*activePut).abort()
	}
}
