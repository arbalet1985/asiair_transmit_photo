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
	"net/url"
	"strings"
	"time"
)

// DefaultShare is the ASIAIR internal storage share.
const DefaultShare = "EMMC Images"

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
}

// Link is a parsed ASIAIR link.
type Link struct {
	Kind     string // "smb" or "dir"
	Host     string // host or host:port
	Shares   []string
	Username string
	Password string
	Dir      string // for Kind == "dir"
}

// ParseLink understands:
//
//	192.168.1.50
//	asiair.local
//	\\192.168.1.50\EMMC Images
//	//192.168.1.50/EMMC Images
//	smb://user:pass@192.168.1.50:445/EMMC Images
//	dir:C:\ASIAIR   (a local or already mounted folder; used for tests too)
func ParseLink(link string) (*Link, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, errors.New("пустая ссылка на ASIAIR")
	}
	if strings.HasPrefix(strings.ToLower(link), "dir:") {
		return &Link{Kind: "dir", Dir: link[4:]}, nil
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
