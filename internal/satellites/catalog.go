package satellites

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CelesTrak gives the orbits of the whole public catalogue (rocket bodies
// and debris too, the inactive satellites) only by name: these queries
// together cover it. Each one may be fetched once in two hours.
var catalogQueries = []string{
	"https://celestrak.org/NORAD/elements/gp.php?NAME=%20&FORMAT=tle",
	"https://celestrak.org/NORAD/elements/gp.php?NAME=-&FORMAT=tle",
	"https://celestrak.org/NORAD/elements/gp.php?GROUP=active&FORMAT=tle",
}

// Download fetches the catalogue: TLE text, each object once with its
// newest elements.
func Download(ctx context.Context, client *http.Client, userAgent string) ([]byte, int, error) {
	var parts [][]byte
	for _, u := range catalogQueries {
		b, err := fetch(ctx, client, u, userAgent)
		if err != nil {
			return nil, 0, err
		}
		parts = append(parts, b)
	}
	text, n := Merge(parts...)
	if n < 1000 {
		return nil, 0, fmt.Errorf("в каталоге всего %d объектов", n)
	}
	return text, n, nil
}

func fetch(ctx context.Context, client *http.Client, u, ua string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CelesTrak: %s", resp.Status)
	}
	if bytes.HasPrefix(b, []byte("GP data has not updated")) {
		return nil, errors.New("CelesTrak: каталог уже скачан в последние 2 часа")
	}
	return b, nil
}

// Merge joins TLE texts: each object once, with its newest elements.
func Merge(texts ...[]byte) ([]byte, int) {
	type entry struct {
		lines [3]string
		epoch string // yyddd.dddddddd: compares as text within a century
	}
	m := map[string]entry{}
	for _, t := range texts {
		lines := strings.Split(strings.ReplaceAll(string(t), "\r", ""), "\n")
		for i := 0; i+2 < len(lines); i++ {
			l1, l2 := lines[i+1], lines[i+2]
			if !strings.HasPrefix(l1, "1 ") || !strings.HasPrefix(l2, "2 ") || len(l1) < 69 || len(l2) < 69 {
				continue
			}
			id := strings.TrimSpace(l1[2:7])
			ep := l1[18:32]
			if yy := ep[:2]; yy < "57" {
				ep = "1" + ep // 20xx after 19xx
			} else {
				ep = "0" + ep
			}
			if old, ok := m[id]; !ok || ep > old.epoch {
				m[id] = entry{[3]string{strings.TrimSpace(lines[i]), l1, l2}, ep}
			}
			i += 2
		}
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b bytes.Buffer
	for _, id := range ids {
		e := m[id]
		b.WriteString(e.lines[0] + "\n" + e.lines[1] + "\n" + e.lines[2] + "\n")
	}
	return b.Bytes(), len(m)
}

// Store keeps the catalogues downloaded, one file per download, named by
// its time: catalog-20261012-1530.tle.gz.
type Store struct {
	Dir  string
	Keep int // newest files kept
}

const fileLayout = "20060102-1504"

// Save writes a catalogue downloaded at t.
func (s Store) Save(text []byte, t time.Time) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Join(s.Dir, "catalog-"+t.UTC().Format(fileLayout)+".tle.gz")
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(text)
	if err := zw.Close(); err != nil {
		return "", err
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, name); err != nil {
		return "", err
	}
	s.prune()
	return name, nil
}

// Catalogs: the files and their times, oldest first.
func (s Store) Catalogs() []CatalogFile {
	ents, _ := os.ReadDir(s.Dir)
	var out []CatalogFile
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "catalog-") || !strings.HasSuffix(n, ".tle.gz") {
			continue
		}
		t, err := time.Parse(fileLayout, strings.TrimSuffix(strings.TrimPrefix(n, "catalog-"), ".tle.gz"))
		if err != nil {
			continue
		}
		out = append(out, CatalogFile{Path: filepath.Join(s.Dir, n), Time: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func (s Store) prune() {
	c := s.Catalogs()
	for len(c) > max(s.Keep, 1) {
		os.Remove(c[0].Path)
		c = c[1:]
	}
}

// CatalogFile is one catalogue kept.
type CatalogFile struct {
	Path string
	Time time.Time // downloaded
}

// Nearest returns the catalogue downloaded nearest to t (its orbits are
// the most accurate then); false when there is none.
func (s Store) Nearest(t time.Time) (CatalogFile, bool) {
	var best CatalogFile
	ok := false
	for _, c := range s.Catalogs() {
		if !ok || absDur(c.Time.Sub(t)) < absDur(best.Time.Sub(t)) {
			best, ok = c, true
		}
	}
	return best, ok
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// Load reads the orbits of a catalogue file.
func Load(p string) ([]*Orbit, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return ParseTLE(zr)
}
