package receiver

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

func at(h, m int) time.Time { return time.Date(2026, 9, 27, h, m, 0, 0, time.Local) }

func TestScheduleActive(t *testing.T) {
	night := ScheduleSettings{Enabled: true, From: "22:00", To: "06:30"}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(21, 59), false}, {at(22, 0), true}, {at(2, 0), true}, {at(6, 29), true}, {at(6, 30), false}, {at(12, 0), false}} {
		if got := night.Active(c.t); got != c.want {
			t.Errorf("%s: %v", c.t.Format("15:04"), got)
		}
	}
	day := ScheduleSettings{Enabled: true, From: "09:00", To: "18:00"}
	if !day.Active(at(12, 0)) || day.Active(at(20, 0)) {
		t.Fatal("day window")
	}
	night.Enabled = false
	if night.Active(at(23, 0)) {
		t.Fatal("disabled window active")
	}
}

func TestApplySchedule(t *testing.T) {
	s := &State{}
	s.p.Schedule = ScheduleSettings{Enabled: true, From: "22:00", To: "06:00", Rate: 2 << 20}
	var resp protocol.SyncResponse
	s.applySchedule(&resp, at(23, 0))
	if resp.MaxRate == nil || *resp.MaxRate != 2<<20 || resp.Paused {
		t.Fatalf("night limit: %+v", resp)
	}
	low := int64(1 << 20)
	resp = protocol.SyncResponse{MaxRate: &low}
	s.applySchedule(&resp, at(23, 0))
	if *resp.MaxRate != 1<<20 {
		t.Fatal("the lower page limit must stay")
	}
	resp = protocol.SyncResponse{}
	s.applySchedule(&resp, at(12, 0))
	if resp.MaxRate != nil || resp.Paused {
		t.Fatal("applied outside the window")
	}
	s.p.Schedule.Rate = 0
	s.applySchedule(&resp, at(1, 0))
	if !resp.Paused {
		t.Fatal("rate 0 must pause")
	}
}

func TestCleanupCandidates(t *testing.T) {
	now := at(12, 0)
	old, fresh := now.Add(-10*24*time.Hour), now.Add(-time.Hour)
	inv := []protocol.RemoteFile{
		{Path: "EMMC Images/a.fit", Size: 10, ModTime: old},   // received, old: delete
		{Path: "EMMC Images/b.fit", Size: 10, ModTime: fresh}, // too new
		{Path: "EMMC Images/c.fit", Size: 10, ModTime: old},   // not received
		{Path: "EMMC Images/d.fit", Size: 10, ModTime: old},   // size differs
		{Path: "EMMC Images/x/e.fit", Size: 10, ModTime: old}, // already being deleted (folder)
		{Path: "EMMC Images/f.fit", Size: 10, ModTime: old},   // already being deleted
	}
	local := []LocalFile{{Path: "EMMC Images/a.fit", Size: 10}, {Path: "EMMC Images/b.fit", Size: 10},
		{Path: "EMMC Images/d.fit", Size: 9}, {Path: "EMMC Images/x/e.fit", Size: 10}, {Path: "EMMC Images/f.fit", Size: 10}}
	cmds := []*Command{
		{Command: protocol.Command{Type: protocol.CmdDeleteDir, Path: "EMMC Images/x"}, Status: StatusPending},
		{Command: protocol.Command{Type: protocol.CmdDelete, Path: "EMMC Images/f.fit"}, Status: StatusPending},
	}
	got := cleanupCandidates(local, inv, cmds, 7, now)
	if len(got) != 1 || got[0] != "EMMC Images/a.fit" {
		t.Fatalf("%v", got)
	}
}

func TestSpeedLog(t *testing.T) {
	var l speedLog
	t0 := at(22, 0)
	l.add(protocol.AgentStatus{Speed: 100, Uploading: "a"}, t0)
	l.add(protocol.AgentStatus{Speed: 300, Uploading: "a", Diag: &protocol.Diag{Bottleneck: "network"}}, t0.Add(30*time.Second))
	l.add(protocol.AgentStatus{Speed: 0}, t0.Add(time.Minute))
	s := l.list(t0)
	if len(s) != 2 || s[0].Speed != 200 || s[0].Max != 300 || !s[0].Active || s[0].Bottleneck != "network" || s[1].Active {
		t.Fatalf("%+v", s)
	}
	for i := 0; i < speedLogLen+10; i++ {
		l.add(protocol.AgentStatus{}, t0.Add(time.Duration(i+2)*time.Minute))
	}
	if n := len(l.list(time.Time{})); n != speedLogLen {
		t.Fatalf("kept %d", n)
	}
}

func TestParseAngle(t *testing.T) {
	for in, want := range map[string]float64{"55.75": 55.75, "'55.75'": 55.75, "55 45 00": 55.75, "-37:36:00": -37.6, "+55d45m00s": 55.75} {
		if got, ok := parseAngle(in); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseAngle("north"); ok {
		t.Error("parsed junk")
	}
}

// The schedule window is in the observatory's time, as the agent reports it.
func TestScheduleObservatoryZone(t *testing.T) {
	msk := 3 * 3600
	p := persisted{Schedule: ScheduleSettings{Enabled: true, From: "22:00", To: "06:00", Rate: 0}}
	p.Status.TZOffset, p.Status.TZName = &msk, "MSK"
	utc20 := time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC) // 23:00 at the observatory
	if !scheduleActive(&p, utc20) {
		t.Fatal("23:00 MSK must be inside 22:00–06:00")
	}
	utc4 := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC) // 07:00 at the observatory
	if scheduleActive(&p, utc4) {
		t.Fatal("07:00 MSK must be outside")
	}
	if z := newObsTZ(p.Status); !z.Known || z.Offset != msk || z.Text != "UTC+3 (MSK)" {
		t.Fatalf("%+v", z)
	}
	ny := -4*3600 - 30*60
	p.Status.TZOffset, p.Status.TZName = &ny, "-0430"
	if z := newObsTZ(p.Status); z.Text != "UTC-4:30" || z.Offset != ny {
		t.Fatalf("%+v", z)
	}
}

// The speed history is saved and read back; minutes older than 7 days go.
func TestSpeedLogSaved(t *testing.T) {
	file := t.TempDir() + "/speed-log.json"
	now := time.Now().Truncate(time.Minute)
	var l speedLog
	l.load(file, now) // no file yet
	l.add(protocol.AgentStatus{Speed: 1000, Uploading: "a", Streams: 3}, now.Add(-8*24*time.Hour))
	l.add(protocol.AgentStatus{Speed: 2000, Uploading: "a", Streams: 3}, now.Add(-time.Hour))
	l.add(protocol.AgentStatus{Speed: 3000, Uploading: "a"}, now.Add(-time.Minute))
	l.save()
	var back speedLog
	back.load(file, now)
	got := back.list(time.Time{})
	if len(got) != 2 || got[0].Speed != 2000 || !got[0].Active || got[0].Streams != 3 || got[1].Speed != 3000 {
		t.Fatalf("%+v", got)
	}
	back.add(protocol.AgentStatus{Speed: 5}, now)
	back.save()
	if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), `"speed":5`) {
		t.Fatalf("not saved again: %v", err)
	}
}
