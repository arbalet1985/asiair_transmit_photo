// Package tsnode runs a Tailscale node inside the program (tsnet): the
// agent and the receiver join the user's tailnet and reach each other by
// name, without an open port on the router and with a "grey" IP address.
package tsnode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

func init() {
	// do not send the node's debug logs to Tailscale
	envknob.SetNoLogsNoSupport()
}

// States reported in Status.State.
const (
	StateOff      = "off"      // not enabled
	StateStarting = "starting" // connecting to Tailscale
	StateLogin    = "login"    // waiting for the user to log in (AuthURL)
	StateRunning  = "running"  // in the tailnet
	StateError    = "error"
)

// Status describes the node for the web page and the agent window.
type Status struct {
	State   string `json:"state"`
	AuthURL string `json:"auth_url,omitempty"` // log in here (StateLogin)
	Name    string `json:"name,omitempty"`     // short name in the tailnet, e.g. "asiair-receiver"
	DNSName string `json:"dns_name,omitempty"` // full name, e.g. "asiair-receiver.tail1234.ts.net"
	IP      string `json:"ip,omitempty"`       // 100.x.y.z
	User    string `json:"user,omitempty"`     // account the node belongs to
	Error   string `json:"error,omitempty"`
}

// Text is a short human-readable description of the status.
func (s Status) Text() string {
	switch s.State {
	case StateRunning:
		t := "подключено: " + s.Name
		if s.IP != "" {
			t += " (" + s.IP + ")"
		}
		return t
	case StateLogin:
		if s.AuthURL == "" {
			return "нужно войти в Tailscale, ссылка готовится…"
		}
		return "нужно войти в Tailscale"
	case StateStarting:
		return "подключение к Tailscale…"
	case StateError:
		return "ошибка: " + s.Error
	}
	return "выключено"
}

// Config of a node.
type Config struct {
	Dir        string // state directory (keys, login); kept between runs
	Hostname   string // name in the tailnet
	AuthKey    string // optional: joins without the interactive login
	ControlURL string // optional: Headscale or a test server; empty = Tailscale
	Logf       func(format string, args ...any)
}

// HasLogin reports whether the state directory keeps a login (tsnet's
// tailscaled.state): a node started there joins without asking again.
func HasLogin(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "tailscaled.state"))
	return err == nil && fi.Size() > 0
}

// Node is a running Tailscale node.
type Node struct {
	srv    *tsnet.Server
	cancel context.CancelFunc
	logf   func(format string, args ...any)

	mu sync.Mutex
	st Status
}

