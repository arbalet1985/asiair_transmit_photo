package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// Credentials used when none are configured. The ASIAIR normally maps any
// login to its guest account; "." / "123456789" is known to work on firmware
// versions that refuse a plain guest login.
var guestAttempts = [][2]string{{"guest", ""}, {".", "123456789"}}

// SMB is a Source backed by SMB shares on the ASIAIR.
type SMB struct {
	host     string
	shares   []string
	auto     bool // shares are listed on the device: every disk share is walked
	user     string
	password string
	domain   string

	mu      sync.Mutex
	conn    net.Conn
	session *smb2.Session
	mounts  map[string]*smb2.Share

	lastSkip string // last logged "share skipped" message, to avoid repeating it
	lastList string // last logged share list
	login    string // how the current session logged in
}

// NewSMB creates an SMB source. Nothing is connected until first use.
func NewSMB(l *Link, defaultShares []string, user, password, domain string) *SMB {
	host := l.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "445")
	}
	shares, auto := resolveShares(l.Shares, defaultShares)
	if l.Username != "" {
		user, password = l.Username, l.Password
	}
	return &SMB{host: host, shares: shares, auto: auto, user: user, password: password, domain: domain}
}

// resolveShares decides which shares to walk. A share named in the link is
// used alone, except the ASIAIR internal memory ("EMMC Images", which the
// device search puts into the link): that one stands for the whole ASIAIR.
// With no share given, or "*" in the list, or the list of older versions
// (["EMMC Images", "Udisk"]), all disk shares of the device are walked:
// eMMC, SD card, USB stick, whatever the firmware calls them.
func resolveShares(linkShares, defaultShares []string) (shares []string, auto bool) {
	if len(linkShares) > 0 && !(len(linkShares) == 1 && strings.EqualFold(linkShares[0], DefaultShare)) {
		return linkShares, false
	}
	var list []string
	for _, sh := range defaultShares {
		if sh = strings.TrimSpace(sh); sh != "" {
			list = append(list, sh)
		}
	}
	if len(list) == 0 || hasShare(list, AllShares) || isLegacyDefault(list) {
		return fallbackShares, true
	}
	return list, false
}

// AllShares in the "shares" setting means every disk share of the device.
const AllShares = "*"

// fallbackShares are walked in the automatic mode when the device does not
// tell its share list.
var fallbackShares = []string{DefaultShare, USBShare}

func isLegacyDefault(list []string) bool {
	return len(list) == 2 && hasShare(list, DefaultShare) && hasShare(list, USBShare)
}

// shareNames returns the shares to walk: the fixed list, or in the automatic
// mode the disk shares the device has right now (an SD card or a USB stick
// can appear and disappear).
func (s *SMB) shareNames() []string {
	if !s.auto {
		return s.shares
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return s.shares
	}
	names, err := s.session.ListSharenames()
	var out []string
	for _, n := range names {
		if n != "" && !strings.HasSuffix(n, "$") {
			out = append(out, n)
		}
	}
	if err != nil || len(out) == 0 {
		msg := fmt.Sprintf("ASIAIR: список шар не получен (%v), обход: %s", err, strings.Join(s.shares, ", "))
		if msg != s.lastList {
			log.Print(msg)
			s.lastList = msg
		}
		return s.shares
	}
	sortShares(out)
	if msg := "ASIAIR: шары: " + strings.Join(out, ", "); msg != s.lastList {
		log.Print(msg)
		s.lastList = msg
	}
	return out
}

// sortShares puts the internal memory first, the rest by name.
func sortShares(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		a, b := strings.EqualFold(names[i], DefaultShare), strings.EqualFold(names[j], DefaultShare)
		if a != b {
			return a
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
}

func (s *SMB) dial(ctx context.Context, user, pass string) (net.Conn, *smb2.Session, error) {
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", s.host)
	if err != nil {
		return nil, nil, err
	}
	sd := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: pass, Domain: s.domain}}
	sess, err := sd.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, sess, nil
}

// connect returns a live session, logging in if needed. Caller holds s.mu.
func (s *SMB) connect(ctx context.Context) error {
	if s.session != nil {
		return nil
	}
	attempts := guestAttempts
	if s.user != "" {
		attempts = [][2]string{{s.user, s.password}}
	}
	var lastErr error
	for _, a := range attempts {
		conn, sess, err := s.dial(ctx, a[0], a[1])
		if err != nil {
			lastErr = err
			var ne net.Error
			if errors.As(err, &ne) || ctx.Err() != nil {
				break // network problem, other credentials will not help
			}
			continue
		}
		who := a[0]
		if s.user == "" {
			who = "гость (" + a[0] + ")"
		}
		log.Printf("ASIAIR: подключено к %s как %s", s.host, who)
		s.login = who
		s.conn, s.session, s.mounts = conn, sess, map[string]*smb2.Share{}
		return nil
	}
	return fmt.Errorf("не удалось подключиться к ASIAIR %s: %w", s.host, lastErr)
}

func (s *SMB) share(ctx context.Context, name string) (*smb2.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	if m, ok := s.mounts[name]; ok {
		return m, nil
	}
	m, err := s.session.Mount(name)
	if err != nil {
		return nil, fmt.Errorf("шара %q: %w", name, err)
	}
	s.mounts[name] = m
	return m, nil
}

// Reconnect drops the connection (e.g. after the network changed): the
// next call connects again.
func (s *SMB) Reconnect() { s.reset() }

