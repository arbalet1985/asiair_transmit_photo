package receiver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// stalledPut starts an upload of size bytes that sends first bytes and
// then nothing until release is closed; the answer goes to done.
func stalledPut(t *testing.T, base, token string, q url.Values, size int64, first int, release chan struct{}) chan *http.Response {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		pw.Write(make([]byte, first))
		<-release
		pw.CloseWithError(io.ErrUnexpectedEOF)
	}()
	q = url.Values{"path": q["path"], "size": q["size"], "mtime": q["mtime"], "offset": {"0"}}
	req, _ := http.NewRequest(http.MethodPut, base+"/api/upload?"+q.Encode(), pr)
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- resp
	}()
	return done
}

func headOffset(t *testing.T, base, token string, q url.Values) int64 {
	t.Helper()
	req, _ := http.NewRequest(http.MethodHead, base+"/api/upload?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	n, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	return n
}

// TestUploadIdle: an upload that stops bringing data is ended after
// putIdle, keeping what came, and does not hold the file.
func TestUploadIdle(t *testing.T) {
	putIdle = 500 * time.Millisecond
	defer func() { putIdle = time.Minute }()
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.agentPortHandler("internet"))
	defer ts.Close()
	token := s.getToken()
	q := url.Values{"path": {"EMMC Images/Light/a.fit"}, "size": {"100000"}, "mtime": {"1"}}

	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	done := stalledPut(t, ts.URL, token, q, 100000, 1000, release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled upload is not ended")
	}
	if d := time.Since(start); d < putIdle {
		t.Fatalf("ended after %v, before putIdle", d)
	}
	if off := headOffset(t, ts.URL, token, q); off != 1000 {
		t.Fatalf("offset %d, want 1000", off)
	}
}

// TestUploadTakeover: a new try of the same file ends the upload still
// open at once instead of waiting for it.
func TestUploadTakeover(t *testing.T) {
	s, _ := newTestServer(t)
	ts := httptest.NewServer(s.agentPortHandler("internet"))
	defer ts.Close()
	token := s.getToken()
	q := url.Values{"path": {"EMMC Images/Light/b.fit"}, "size": {"100000"}, "mtime": {"1"}}

	release := make(chan struct{})
	defer close(release)
	done := stalledPut(t, ts.URL, token, q, 100000, 3000, release)
	// wait for the data to reach the file
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	off := headOffset(t, ts.URL, token, q)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the new try waited %v for the old upload", d)
	}
	if off != 3000 {
		t.Fatalf("offset %d, want 3000", off)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the old upload is still open")
	}
}
