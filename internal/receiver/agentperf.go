package receiver

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// The perf log of the agent (protocol.PerfLogName) comes in its archive of
// logs; the page draws it. The days of it are rotated like the other logs.
var perfLogName = regexp.MustCompile(`^` + regexp.QuoteMeta(strings.TrimSuffix(protocol.PerfLogName, ".log")) +
	`(-\d{4}-\d{2}-\d{2}(-\d+)?)?\.log(\.gz)?$`)

// perfMaxFiles: the records of files sent at most, the last ones (a few
// days of frames every few seconds are too many for a phone).
const perfMaxFiles = 3000

type perfAnswer struct {
	Minutes []protocol.PerfRecord `json:"minutes"`
	Files   []protocol.PerfRecord `json:"files"`
	More    int                   `json:"more_files,omitempty"` // older file records left out
}

// readPerf reads the perf records of an archive of agent logs.
func readPerf(zr *zip.Reader) (perfAnswer, error) {
	var out perfAnswer
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if !perfLogName.MatchString(name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return out, err
		}
		var r io.Reader = rc
		if strings.HasSuffix(name, ".gz") {
			gz, err := gzip.NewReader(rc)
			if err != nil {
				rc.Close()
				continue // a broken day does not hide the others
			}
			r = gz
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue // comments
			}
			var rec protocol.PerfRecord
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			switch rec.Kind {
			case "minute":
				out.Minutes = append(out.Minutes, rec)
			case "file":
				out.Files = append(out.Files, rec)
			}
		}
		rc.Close()
	}
	byTime := func(rs []protocol.PerfRecord) {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
	}
	byTime(out.Minutes)
	byTime(out.Files)
	if n := len(out.Files); n > perfMaxFiles {
		out.More = n - perfMaxFiles
		out.Files = out.Files[n-perfMaxFiles:]
	}
	if out.Minutes == nil {
		out.Minutes = []protocol.PerfRecord{}
	}
	if out.Files == nil {
		out.Files = []protocol.PerfRecord{}
	}
	return out, nil
}

// handleUIAgentPerf: GET ?name=<agent archive> returns its perf records.
func (s *Server) handleUIAgentPerf(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !agentLogsName.MatchString(name) {
		http.Error(w, "неверное имя", http.StatusBadRequest)
		return
	}
	zr, err := zip.OpenReader(filepath.Join(s.agentLogsDir(), name))
	if err != nil {
		http.Error(w, "нет такого архива", http.StatusNotFound)
		return
	}
	defer zr.Close()
	out, err := readPerf(&zr.Reader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