// reset drops the connection so the next call reconnects.
func (s *SMB) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *SMB) closeLocked() {
	for _, m := range s.mounts {
		m.Umount()
	}
	if s.session != nil {
		s.session.Logoff()
	}
	if s.conn != nil {
		s.conn.Close()
	}
	s.conn, s.session, s.mounts = nil, nil, nil
}

// isConnErr reports whether err means the SMB connection is broken
// (as opposed to e.g. a missing file).
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return false
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		var re *smb2.ResponseError
		if errors.As(pe.Err, &re) {
			return false
		}
	}
	return true
}

func smbPath(p string) string { return strings.ReplaceAll(p, "/", `\`) }

func (s *SMB) Walk(ctx context.Context, skipDir func(string) bool, fn func(File) error) error {
	s.mu.Lock()
	err := s.connect(ctx)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	var errs []error
	shares := s.shareNames()
	for _, name := range shares {
		m, err := s.share(ctx, name)
		if err != nil {
			// e.g. "Udisk" when no USB stick is inserted
			errs = append(errs, err)
			continue
		}
		if err := s.walkDir(ctx, m.WithContext(ctx), name, "", skipDir, fn); err != nil {
			if isConnErr(err) {
				s.reset()
			}
			return err
		}
	}
	if len(errs) == len(shares) {
		s.reset()
		return errors.Join(errs...)
	}
	skip := fmt.Sprint(errs)
	if len(errs) > 0 && skip != s.lastSkip {
		for _, e := range errs {
			log.Printf("ASIAIR: пропуск: %v", e)
		}
	}
	s.lastSkip = skip
	return nil
}

func (s *SMB) walkDir(ctx context.Context, m *smb2.Share, share, dir string, skipDir func(string) bool, fn func(File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := dir
	if name == "" {
		name = "."
	}
	entries, err := m.ReadDir(smbPath(name))
	if err != nil {
		if !isConnErr(err) && dir != "" {
			log.Printf("ASIAIR: не удалось прочитать папку %s/%s: %v", share, dir, err)
			return nil
		}
		return err
	}
	for _, e := range entries {
		rel := path.Join(dir, e.Name())
		full := share + "/" + rel
		if e.IsDir() {
			if skipDir != nil && skipDir(full) {
				continue
			}
			if err := s.walkDir(ctx, m, share, rel, skipDir, fn); err != nil {
				return err
			}
			continue
		}
		if !e.Mode().IsRegular() {
			continue
		}
		if err := fn(File{Path: full, Size: e.Size(), ModTime: e.ModTime()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *SMB) Open(p string) (io.ReadSeekCloser, error) {
	share, rest, err := SplitPath(p)
	if err != nil {
		return nil, err
	}
	m, err := s.share(context.Background(), share)
	if err != nil {
		return nil, err
	}
	f, err := m.Open(smbPath(rest))
	if err != nil && isConnErr(err) {
		s.reset()
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return newReadAhead(f, f, st.Size()), nil
}

func (s *SMB) Remove(p string) error {
	share, rest, err := SplitPath(p)
	if err != nil {
		return err
	}
	m, err := s.share(context.Background(), share)
	if err != nil {
		return err
	}
	err = m.Remove(smbPath(rest))
	if err != nil && isConnErr(err) {
		s.reset()
	}
	return err
}

func (s *SMB) RemoveAll(p string) error {
	share, rest, err := SplitPath(p)
	if err != nil {
		return err
	}
	m, err := s.share(context.Background(), share)
	if err != nil {
		return err
	}
	err = m.RemoveAll(smbPath(rest))
	if err != nil && isConnErr(err) {
		s.reset()
	}
	return err
}

func (s *SMB) Close() error {
	s.reset()
	return nil
}

func (s *SMB) Probe(ctx context.Context) (*ProbeResult, error) {
	s.mu.Lock()
	err := s.connect(ctx)
	login := s.login
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	res := &ProbeResult{Login: login}
	ok := false
	shares := s.shareNames()
	for _, name := range shares {
		sp := ShareProbe{Name: name}
		m, err := s.share(ctx, name)
		if err == nil {
			var entries []fs.FileInfo
			entries, err = m.WithContext(ctx).ReadDir(".")
			sp.Entries = len(entries)
		}
		sp.Err = err
		ok = ok || err == nil
		res.Shares = append(res.Shares, sp)
	}
	if !ok {
		s.reset()
		return res, fmt.Errorf("ни одна шара не доступна (%s)", strings.Join(shares, ", "))
	}
	return res, nil
}

// Space reports the free space of every ASIAIR share that answers.
func (s *SMB) Space(ctx context.Context) []ShareSpace {
	s.mu.Lock()
	err := s.connect(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil
	}
	var out []ShareSpace
	for _, name := range s.shareNames() {
		m, err := s.share(ctx, name)
		if err != nil {
			continue
		}
		st, err := m.WithContext(ctx).Statfs(".")
		if err != nil {
			continue
		}
		if sp := shareSpace(name, st); sp.Total > 0 {
			out = append(out, sp)
		}
	}
	return out
}

// shareSpace converts the FileFsFullSizeInformation of a share to bytes.
// go-smb2 counts in allocation units but its BlockSize is only the sector
// size: a unit is SectorsPerAllocationUnit (FragmentSize) sectors, and Samba
// on the ASIAIR reports two 512-byte sectors per unit — without it every size
// came out half of the real one.
func shareSpace(name string, st smb2.FileFsInfo) ShareSpace {
	unit := st.BlockSize()
	if n := st.FragmentSize(); n > 0 {
		unit *= n
	}
	return ShareSpace{Share: name, Free: st.AvailableBlockCount() * unit, Total: st.TotalBlockCount() * unit}
}
