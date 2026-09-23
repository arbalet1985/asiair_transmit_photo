package receiver

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// partialDir holds unfinished uploads. It lives inside the destination folder
// so the final rename never crosses volumes.
const partialDir = ".partial"

// Store keeps received files under root, mirroring the ASIAIR layout.
type Store struct {
	root  string
	locks sync.Map // path -> *sync.Mutex
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, partialDir), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// ErrOffset is returned when an upload does not continue where the previous one stopped.
type ErrOffset struct{ Have int64 }

func (e *ErrOffset) Error() string {
	return fmt.Sprintf("ожидалось смещение %d", e.Have)
}

// CleanPath validates a remote path ("share/dir/file") and returns it normalised.
func CleanPath(p string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", errors.New("пустой путь")
	}
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return "", fmt.Errorf("неверный путь %q", p)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `:*?"<>|`) || s == partialDir {
			return "", fmt.Errorf("недопустимый путь %q", p)
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return "", fmt.Errorf("недопустимый путь %q", p)
			}
		}
	}
	return strings.Join(segs, "/"), nil
}

func (s *Store) lock(p string) func() {
	m, _ := s.locks.LoadOrStore(p, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Local returns the final location of a remote path.
func (s *Store) Local(p string) string {
	return filepath.Join(s.root, filepath.FromSlash(p))
}

type partMeta struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func (s *Store) partPaths(p string) (data, meta string) {
	h := sha1.Sum([]byte(p))
	base := filepath.Join(s.root, partialDir, hex.EncodeToString(h[:]))
	return base + ".part", base + ".json"
}

func sameTime(a, b time.Time) bool {
	d := a.Sub(b)
	return d < 2*time.Second && d > -2*time.Second
}

// Complete reports whether the final file already exists with this size and mtime.
func (s *Store) Complete(p string, size int64, mtime time.Time) bool {
	st, err := os.Stat(s.Local(p))
	return err == nil && st.Mode().IsRegular() && st.Size() == size && sameTime(st.ModTime(), mtime)
}

// Offset returns how many bytes of the given file version have been received.
// A partial upload of a different version is discarded.
func (s *Store) Offset(p string, size int64, mtime time.Time) (int64, bool) {
	unlock := s.lock(p)
	defer unlock()
	return s.offsetLocked(p, size, mtime)
}

func (s *Store) offsetLocked(p string, size int64, mtime time.Time) (int64, bool) {
	if s.Complete(p, size, mtime) {
		return size, true
	}
	data, meta := s.partPaths(p)
	var m partMeta
	if b, err := os.ReadFile(meta); err == nil && json.Unmarshal(b, &m) == nil &&
		m.Path == p && m.Size == size && m.MTime == mtime.UnixNano() {
		if st, err := os.Stat(data); err == nil && st.Size() <= size {
			return st.Size(), false
		}
	}
	os.Remove(data)
	os.Remove(meta)
	return 0, false
}

// Write appends body to the partial file, which must currently hold exactly
// offset bytes. When the file is complete its SHA-256 is returned.
func (s *Store) Write(p string, size int64, mtime time.Time, offset int64, body io.Reader) (int64, string, error) {
	unlock := s.lock(p)
	defer unlock()
	have, done := s.offsetLocked(p, size, mtime)
	if done {
		return size, "", nil
	}
	if have != offset {
		return have, "", &ErrOffset{Have: have}
	}
	data, meta := s.partPaths(p)
	if offset == 0 {
		b, _ := json.Marshal(partMeta{Path: p, Size: size, MTime: mtime.UnixNano()})
		if err := os.WriteFile(meta, b, 0o644); err != nil {
			return 0, "", err
		}
	}
	f, err := os.OpenFile(data, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return offset, "", err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return offset, "", err
	}
	n, copyErr := io.Copy(f, io.LimitReader(body, size-offset))
	closeErr := f.Close()
	got := offset + n
	if copyErr != nil {
		return got, "", copyErr
	}
	if closeErr != nil {
		return got, "", closeErr
	}
	if got < size {
		return got, "", io.ErrUnexpectedEOF
	}
	sum, err := hashFile(data)
	return got, sum, err
}

func hashFile(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrChecksum means the received data does not match the source.
var ErrChecksum = errors.New("контрольная сумма не совпала, файл будет передан заново")

// Commit verifies the checksum and moves the finished file into place.
func (s *Store) Commit(p string, size int64, mtime time.Time, sum string) error {
	unlock := s.lock(p)
	defer unlock()
	if s.Complete(p, size, mtime) {
		return nil
	}
	data, meta := s.partPaths(p)
	st, err := os.Stat(data)
	if err != nil || st.Size() != size {
		return &ErrOffset{Have: 0}
	}
	got, err := hashFile(data)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, sum) {
		os.Remove(data)
		os.Remove(meta)
		return ErrChecksum
	}
	dst := s.Local(p)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	os.Remove(dst) // Windows cannot rename over an existing file
	if err := os.Rename(data, dst); err != nil {
		return err
	}
	os.Remove(meta)
	return os.Chtimes(dst, mtime, mtime)
}

// LocalFile is a received file.
type LocalFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// List returns the received files of the given ASIAIR shares (top-level
// folders), sorted by path. Only those folders are walked, so the
// destination can be any folder, even the root of a drive.
func (s *Store) List(shares []string) ([]LocalFile, error) {
	var out []LocalFile
	seen := map[string]bool{}
	for _, share := range shares {
		if share == "" || seen[share] || share == partialDir || strings.ContainsAny(share, `/\`) {
			continue
		}
		seen[share] = true
		base := filepath.Join(s.root, share)
		filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(s.root, p)
			out = append(out, LocalFile{Path: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Remove deletes a received file and empty parent folders.
func (s *Store) Remove(p string) error {
	unlock := s.lock(p)
	defer unlock()
	dst := s.Local(p)
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	root := filepath.Clean(s.root)
	for dir := filepath.Dir(dst); dir != root && strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}
