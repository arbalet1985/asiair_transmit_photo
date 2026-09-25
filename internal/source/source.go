// Package source provides access to the files stored on the ASIAIR.
//
// The ASIAIR exposes its storage as SMB shares: "EMMC Images" (internal
// memory) and "Udisk" (USB stick, when inserted). By default the shares are
// accessible as guest, without a username or password. Paths used by this
// package are "<share>/<path inside share>" with forward slashes.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// DefaultShare is the ASIAIR internal storage share.
const DefaultShare = "EMMC Images"

// USBShare is the ASIAIR share of its USB stick. Files read from the stick
// plugged into this computer get the same paths, so what was already
// received over the network is not sent again.
const USBShare = "Udisk"

// File describes a file found on the source.
type File struct {
	Path    string // "<share>/<dir>/<name>"
	Size    int64
	ModTime time.Time
}

// Source lists, reads and deletes files.
type Source interface {
	// Walk calls fn for every regular file. skipDir(path) may prune directories.
	Walk(ctx context.Context, skipDir func(path string) bool, fn func(File) error) error
	Open(path string) (io.ReadSeekCloser, error)
	Remove(path string) error
	Close() error
	// Probe connects and lists the top of each share, without walking the
	// whole tree: a quick availability check.
	Probe(ctx context.Context) (*ProbeResult, error)
}

// ProbeResult describes what a quick check found.
type ProbeResult struct {
	Login  string // how the connection was authenticated, e.g. "гость (guest)"
	Shares []ShareProbe
}

type ShareProbe struct {
	Name    string
	Entries int   // files and folders at the top of the share
	Err     error // share not available (e.g. "Udisk" without a USB stick)
}

// Link is a parsed ASIAIR link.
type Link struct {
	Kind     string // "smb", "dir" or "usb"
	Host     string // host or host:port
	Shares   []string
	Username string
	Password string
	Dir      string // for Kind "dir" and "usb"; "usb" with no Dir = any removable disk
}

// ParseLink understands:
//
//	192.168.1.50
//	asiair.local
//	\\192.168.1.50\EMMC Images
//	//192.168.1.50/EMMC Images
//	smb://user:pass@192.168.1.50:445/EMMC Images
//	dir:C:\ASIAIR   (a local or already mounted folder; used for tests too)
//	usb:E:\         (the ASIAIR USB stick plugged into this computer)
//	usb:            (the same, on whatever removable disk is inserted)
func ParseLink(link string) (*Link, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, errors.New("пустая ссылка на ASIAIR")
	}
	if strings.HasPrefix(strings.ToLower(link), "dir:") {
		return &Link{Kind: "dir", Dir: link[4:]}, nil
	}
	if strings.HasPrefix(strings.ToLower(link), "usb:") {
		return &Link{Kind: "usb", Dir: strings.TrimSpace(link[4:])}, nil
	}
	l := &Link{Kind: "smb"}
	switch {
	case strings.HasPrefix(strings.ToLower(link), "smb://"):
		u, err := url.Parse(link)
		if err != nil {
			return nil, fmt.Errorf("неверная ссылка %q: %w", link, err)
		}
		l.Host = u.Host
		if u.User != nil {
			l.Username = u.User.Username()
			l.Password, _ = u.User.Password()
		}
		if s := strings.Trim(u.Path, "/"); s != "" {
			l.Shares = []string{strings.SplitN(s, "/", 2)[0]}
		}
	case strings.HasPrefix(link, `\\`) || strings.HasPrefix(link, "//"):
		rest := strings.ReplaceAll(link[2:], `\`, "/")
		parts := strings.SplitN(strings.Trim(rest, "/"), "/", 3)
		l.Host = parts[0]
		if len(parts) > 1 && parts[1] != "" {
			l.Shares = []string{parts[1]}
		}
	default:
		if strings.ContainsAny(link, `/\`) {
			return nil, fmt.Errorf("не понимаю ссылку %q: ожидается IP, \\\\IP\\шара или smb://IP/шара", link)
		}
		l.Host = link
	}
	if l.Host == "" {
		return nil, fmt.Errorf("в ссылке %q нет адреса ASIAIR", link)
	}
	return l, nil
}

// SplitPath splits "<share>/<rest>" into its parts.
func SplitPath(p string) (share, rest string, err error) {
	p = strings.TrimLeft(strings.ReplaceAll(p, `\`, "/"), "/")
	share, rest, ok := strings.Cut(p, "/")
	if !ok || share == "" || rest == "" {
		return "", "", fmt.Errorf("неверный путь %q", p)
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return "", "", fmt.Errorf("недопустимый путь %q", p)
		}
	}
	return share, rest, nil
}

// ReplaceHost returns the link with the device address replaced by ip,
// keeping the share, the port and the login. Links to folders ("dir:",
// "usb:") are returned unchanged.
func ReplaceHost(link, ip string) (string, error) {
	l, err := ParseLink(link)
	if err != nil {
		return "", err
	}
	if l.Kind != "smb" {
		return link, nil
	}
	host := ip
	if _, port, err := net.SplitHostPort(l.Host); err == nil {
		host = net.JoinHostPort(ip, port)
	}
	link = strings.TrimSpace(link)
	i := strings.Index(link, l.Host)
	if i < 0 {
		return "", fmt.Errorf("не удалось заменить адрес в %q", link)
	}
	return link[:i] + host + link[i+len(l.Host):], nil
}
