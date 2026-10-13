package sorter

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/fits"
	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
	"github.com/arbalet1985/asiair_transmit_photo/internal/update"
)

// A receiver of this project (asiair-receiver) as the input: its received
// frames are listed through the page API, the header of each FITS is read
// with a small range request, and only the frames missing in the output
// are downloaded, resuming where a download stopped.
//
// The address is the one of the page: from another computer the remote
// access address (https://<address>:8443/ or the Tailscale one, with the
// password of «Доступ с телефона»), on the same computer http://127.0.0.1:8080/.

const sessionCookie = "asiair_session" // as in internal/receiver

// IsURL tells a receiver address from a folder.
func IsURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ErrLogin: the receiver wants the password (and the one-time code, when on).
var ErrLogin = errors.New("приёмник просит войти: введите пароль доступа с телефона")

// FingerprintError: the receiver presented another self-signed certificate
// than the one remembered at the first connection.
type FingerprintError struct{ Got, Want string }

func (e *FingerprintError) Error() string {
	return "сертификат приёмника сменился (был " + e.Want + ", сейчас " + e.Got + "). " +
		"Если приёмник переустанавливали, сверьте новый отпечаток со страницей приёмника и нажмите «Забыть сертификат»"
}

// Receiver is a connection to a receiver's page.
type Receiver struct {
	base   *url.URL
	client *http.Client

	mu      sync.Mutex
	pinned  string // fingerprint of the self-signed certificate
	session string // the login cookie
	// OnPin is told the fingerprint trusted at the first connection.
	OnPin func(fp string)
}

// NewReceiver prepares a connection. fingerprint is the certificate
// remembered before ("" = trust the first one), session the login kept
// from last time.
func NewReceiver(addr, fingerprint, session string) (*Receiver, error) {
	u, err := url.Parse(strings.TrimSpace(addr))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("неверный адрес приёмника %q: нужен вида https://адрес:8443/", addr)
	}
	u.Path, u.RawQuery, u.Fragment = strings.TrimRight(u.Path, "/"), "", ""
	r := &Receiver{base: u, pinned: normFP(fingerprint), session: session}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: r.verify},
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   8,
	}
	r.client = &http.Client{Transport: tr}
	return r, nil
}

// Base is the address, for messages and the cache.
func (r *Receiver) Base() string { return r.base.String() }

// Session is the login cookie to keep for the next time.
func (r *Receiver) Session() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

// Fingerprint is the certificate trusted ("" for plain http or a
// certificate of a public authority, as on Tailscale).
func (r *Receiver) Fingerprint() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pinned
}

// verify accepts a certificate a public authority vouches for (Tailscale's
// ts.net addresses); the receiver's own self-signed one is trusted at the
// first connection and must stay the same after that.
func (r *Receiver) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("приёмник не прислал сертификат")
	}
	opts := x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: x509.NewCertPool()}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := cs.PeerCertificates[0].Verify(opts); err == nil {
		return nil
	}
	fp := protocol.Fingerprint(cs.PeerCertificates[0].Raw)
	r.mu.Lock()
	if r.pinned == "" {
		r.pinned = normFP(fp)
		pin := r.OnPin
		r.mu.Unlock()
		if pin != nil {
			pin(fp)
		}
		return nil
	}
	want := r.pinned
	r.mu.Unlock()
	if normFP(fp) != want {
		return &FingerprintError{Got: fp, Want: want}
	}
	return nil
}

func normFP(s string) string {
	return strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.ToUpper(strings.TrimSpace(s)))
}

func (r *Receiver) url(p string, q url.Values) string {
	u := *r.base
	u.Path += p
	u.RawQuery = q.Encode()
	return u.String()
}

func (r *Receiver) do(req *http.Request) (*http.Response, error) {
	if s := r.Session(); s != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s})
	}
	resp, err := r.client.Do(req)
	if err != nil {
		var fe *FingerprintError
		if errors.As(err, &fe) {
			return nil, fe
		}
		return nil, fmt.Errorf("приёмник %s: %w", r.base.Host, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		basic := strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic")
		resp.Body.Close()
		if basic {
			return nil, errors.New("страница приёмника защищена логином (ui_password): укажите его в адресе, например http://логин:пароль@127.0.0.1:8080/")
		}
		return nil, ErrLogin
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusNotFound && msg == "404 page not found" {
			msg = "страницы нет: включите на приёмнике «Доступ с телефона» (через интернет или Tailscale) или проверьте адрес"
		}
		return nil, fmt.Errorf("приёмник ответил %d: %s", resp.StatusCode, msg)
	}
	return resp, nil
}

