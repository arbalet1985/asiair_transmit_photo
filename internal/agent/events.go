package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/svc"
)

// Events are the important steps of the agent (an update, a restart,
// deleting files, a new ASIAIR address, a crash). Each is sent to the
// receiver with the next sync (which starts at once) before the step is
// taken, so the receiver page tells what the agent was doing if it stops
// answering.

const maxEvents = 200 // unacknowledged events kept (old receivers never acknowledge)

type eventLog struct {
	mu    sync.Mutex
	seq   uint64
	acked uint64
	list  []protocol.AgentEvent
}

// event records an important step: in the log, and for the receiver.
// level is "", "warn" or "error".
func (a *Agent) event(level, format string, args ...any) uint64 {
	text := fmt.Sprintf(format, args...)
	log.Printf("Агент: %s", text)
	e := &a.events
	e.mu.Lock()
	e.seq++
	seq := e.seq
	e.list = append(e.list, protocol.AgentEvent{Seq: seq, Time: time.Now(), Text: text, Level: level})
	if len(e.list) > maxEvents {
		e.list = e.list[len(e.list)-maxEvents:]
	}
	e.mu.Unlock()
	a.kickSync()
	return seq
}

// eventWait records an event and waits (at most d) until the receiver has
// it: for steps after which the agent may not come back, like a restart.
func (a *Agent) eventWait(d time.Duration, level, format string, args ...any) {
	seq := a.event(level, format, args...)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		a.events.mu.Lock()
		done := a.events.acked >= seq
		a.events.mu.Unlock()
		if done {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pendingEvents are the events to send with the next sync.
func (a *Agent) pendingEvents() []protocol.AgentEvent {
	e := &a.events
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.list) == 0 {
		return nil
	}
	return append([]protocol.AgentEvent(nil), e.list...)
}

// ackEvents drops the events the receiver has stored.
func (a *Agent) ackEvents(seq uint64) {
	e := &a.events
	e.mu.Lock()
	defer e.mu.Unlock()
	if seq <= e.acked {
		return
	}
	e.acked = seq
	i := 0
	for i < len(e.list) && e.list[i].Seq <= seq {
		i++
	}
	e.list = e.list[i:]
}

// guard runs fn again and again until ctx ends: a panic in one of the
// agent's loops is logged and reported, and the loop starts over instead of
// taking the whole agent down.
func (a *Agent) guard(ctx context.Context, name string, fn func(context.Context)) {
	for ctx.Err() == nil {
		crashed := func() (crashed bool) {
			defer func() {
				if v := recover(); v != nil {
					crashed = true
					log.Printf("Агент: сбой в «%s»: %v\n%s", name, v, debug.Stack())
					a.event("error", "сбой в «%s»: %v — перезапуск через 5 с", name, v)
				}
			}()
			fn(ctx)
			return false
		}()
		if !crashed {
			return
		}
		sleep(ctx, 5*time.Second)
	}
}

// reportCrash makes crashes leave a report (svc.CatchCrashes) and tells the
// receiver how the previous run ended when it crashed.
func (a *Agent) reportCrash() {
	if a.App == "" {
		return
	}
	svc.CatchCrashes(a.App)
	if code, ok := svc.GuardianRestart(); ok {
		a.event("error", "агент перезапущен после аварийного завершения (код %d)", code)
	}
	file := svc.PrevCrashFile(a.App)
	b, err := os.ReadFile(file)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return
	}
	text := strings.TrimSpace(string(b))
	log.Printf("Агент: предыдущий запуск завершился сбоем:\n%s", text)
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	if len(first) > 300 {
		first = first[:300]
	}
	a.event("error", "предыдущий запуск агента завершился сбоем: %s", first)
	// reported: keep it for reading, but never report it again
	os.Remove(file + ".reported")
	os.Rename(file, file+".reported")
}
