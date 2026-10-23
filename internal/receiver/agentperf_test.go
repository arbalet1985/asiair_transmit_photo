package receiver

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestReadPerf(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, b []byte) {
		w, _ := zw.Create(name)
		w.Write(b)
	}
	var gz bytes.Buffer
	g := gzip.NewWriter(&gz)
	g.Write([]byte(`{"t":"2026-10-20T01:00:00Z","k":"minute","bytes":100,"acts":{"net":{"ms":60000,"b":100}}}` + "\n"))
	g.Close()
	add("asiair-agent-perf-2026-10-20.log.gz", gz.Bytes())
	add("asiair-agent-perf.log", []byte("# comment\n"+
		`{"t":"2026-10-23T01:00:05Z","k":"file","path":"a.fit","bytes":5,"ms":10,"acts":{"read":{"ms":3,"b":5}}}`+"\n"+
		"broken\n"+
		`{"t":"2026-10-23T01:01:00Z","k":"minute","bytes":5,"cpu":12.5}`+"\n"))
	add("asiair-agent.log", []byte(`{"t":"2026-10-23T01:00:00Z","k":"minute"}`+"\n")) // not the perf log
	zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	out, err := readPerf(zr)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Minutes) != 2 || out.Minutes[0].Acts["net"].Bytes != 100 || out.Minutes[1].CPU != 12.5 {
		t.Fatalf("minutes: %+v", out.Minutes)
	}
	if len(out.Files) != 1 || out.Files[0].Path != "a.fit" || out.Files[0].Acts["read"].MS != 3 {
		t.Fatalf("files: %+v", out.Files)
	}
}
