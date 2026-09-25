package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoUSB means no removable disk is inserted.
var ErrNoUSB = errors.New("флешка не найдена: вставьте флешку из ASIAIR в этот компьютер")

// Dir is a Source backed by a local (or already mounted) folder. The folder
// name is used as the share name, so "dir:Z:\" mapped from
// \\asiair\EMMC Images produces the same layout as the SMB source.
type Dir struct {
	root  string // "" = the first removable disk found at the time of use
	share string
}

func NewDir(root string) *Dir {
	root = filepath.Clean(root)
	share := filepath.Base(root)
	if share == "." || share == string(filepath.Separator) || strings.HasSuffix(share, ":") || share == "" {
		share = DefaultShare
	}
	return &Dir{root: root, share: share}
}

// NewUSB reads the ASIAIR USB stick plugged into this computer. Its files
// get the paths of the ASIAIR "Udisk" share. An empty root means whatever
// removable disk is inserted, so the drive letter may change.
func NewUSB(root string) *Dir {
	if root != "" {
		root = filepath.Clean(root)
		if strings.HasSuffix(root, ":") {
			root += string(filepath.Separator)
		}
	}
	return &Dir{root: root, share: USBShare}
}

// base returns the folder to read.
func (d *Dir) base() (string, error) {
	if d.root != "" {
		return d.root, nil
	}
	drives := RemovableDrives()
	if len(drives) == 0 {
		return "", ErrNoUSB
	}
	return drives[0].Root, nil
}

func (d *Dir) local(p string) (string, error) {
	share, rest, err := SplitPath(p)
	if err != nil {
		return "", err
	}
	if share != d.share {
		return "", fs.ErrNotExist
	}
	root, err := d.base()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(rest)), nil
}

// systemDir reports folders Windows keeps on every disk.
func systemDir(name string) bool {
	return strings.HasPrefix(name, "$") || strings.EqualFold(name, "System Volume Information")
}

func (d *Dir) Walk(ctx context.Context, skipDir func(string) bool, fn func(File) error) error {
	root, err := d.base()
	if err != nil {
		return err
	}
	if _, err := os.Stat(root); err != nil {
		if d.share == USBShare {
			return fmt.Errorf("%w (%v)", ErrNoUSB, err)
		}
		return err
	}
	return filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			// e.g. a folder without read access: skip it, keep the rest
			log.Printf("Пропуск %s: %v", p, err)
			if e != nil && e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		full := d.share + "/" + filepath.ToSlash(rel)
		if e.IsDir() {
			if systemDir(e.Name()) || (skipDir != nil && skipDir(full)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		return fn(File{Path: full, Size: info.Size(), ModTime: info.ModTime()})
	})
}

func (d *Dir) Open(p string) (io.ReadSeekCloser, error) {
	lp, err := d.local(p)
	if err != nil {
		return nil, err
	}
	return os.Open(lp)
}

func (d *Dir) Remove(p string) error {
	lp, err := d.local(p)
	if err != nil {
		return err
	}
	return os.Remove(lp)
}

func (d *Dir) RemoveAll(p string) error {
	lp, err := d.local(p)
	if err != nil {
		return err
	}
	return os.RemoveAll(lp)
}

func (d *Dir) Close() error { return nil }

func (d *Dir) Probe(ctx context.Context) (*ProbeResult, error) {
	root, err := d.base()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if d.share == USBShare {
			return nil, fmt.Errorf("%w (%v)", ErrNoUSB, err)
		}
		return nil, err
	}
	login := "локальная папка"
	if d.share == USBShare {
		login = "флешка " + root
	}
	return &ProbeResult{Login: login, Shares: []ShareProbe{{Name: d.share, Entries: len(entries)}}}, nil
}

// New creates the Source described by the link.
func New(link string, defaultShares []string, user, password, domain string) (Source, error) {
	l, err := ParseLink(link)
	if err != nil {
		return nil, err
	}
	switch l.Kind {
	case "dir":
		return NewDir(l.Dir), nil
	case "usb":
		return NewUSB(l.Dir), nil
	}
	return NewSMB(l, defaultShares, user, password, domain), nil
}
