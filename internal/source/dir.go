package source

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Dir is a Source backed by a local (or already mounted) folder. The folder
// name is used as the share name, so "dir:Z:\" mapped from
// \\asiair\EMMC Images produces the same layout as the SMB source.
type Dir struct {
	root  string
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

func (d *Dir) local(p string) (string, error) {
	share, rest, err := SplitPath(p)
	if err != nil {
		return "", err
	}
	if share != d.share {
		return "", fs.ErrNotExist
	}
	return filepath.Join(d.root, filepath.FromSlash(rest)), nil
}

func (d *Dir) Walk(ctx context.Context, skipDir func(string) bool, fn func(File) error) error {
	return filepath.WalkDir(d.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == d.root {
			return nil
		}
		rel, _ := filepath.Rel(d.root, p)
		full := d.share + "/" + filepath.ToSlash(rel)
		if e.IsDir() {
			if skipDir != nil && skipDir(full) {
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

func (d *Dir) Close() error { return nil }

// New creates the Source described by the link.
func New(link string, defaultShares []string, user, password, domain string) (Source, error) {
	l, err := ParseLink(link)
	if err != nil {
		return nil, err
	}
	if l.Kind == "dir" {
		return NewDir(l.Dir), nil
	}
	return NewSMB(l, defaultShares, user, password, domain), nil
}
