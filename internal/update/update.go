// Package update finds new versions among the GitHub releases, downloads
// the executables with a checksum check and replaces the running program.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is where the releases are published.
const DefaultRepo = "arbalet1985/asiair_transmit_photo"

// Asset names in a release.
const (
	AgentAsset    = "asiair-agent.exe"
	ReceiverAsset = "asiair-receiver.exe"
	// BuildsAsset maps each program of a release to its build (see
	// protocol.Build): a program whose build is the same as the running one
	// has not changed in that release.
	BuildsAsset = "builds.json"
)

// Release is a published version.
type Release struct {
	Version string // "1.2.0", without "v"
	Tag     string // "v1.2.0"
	URL     string // release page
	Assets  map[string]Asset
}

// Asset is a file attached to a release.
type Asset struct {
	Name   string
	Size   int64
	SHA256 string // hex, from the GitHub digest; may be empty on old releases
	api    string // API address used for private repositories
	public string // direct download address
}

// parse splits "v1.2.3-beta.1" into numbers and a pre-release suffix.
func parse(v string) (nums [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// Valid reports whether v is a release version such as "1.2.0". Builds
// without one ("dev") are never replaced automatically.
func Valid(v string) bool {
	_, _, ok := parse(v)
	return ok
}

// Newer reports whether version a is newer than b. Invalid versions are
// never newer, and nothing is newer than an invalid (development) b.
func Newer(a, b string) bool {
	na, pa, oka := parse(a)
	nb, pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	// 1.2.0 is newer than 1.2.0-beta
	switch {
	case pa == pb:
		return false
	case pa == "":
		return true
	case pb == "":
		return false
	}
	return pa > pb
}

// Client talks to the GitHub API.
type Client struct {
	Repo  string // "owner/name"; DefaultRepo when empty
	Token string // GitHub token: needed only for a private repository
	HTTP  *http.Client
	// API is the API root, for tests. Default https://api.github.com.
	API string
	// Progress, when set, is told how much Download has received
	// (total is 0 when unknown).
	Progress func(done, total int64)
}

func (c *Client) api() string {
	if c.API != "" {
		return strings.TrimRight(c.API, "/")
	}
	return "https://api.github.com"
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (c *Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) request(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "asiair-transmit-photo")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound:
			if c.Token == "" {
				return nil, errors.New("GitHub: релизы не найдены — репозиторий приватный, укажите токен GitHub в настройках обновления")
			}
			return nil, errors.New("GitHub: релизы не найдены — проверьте, что у токена есть доступ к репозиторию " + c.repo())
		case http.StatusUnauthorized:
			return nil, errors.New("GitHub: токен не подходит (устарел или отозван)")
		}
		return nil, fmt.Errorf("GitHub ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	resp, err := c.request(ctx, c.api()+"/repos/"+c.repo()+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
			URL    string `json:"url"`
			Public string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("GitHub: %w", err)
	}
	if !Valid(r.Tag) {
		return nil, fmt.Errorf("GitHub: у последнего релиза непонятный номер версии %q", r.Tag)
	}
	rel := &Release{Version: strings.TrimPrefix(r.Tag, "v"), Tag: r.Tag, URL: r.URL, Assets: map[string]Asset{}}
	for _, a := range r.Assets {
		sum, _ := strings.CutPrefix(a.Digest, "sha256:")
		rel.Assets[a.Name] = Asset{Name: a.Name, Size: a.Size, SHA256: strings.ToLower(sum), api: a.URL, public: a.Public}
	}
	return rel, nil
}

// Builds returns the build of each program of the release, or nil when
// the release does not say (releases before builds.json).
func (c *Client) Builds(ctx context.Context, rel *Release) (map[string]string, error) {
	a, ok := rel.Assets[BuildsAsset]
	if !ok {
		return nil, nil
	}
	url := a.public
	if c.Token != "" && a.api != "" {
		url = a.api
	}
	resp, err := c.request(ctx, url, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", BuildsAsset, err)
	}
	return m, nil
}

// Unchanged reports whether the program asset of a release with builds is
// the same code as the running build (then there is nothing to update).
// Unknown builds (old releases, programs built by hand) are never the same.
func Unchanged(builds map[string]string, asset, running string) bool {
	return running != "" && builds[asset] == running
}

// Download saves the asset to dst (via a temporary file) and verifies its
// size and checksum. It returns the SHA-256 of the file.
func (c *Client) Download(ctx context.Context, a Asset, dst string) (string, error) {
	url := a.public
	if c.Token != "" && a.api != "" {
		url = a.api // private repositories need the API with the token
	}
	resp, err := c.request(ctx, url, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if c.Progress != nil {
		total := a.Size
		if total <= 0 && resp.ContentLength > 0 {
			total = resp.ContentLength
		}
		body = &progressReader{r: body, total: total, f: c.Progress}
	}
	return Save(body, dst, a.Size, a.SHA256)
}

type progressReader struct {
	r           io.Reader
	done, total int64
	f           func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.f(p.done, p.total)
	return n, err
}

// Save writes r to dst through a temporary file, checking the size (when
// > 0) and the SHA-256 (when not empty). It returns the SHA-256.
func Save(r io.Reader, dst string, size int64, sum string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	got := hex.EncodeToString(h.Sum(nil))
	switch {
	case err != nil:
	case size > 0 && n != size:
		err = fmt.Errorf("скачано %d байт вместо %d", n, size)
	case sum != "" && !strings.EqualFold(got, sum):
		err = errors.New("контрольная сумма скачанного файла не совпала")
	case n < 2:
		err = errors.New("скачан пустой файл")
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return got, nil
}

// FileSHA256 returns the checksum of a file.
func FileSHA256(name string) (string, error) {
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

// The path of the program as it was started. Taken once: after Install
// renamed the running file, the system reports the new name of the old
// program (Linux does), not the path where the new version now is.
var startExe, startExeErr = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}()

// Executable returns the path the running program was started from.
func Executable() (string, error) { return startExe, startExeErr }

// An antivirus may hold a freshly written or started program for a moment:
// renaming is tried again for a while before the update gives up.
var (
	renameTries = 6
	renameDelay = 2 * time.Second
)

// renameRetry is os.Rename, tried again while the file is busy.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < renameTries; i++ {
		if i > 0 {
			time.Sleep(renameDelay)
		}
		if err = os.Rename(from, to); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return err
}

// moveAside gets a file that cannot be deleted out of the way. Windows
// refuses to delete a program that is running (e.g. a copy of an old
// version left running, or the watchdog of the last update) but lets it be
// renamed; an antivirus may hold the file for a moment, so both are tried
// again for a while. Cleanup deletes the renamed copy later. It reports
// whether p is gone.
func moveAside(p string) bool {
	for i := 0; i < renameTries; i++ {
		if i > 0 {
			time.Sleep(renameDelay)
		}
		makeWritable(p) // a read-only file cannot be deleted on Windows
		if err := os.Remove(p); err == nil || errors.Is(err, fs.ErrNotExist) {
			return true
		}
		if err := renameAside(p, fmt.Sprintf("%s.%d%s", p, time.Now().UnixNano(), deletedSuffix)); err == nil {
			return true
		}
	}
	_, err := os.Stat(p)
	return errors.Is(err, fs.ErrNotExist)
}

const deletedSuffix = ".del"

// makeWritable clears the read-only attribute of a file (only of a file:
// a folder keeps its rights).
func makeWritable(p string) {
	if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o200 == 0 {
		os.Chmod(p, st.Mode().Perm()|0o200)
	}
}

var renameAside = os.Rename // tests make it fail

// spareBackup is the name of the previous program when Backup(exe) is held
// by something and cannot be replaced: name.old.<n>.exe.
func spareBackup(exe string) string {
	b := Backup(exe)
	ext := filepath.Ext(exe)
	return fmt.Sprintf("%s.%d%s", strings.TrimSuffix(b, ext), time.Now().UnixNano(), ext)
}

// isSpareBackup tells a file name made by spareBackup for exe.
func isSpareBackup(exe, name string) bool {
	b := filepath.Base(Backup(exe))
	ext := filepath.Ext(exe)
	stem := strings.TrimSuffix(b, ext) + "."
	if !strings.HasPrefix(name, stem) || !strings.HasSuffix(name, ext) {
		return false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(name, stem), ext)
	if n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// installed: where Install of this process put the previous program, for
// StartWatch.
var (
	installedMu sync.Mutex
	installed   = map[string]string{}
)

// Install replaces the program exe with the file newFile. Windows cannot
// overwrite a running program but can rename it: the old one is kept as
// Backup(exe) (or, when an earlier copy there cannot be moved, under a
// spare name) and removed once the new version works (Cleanup).
func Install(exe, newFile string) error {
	old := Backup(exe)
	if !moveAside(old) {
		log.Printf("Обновление: старая копия %s занята и не удаляется — прежняя версия сохраняется под другим именем", old)
		old = spareBackup(exe)
	}
	if err := renameRetry(exe, old); err != nil {
		return fmt.Errorf("не удалось заменить %s: %w (файл занят другой программой — антивирусом или ещё одной копией приёмника или агента, — либо нет прав на запись в папку программы)", exe, err)
	}
	if err := renameRetry(newFile, exe); err != nil {
		os.Rename(old, exe) // roll back
		return fmt.Errorf("не удалось заменить %s: %w", exe, err)
	}
	os.Chmod(exe, 0o755) // a downloaded file is not executable on Linux
	installedMu.Lock()
	installed[exe] = old
	installedMu.Unlock()
	return nil
}

// Cleanup removes what an earlier update left next to the program.
func Cleanup(exe string) {
	for _, p := range []string{exe + ".old", Backup(exe), exe + ".bad", exe + ".new", exe + ".new.part"} {
		makeWritable(p)
		os.Remove(p)
	}
	// copies moved aside by Install, once they are no longer running
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		n := e.Name()
		for _, base := range []string{filepath.Base(exe) + ".old", filepath.Base(Backup(exe))} {
			if strings.HasPrefix(n, base+".") && strings.HasSuffix(n, deletedSuffix) {
				os.RemoveAll(filepath.Join(filepath.Dir(exe), n))
			}
		}
		if isSpareBackup(exe, n) {
			p := filepath.Join(filepath.Dir(exe), n)
			makeWritable(p)
			os.Remove(p)
		}
	}
}