func (r *Receiver) get(ctx context.Context, p string, q url.Values, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url(p, q), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return r.do(req)
}

// Login signs in with the password of remote access and, when the
// receiver asks for them, a one-time code.
func (r *Receiver) Login(ctx context.Context, password, code string) error {
	body, _ := json.Marshal(map[string]string{"password": password, "code": strings.TrimSpace(code)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url("/login", nil), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		var fe *FingerprintError
		if errors.As(err, &fe) {
			return fe
		}
		return fmt.Errorf("приёмник %s: %w", r.base.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusNotFound {
			// the page on the receiver's own computer has no login
			return errors.New("вход не нужен или адрес не тот: на компьютере с приёмником используйте http://127.0.0.1:8080/")
		}
		return fmt.Errorf("вход не удался: %s", msg)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			r.mu.Lock()
			r.session = c.Value
			r.mu.Unlock()
			return nil
		}
	}
	return errors.New("приёмник не выдал сессию")
}

// RemoteFile is a file received by the receiver.
type RemoteFile struct {
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Local bool      `json:"local"` // received: can be downloaded
	// the stars of a light frame, as the receiver measured them
	Measured bool    `json:"q,omitempty"`
	HFR      float64 `json:"hfr,omitempty"`
	Stars    int     `json:"stars,omitempty"`
	// the tracks on it and the satellites that left them
	Tracks  []Track `json:"tracks,omitempty"`
	SatTold bool    `json:"sat_told,omitempty"`
	SatAge  float64 `json:"sat_age,omitempty"`
	SatErr  string  `json:"sat_err,omitempty"`
	Flip    bool    `json:"flip,omitempty"`
}

// deleteCmd is a delete request queued on the receiver's page.
type deleteCmd struct {
	Type      string `json:"type"`
	Path      string `json:"path"`
	AlsoLocal bool   `json:"also_local"`
	Status    string `json:"status"`
}

// dropsLocal reports whether the command, still pending, deletes the
// receiver's copy of the file p (the file itself or a folder it lies in).
func (c deleteCmd) dropsLocal(p string) bool {
	if !c.AlsoLocal || c.Status != "pending" {
		return false
	}
	return p == c.Path || c.Type == protocol.CmdDeleteDir && strings.HasPrefix(p, c.Path+"/")
}

// Files lists the FITS frames the receiver has received (thumbnails,
// frames still on the way and frames marked to be deleted on the receiver
// too left out).
func (r *Receiver) Files(ctx context.Context) ([]RemoteFile, error) {
	resp, err := r.get(ctx, "/ui/api/state", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st struct {
		Files    []RemoteFile `json:"files"`
		Commands []deleteCmd  `json:"commands"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("ответ приёмника не прочитан: %w", err)
	}
	var out []RemoteFile
files:
	for _, f := range st.Files {
		if !f.Local || !IsFITS(f.Path) {
			continue
		}
		for _, c := range st.Commands {
			if c.dropsLocal(f.Path) {
				continue files
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// Card is one keyword of a FITS header.
type Card struct{ Key, Value string }

// Header reads the header of a received FITS file with range requests:
// usually the first block of 2880 bytes is enough.
func (r *Receiver) Header(ctx context.Context, p string) ([]Card, error) {
	for _, n := range []int{8 * 2880, 128 * 2880} {
		resp, err := r.get(ctx, "/ui/file", url.Values{"path": {p}}, http.Header{"Range": {fmt.Sprintf("bytes=0-%d", n-1)}})
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		h, err := fits.ReadHeader(bytes.NewReader(b))
		if err == nil {
			cards := make([]Card, 0, len(h.Keys()))
			for _, k := range h.Keys() {
				cards = append(cards, Card{k, h.Get(k)})
			}
			return cards, nil
		}
		if len(b) < n { // the whole file was read
			return nil, err
		}
	}
	return nil, errors.New("заголовок FITS слишком длинный")
}

// Preview asks the receiver for a binned preview (as its page does) and
// returns it ready for fits.Image.Stretched.
func (r *Receiver) Preview(ctx context.Context, p string, maxWidth int) (*fits.Image, error) {
	resp, err := r.get(ctx, "/ui/preview", url.Values{"path": {p}, "max": {strconv.Itoa(maxWidth)}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, errors.New("пустой ответ приёмника")
	}
	n := int(binary.LittleEndian.Uint32(b))
	if 4+n > len(b) {
		return nil, errors.New("ответ приёмника обрезан")
	}
	var info struct {
		Width, Height, Channels, Binning int
		Bayer                            string
		Stats                            []fits.ChannelStats
	}
	if err := json.Unmarshal(b[4:4+n], &info); err != nil {
		return nil, err
	}
	px := b[4+n:]
	want := info.Width * info.Height * info.Channels
	if want <= 0 || len(px) < want*2 {
		return nil, errors.New("ответ приёмника обрезан")
	}
	im := &fits.Image{Width: info.Width, Height: info.Height, Channels: info.Channels, Binning: info.Binning, Bayer: info.Bayer,
		Stats: info.Stats, Data: make([]float32, want)}
	for i := range im.Data {
		im.Data[i] = float32(binary.LittleEndian.Uint16(px[i*2:])) / 65535
	}
	return im, nil
}

// Tilt asks the receiver to measure the tilt of the sensor on a frame (at
// its full resolution, there).
func (r *Receiver) Tilt(ctx context.Context, p string) (stars.Tilt, error) {
	var t stars.Tilt
	resp, err := r.get(ctx, "/ui/api/tilt", url.Values{"path": {p}}, nil)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&t)
	return t, err
}

// Region asks the receiver for the statistics of a small area of a frame,
// read there from the file itself (fits.ReadRegion).
func (r *Receiver) Region(ctx context.Context, p string, x, y, rad int) (*fits.RegionStats, error) {
	q := url.Values{"path": {p}, "x": {strconv.Itoa(x)}, "y": {strconv.Itoa(y)}, "r": {strconv.Itoa(rad)}}
	resp, err := r.get(ctx, "/ui/api/region", q, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ответил 404: страницы нет") {
			return nil, errors.New("замер кадра на приёмнике: нужен приёмник новой версии")
		}
		return nil, err
	}
	defer resp.Body.Close()
	var st fits.RegionStats
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// PartSuffix marks a download not finished yet; the next run continues it.
const PartSuffix = ".part"

// Download fetches a received file to dst, continuing dst+".part" left by
// an interrupted download. progress gets the bytes written so far.
func (r *Receiver) Download(ctx context.Context, p, dst string, size int64, mtime time.Time, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + PartSuffix
	var have int64
	if st, err := os.Stat(part); err == nil && st.Size() <= size {
		have = st.Size()
	}
	var hdr http.Header
	if have > 0 {
		hdr = http.Header{"Range": {fmt.Sprintf("bytes=%d-", have)}}
	}
	if have < size || size == 0 {
		resp, err := r.get(ctx, "/ui/file", url.Values{"path": {p}}, hdr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
		if resp.StatusCode != http.StatusPartialContent {
			flag, have = os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0 // the whole file came
		}
		f, err := os.OpenFile(part, flag, 0o644)
		if err != nil {
			return err
		}
		buf := make([]byte, 256<<10)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, err := f.Write(buf[:n]); err != nil {
					f.Close()
					return err
				}
				have += int64(n)
				if progress != nil {
					progress(have)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				f.Close()
				return rerr // the part stays for the next run
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if size > 0 && have != size {
		return fmt.Errorf("скачано %d байт из %d", have, size)
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	if !mtime.IsZero() {
		os.Chtimes(dst, mtime, mtime)
	}
	return nil
}

// HeaderCache keeps the headers read from a receiver, so the next run
// reads only the new frames. Entries are keyed by address, path, size and
// time of the file.
type HeaderCache struct {
	mu   sync.Mutex
	file string
	m    map[string][]Card
	used map[string]bool
}

// LoadHeaderCache reads the cache file ("" = keep in memory only).
func LoadHeaderCache(file string) *HeaderCache {
	c := &HeaderCache{file: file, m: map[string][]Card{}, used: map[string]bool{}}
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			json.Unmarshal(b, &c.m)
		}
	}
	return c
}

func cacheKey(base string, f RemoteFile) string {
	return fmt.Sprintf("%s|%s|%d|%d", base, f.Path, f.Size, f.MTime.Unix())
}

func (c *HeaderCache) get(k string) ([]Card, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if ok {
		c.used[k] = true
	}
	return v, ok
}

// keep marks an entry as still wanted without using it.
func (c *HeaderCache) keep(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; ok {
		c.used[k] = true
	}
}

func (c *HeaderCache) put(k string, v []Card) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k], c.used[k] = v, true
}

// Save writes the cache, dropping the entries of files that are gone
// (only for the receiver just scanned).
func (c *HeaderCache) Save(base string) error {
	if c.file == "" {
		return nil
	}
	c.mu.Lock()
	for k := range c.m {
		if strings.HasPrefix(k, base+"|") && !c.used[k] {
			delete(c.m, k)
		}
	}
	b, err := json.Marshal(c.m)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.file), 0o755); err != nil {
		return err
	}
	tmp := c.file + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.file)
}

// ScanReceiver lists the frames of a receiver and reads their headers
// (several at a time; the cached ones are not read again).
func ScanReceiver(ctx context.Context, r *Receiver, cache *HeaderCache, progress func(done, total int)) ([]Frame, []error) {
	frames, _, errs := ScanReceiverOnly(ctx, r, cache, nil, progress)
	return frames, errs
}

// ScanReceiverOnly is ScanReceiver with only the folders chosen read; the
// files of the other folders are returned as skipped (for Layout.Elsewhere).
func ScanReceiverOnly(ctx context.Context, r *Receiver, cache *HeaderCache, only Folders, progress func(done, total int)) ([]Frame, []RemoteFile, []error) {
	all, err := r.Files(ctx)
	if err != nil {
		return nil, nil, []error{err}
	}
	if cache == nil {
		cache = LoadHeaderCache("")
	}
	var files, skipped []RemoteFile
	for _, f := range all {
		if only.Has(f.Path) {
			files = append(files, f)
		} else {
			skipped = append(skipped, f)
			cache.keep(cacheKey(r.Base(), f)) // read again when chosen later
		}
	}
	frames, errs := scanFiles(ctx, r, cache, files, progress)
	return frames, skipped, errs
}

func scanFiles(ctx context.Context, r *Receiver, cache *HeaderCache, files []RemoteFile, progress func(done, total int)) ([]Frame, []error) {
	if cache == nil {
		cache = LoadHeaderCache("")
	}
	frames := make([]Frame, len(files))
	ok := make([]bool, len(files))
	errs := make([]error, len(files))
	var mu sync.Mutex
	done := 0
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				f := files[i]
				k := cacheKey(r.Base(), f)
				cards, hit := cache.get(k)
				if !hit {
					var err error
					cards, err = r.Header(ctx, f.Path)
					if err != nil {
						errs[i] = fmt.Errorf("%s: %w", f.Path, err)
					} else {
						cache.put(k, cards)
					}
				}
				if errs[i] == nil {
					frames[i], ok[i] = remoteFrame(r, f, cards), true
				}
				mu.Lock()
				done++
				if progress != nil && (done%20 == 0 || done == len(files)) {
					progress(done, len(files))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range files {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var out []Frame
	var bad []error
	for i := range files {
		if ok[i] {
			out = append(out, frames[i])
		} else if errs[i] != nil {
			bad = append(bad, errs[i])
		}
	}
	if err := ctx.Err(); err != nil {
		bad = append(bad, err)
	}
	return out, bad
}

func remoteFrame(r *Receiver, f RemoteFile, cards []Card) Frame {
	m := make(map[string]string, len(cards))
	for _, c := range cards {
		m[c.Key] = c.Value
	}
	fr := describe(func(k string) string { return m[k] }, f.Path, f.MTime)
	fr.Path, fr.Size, fr.Src, fr.MTime, fr.Cards = f.Path, f.Size, r, f.MTime, cards
	fr.Measured, fr.HFR, fr.Stars = f.Measured, f.HFR, f.Stars
	fr.Tracks, fr.SatTold, fr.SatAge, fr.SatErr, fr.FlipRows = f.Tracks, f.SatTold, f.SatAge, f.SatErr, f.Flip
	return fr
}

// SorterUpdate asks the receiver which sorter program it keeps for the
// sorters (downloaded with its own update). Version is "" when none, and
// an old receiver without it answers with an error.
func (r *Receiver) SorterUpdate(ctx context.Context) (protocol.UpdateOffer, error) {
	var o protocol.UpdateOffer
	resp, err := r.get(ctx, "/ui/api/sorter", nil, nil)
	if err != nil {
		return o, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&o)
	return o, err
}

// NewSorter reports whether the sorter offered is worth installing: a newer
// version with other code than the running one. A sorter built by hand
// ("dev") is never replaced.
func NewSorter(o protocol.UpdateOffer) bool {
	if !update.Valid(protocol.Version) || !update.Newer(o.Version, protocol.Version) {
		return false
	}
	return o.Build == "" || o.Build != protocol.Build
}

// DownloadSorter saves the sorter program the receiver keeps to dst,
// checking its size and checksum against the offer.
func (r *Receiver) DownloadSorter(ctx context.Context, o protocol.UpdateOffer, dst string, progress func(done, total int64)) error {
	resp, err := r.get(ctx, "/ui/sorter.exe", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if progress != nil {
		body = &countReader{r: body, total: o.Size, f: progress}
	}
	_, err = update.Save(body, dst, o.Size, o.SHA256)
	return err
}

type countReader struct {
	r           io.Reader
	done, total int64
	f           func(done, total int64)
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.done += int64(n)
	c.f(c.done, c.total)
	return n, err
}