// Start starts the node. It returns at once; Status tells when it is ready
// or needs a login.
func Start(cfg Config) (*Node, error) {
	if cfg.Dir == "" || cfg.Hostname == "" {
		return nil, errors.New("tailscale: не заданы папка или имя")
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	srv := &tsnet.Server{
		Dir:        cfg.Dir,
		Hostname:   cfg.Hostname,
		AuthKey:    strings.TrimSpace(cfg.AuthKey),
		ControlURL: strings.TrimSpace(cfg.ControlURL),
		UserLogf:   func(string, ...any) {}, // the state is reported by Status
	}
	if err := srv.Start(); err != nil {
		srv.Close()
		return nil, fmt.Errorf("tailscale: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{srv: srv, cancel: cancel, logf: logf, st: Status{State: StateStarting}}
	go n.watch(ctx)
	return n, nil
}

// watch keeps the status current and logs its changes.
func (n *Node) watch(ctx context.Context) {
	lc, err := n.srv.LocalClient()
	if err != nil {
		n.set(Status{State: StateError, Error: err.Error()})
		return
	}
	for {
		st := n.poll(ctx, lc)
		n.set(st)
		d := time.Second
		if st.State == StateRunning {
			d = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

func (n *Node) poll(ctx context.Context, lc *local.Client) Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return n.Status()
		}
		return Status{State: StateError, Error: err.Error()}
	}
	return fromIPN(s)
}

func (n *Node) set(st Status) {
	n.mu.Lock()
	old := n.st
	n.st = st
	n.mu.Unlock()
	if old.State == st.State && old.AuthURL == st.AuthURL && old.Name == st.Name {
		return
	}
	switch st.State {
	case StateLogin:
		if st.AuthURL != "" {
			n.logf("Tailscale: нужно войти, откройте ссылку: %s", st.AuthURL)
		}
	case StateRunning:
		n.logf("Tailscale: %s", st.Text())
	case StateError:
		n.logf("Tailscale: %s", st.Error)
	}
}

// Status returns the current state.
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st
}

// Dial connects to a device of the tailnet, by name ("asiair-receiver") or
// by its 100.x.y.z address. It waits while the node is starting.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	// waiting for the user: say so at once instead of hanging until the timeout
	if st := n.Status(); st.State == StateLogin && st.AuthURL != "" {
		return nil, errors.New("Tailscale: " + st.Text())
	}
	c, err := n.srv.Dial(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("Tailscale: %w", err)
	}
	return c, nil
}

// Listen accepts connections from the tailnet, e.g. Listen(":8443").
func (n *Node) Listen(addr string) (net.Listener, error) {
	return n.srv.Listen("tcp", addr)
}

// ErrNoHTTPS: HTTPS certificates are not enabled for the tailnet.
var ErrNoHTTPS = errors.New("в панели Tailscale не включены сертификаты HTTPS: login.tailscale.com → DNS → включите MagicDNS и HTTPS Certificates")

// ListenTLS accepts HTTPS connections from the tailnet with a certificate
// for the node's name (e.g. asiair-receiver.tail1234.ts.net), issued via
// Tailscale. It needs a running node and HTTPS enabled for the tailnet.
func (n *Node) ListenTLS(addr string) (ln net.Listener, domain string, err error) {
	if n.Status().State != StateRunning {
		return nil, "", errors.New("Tailscale ещё не подключён")
	}
	domains := n.srv.CertDomains()
	if len(domains) == 0 {
		return nil, "", ErrNoHTTPS
	}
	ln, err = n.srv.ListenTLS("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	return ln, domains[0], nil
}

// CheckCert gets (or renews) the HTTPS certificate of the domain from
// Tailscale; the first one may take a minute to be issued.
func (n *Node) CheckCert(ctx context.Context, domain string) error {
	lc, err := n.srv.LocalClient()
	if err != nil {
		return err
	}
	_, _, err = lc.CertPair(ctx, domain)
	return err
}

// Login starts a new interactive login (a fresh link).
func (n *Node) Login(ctx context.Context) error {
	lc, err := n.srv.LocalClient()
	if err != nil {
		return err
	}
	return lc.StartLoginInteractive(ctx)
}

// Logout leaves the tailnet; a new login is needed afterwards.
func (n *Node) Logout(ctx context.Context) error {
	lc, err := n.srv.LocalClient()
	if err != nil {
		return err
	}
	if err := lc.Logout(ctx); err != nil {
		return err
	}
	n.set(Status{State: StateLogin})
	return lc.StartLoginInteractive(ctx)
}

// Close stops the node; the login is kept in Dir.
func (n *Node) Close() error {
	n.cancel()
	return n.srv.Close()
}

// ShortName returns the first label of a MagicDNS name.
func ShortName(dns string) string {
	dns = strings.TrimSuffix(dns, ".")
	name, _, _ := strings.Cut(dns, ".")
	return name
}

func firstIPv4(ips []netip.Addr) string {
	for _, ip := range ips {
		if ip.Is4() {
			return ip.String()
		}
	}
	if len(ips) > 0 {
		return ips[0].String()
	}
	return ""
}

// fromIPN converts the Tailscale status.
func fromIPN(s *ipnstate.Status) Status {
	st := Status{AuthURL: s.AuthURL, IP: firstIPv4(s.TailscaleIPs)}
	if s.Self != nil {
		st.DNSName = strings.TrimSuffix(s.Self.DNSName, ".")
		st.Name = ShortName(s.Self.DNSName)
		if u, ok := s.User[s.Self.UserID]; ok {
			st.User = u.LoginName
		}
	}

	switch s.BackendState {
	case ipn.Running.String():
		st.State = StateRunning
	case ipn.NeedsLogin.String():
		st.State = StateLogin
	case ipn.NeedsMachineAuth.String():
		st.State = StateError
		st.Error = "устройство ждёт подтверждения в панели Tailscale (Machines → Approve)"
	case ipn.Stopped.String():
		st.State = StateError
		st.Error = "Tailscale остановлен"
	default:
		st.State = StateStarting
	}
	return st
}

// Path says how traffic reaches the device host (short name, full name or
// 100.x.y.z address): "напрямую (addr)" or through a relay. "" when the
// device is unknown or the node is not running.
func (n *Node) Path(ctx context.Context, host string) string {
	if n.Status().State != StateRunning {
		return ""
	}
	lc, err := n.srv.LocalClient()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := lc.Status(ctx)
	if err != nil {
		return ""
	}
	return peerPath(s, host)
}

func peerPath(s *ipnstate.Status, host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range s.Peer {
		match := strings.EqualFold(ShortName(p.DNSName), host) || strings.EqualFold(strings.TrimSuffix(p.DNSName, "."), host) ||
			strings.EqualFold(p.HostName, host)
		for _, ip := range p.TailscaleIPs {
			match = match || ip.String() == host
		}
		if !match {
			continue
		}
		switch {
		case p.CurAddr != "":
			return "напрямую (" + p.CurAddr + ")"
		case p.PeerRelay != "":
			return "через ретранслятор (" + p.PeerRelay + ")"
		case p.Relay != "":
			return "через ретранслятор Tailscale «" + p.Relay + "» — скорость обычно не выше 1–2 МБ/с"
		}
		return "соединение устанавливается"
	}
	return ""
}
