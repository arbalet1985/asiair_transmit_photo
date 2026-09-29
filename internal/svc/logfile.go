package svc

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogPolicy says how long old days of the log are kept.
type LogPolicy struct {
	Keep          time.Duration // older days are deleted; 0 = never
	CompressAfter time.Duration // older days are compressed to .gz; 0 = at once
}

var (
	policyMu  sync.Mutex
	logPolicy = LogPolicy{Keep: 7 * 24 * time.Hour, CompressAfter: 3 * 24 * time.Hour}
)

// SetLogPolicy sets the retention of the logs opened by OpenLog, including
// ones already open (it applies at their next cleanup).
func SetLogPolicy(p LogPolicy) {
	policyMu.Lock()
	logPolicy = p
	policyMu.Unlock()
}

// SetLogRetention is SetLogPolicy from the settings of a program.
func SetLogRetention(keep, compressAfter time.Duration) {
	SetLogPolicy(LogPolicy{Keep: keep, CompressAfter: compressAfter})
}

func currentPolicy() LogPolicy {
	policyMu.Lock()
	defer policyMu.Unlock()
	return logPolicy
}

const (
	logMaxSize      = 50 << 20 // a day bigger than this continues in a new file
	cleanupInterval = 6 * time.Hour
	dayLayout       = "2006-01-02"
)

// RotatingLog is a log file that starts anew every day. The previous day
// is renamed to "<name>-YYYY-MM-DD.log"; older days are compressed and
// deleted according to the LogPolicy.
type RotatingLog struct {
	mu          sync.Mutex
	path        string
	f           *os.File
	day         string // day of the lines in f
	size        int64
	retryAt     time.Time // renaming failed (file busy): try again then
	nextCleanup time.Time
	// OnRotate is called with the new file after each rotation.
	OnRotate func(*os.File)

	cleaning sync.Mutex
	now      func() time.Time // tests
}

// OpenLog opens a log file for appending, rotating it by day.
func OpenLog(file string) (*RotatingLog, error) {
	return openLog(file, time.Now)
}

func openLog(file string, now func() time.Time) (*RotatingLog, error) {
	os.MkdirAll(filepath.Dir(file), 0o755)
	r := &RotatingLog{path: file, now: now}
	if err := r.open(); err != nil {
		return nil, err
	}
	t := now()
	r.day = t.Format(dayLayout)
	if st, err := r.f.Stat(); err == nil && st.Size() > 0 {
		r.day = st.ModTime().Format(dayLayout) // an earlier day: rotated at the first line
	}
	r.maybeCleanup(t)
	return r, nil
}

func (r *RotatingLog) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	r.size = 0
	if st, err := f.Stat(); err == nil {
		r.size = st.Size()
	}
	return nil
}

// File returns the file written now.
func (r *RotatingLog) File() *os.File {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f
}

func (r *RotatingLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	if (t.Format(dayLayout) != r.day || r.size+int64(len(p)) > logMaxSize && r.size > 0) && !t.Before(r.retryAt) {
		r.rotate(t)
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return len(p), nil // the log must never stop the program
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	r.maybeCleanup(t)
	return n, err
}

func (r *RotatingLog) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// rotate renames the current file after its day and starts a new one.
// Caller holds r.mu.
func (r *RotatingLog) rotate(t time.Time) {
	dst := r.archiveName(r.day)
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	err := os.Rename(r.path, dst)
	if oerr := r.open(); oerr != nil {
		r.retryAt = t.Add(time.Minute)
		return
	}
	if err != nil && !os.IsNotExist(err) {
		// e.g. another process has the file open on Windows: keep writing, try later
		r.retryAt = t.Add(time.Minute)
		fmt.Fprintf(r.f, "%s журнал: не удалось начать новый файл: %v\n", t.Format("2006/01/02 15:04:05"), err)
		return
	}
	r.day = t.Format(dayLayout)
	if r.OnRotate != nil {
		r.OnRotate(r.f)
	}
	r.nextCleanup = time.Time{} // compress and delete old days now
}

// archiveName returns a free name for the lines of the day:
// "<name>-2026-09-25.log", then "-2", "-3"… when the day was split.
func (r *RotatingLog) archiveName(day string) string {
	ext := filepath.Ext(r.path)
	base := strings.TrimSuffix(r.path, ext)
	for i := 1; ; i++ {
		name := base + "-" + day + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%s-%d%s", base, day, i, ext)
		}
		if !exists(name) && !exists(name+".gz") {
			return name
		}
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// maybeCleanup compresses and deletes old days in the background from time
// to time. Caller holds r.mu (or r is not shared yet).
func (r *RotatingLog) maybeCleanup(t time.Time) {
	if t.Before(r.nextCleanup) {
		return
	}
	r.nextCleanup = t.Add(cleanupInterval)
	go r.cleanup(t)
}

// cleanup runs cleanupLocked unless a cleanup is already running.
func (r *RotatingLog) cleanup(t time.Time) {
	if !r.cleaning.TryLock() {
		return
	}
	defer r.cleaning.Unlock()
	r.cleanupLocked(t)
}

// cleanupLocked compresses the days older than CompressAfter and deletes
// the ones older than Keep (by the time of their last line). Caller holds
// r.cleaning.
func (r *RotatingLog) cleanupLocked(t time.Time) {
	p := currentPolicy()
	dir := filepath.Dir(r.path)
	ext := filepath.Ext(r.path)
	name := strings.TrimSuffix(filepath.Base(r.path), ext)
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-\d{4}-\d{2}-\d{2}(-\d+)?` + regexp.QuoteMeta(ext) + `(\.gz)?$`)
	old := filepath.Base(r.path) + ".old" // rotated by older versions
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !re.MatchString(e.Name()) && e.Name() != old {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		age := t.Sub(info.ModTime())
		switch {
		case p.Keep > 0 && age > p.Keep:
			os.Remove(full)
		case strings.HasSuffix(e.Name(), ext) && age >= p.CompressAfter:
			if err := gzipFile(full, info.ModTime()); err != nil {
				log.Printf("Журнал: не удалось сжать %s: %v", e.Name(), err)
			}
		}
	}
}

// gzipFile replaces the file with "<file>.gz" keeping its time.
func gzipFile(file string, mtime time.Time) error {
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := file + ".gz.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	zw.Name = filepath.Base(file)
	zw.ModTime = mtime
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	in.Close()
	if err == nil {
		err = os.Rename(tmp, file+".gz")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(file+".gz", mtime, mtime)
	return os.Remove(file)
}

// LogFiles lists the log file and its rotated days (also compressed) changed
// after since, oldest first.
func LogFiles(file string, since time.Time) []string {
	dir, ext := filepath.Dir(file), filepath.Ext(file)
	base := strings.TrimSuffix(filepath.Base(file), ext)
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `(-\d{4}-\d{2}-\d{2}(-\d+)?)?` + regexp.QuoteMeta(ext) + `(\.gz|\.old)?$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type lf struct {
		path string
		t    time.Time
	}
	var files []lf
	for _, e := range entries {
		if e.IsDir() || !re.MatchString(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(since) {
			files = append(files, lf{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].t.Before(files[j].t) })
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out
}

// LogsSince is the start of the log days to take for the last days days
// (today counts as one), with a day to spare for the time zones.
func LogsSince(days int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -days)
}
