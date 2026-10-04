package sorter

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Only some folders of the input can be laid out: the folders chosen, with
// their subfolders. The frames of the other folders are not read (from a
// receiver: not downloaded), and their copies already in the output are not
// taken for frames deleted on the receiver.

// Folder is a folder of the input that holds FITS frames itself.
type Folder struct {
	Dir   string // relative to the input, with /
	Files int
	Bytes int64
}

// Folders is a choice of folders of the input; empty means all of them.
type Folders []string

// Has reports whether the file rel (relative to the input, with / or \)
// lies in one of the folders chosen or below.
func (fs Folders) Has(rel string) bool {
	if len(fs) == 0 {
		return true
	}
	return fs.HasDir(path.Dir(slash(rel)))
}

// HasDir reports whether the folder dir (relative to the input) is one of
// the folders chosen or lies in one.
func (fs Folders) HasDir(dir string) bool {
	if len(fs) == 0 {
		return true
	}
	dir = strings.Trim(slash(dir), "/")
	for _, f := range fs {
		f = strings.Trim(slash(f), "/")
		if strings.EqualFold(dir, f) || len(dir) > len(f) && strings.EqualFold(dir[:len(f)], f) && dir[len(f)] == '/' {
			return true
		}
	}
	return false
}

// below reports whether the folder dir (relative to the input) may hold
// frames of the folders chosen: it is one of them, lies in one, or holds one.
func (fs Folders) below(dir string) bool {
	if len(fs) == 0 || dir == "." || dir == "" {
		return true
	}
	dir = slash(dir)
	for _, f := range fs {
		f = strings.Trim(slash(f), "/")
		a, b := strings.ToLower(dir), strings.ToLower(f)
		if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
			return true
		}
	}
	return false
}

func countFolders(add func(yield func(rel string, size int64))) []Folder {
	by := map[string]*Folder{}
	add(func(rel string, size int64) {
		d := path.Dir(slash(rel))
		f := by[d]
		if f == nil {
			f = &Folder{Dir: d}
			by[d] = f
		}
		f.Files++
		f.Bytes += size
	})
	out := make([]Folder, 0, len(by))
	for _, f := range by {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Dir) < strings.ToLower(out[j].Dir) })
	return out
}

// LocalFolders lists the folders under in that hold FITS files (the output
// folder left out when inside it). Headers are not read.
func LocalFolders(in, out string) ([]Folder, error) {
	var werr error
	list := countFolders(func(yield func(string, int64)) {
		werr = filepath.WalkDir(in, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != in && out != "" && samePath(p, out) {
					return filepath.SkipDir
				}
				return nil
			}
			if !IsFITS(p) {
				return nil
			}
			rel, _ := filepath.Rel(in, p)
			var size int64
			if info, err := d.Info(); err == nil {
				size = info.Size()
			}
			yield(rel, size)
			return nil
		})
	})
	return list, werr
}

// ReceiverFolders lists the folders of the frames received by the receiver.
func ReceiverFolders(ctx context.Context, r *Receiver) ([]Folder, error) {
	files, err := r.Files(ctx)
	if err != nil {
		return nil, err
	}
	return countFolders(func(yield func(string, int64)) {
		for _, f := range files {
			yield(f.Path, f.Size)
		}
	}), nil
}

// ScanForOnly is ScanFor with only the folders chosen of the input read
// (the library of the output is read whole).
func ScanForOnly(in, out string, only Folders, progress func(n int)) ([]Frame, []error) {
	if len(only) == 0 {
		return ScanFor(in, out, progress)
	}
	frames, errs := Scan(in, func(dir string) bool {
		if out != "" && samePath(dir, out) {
			return true
		}
		rel, err := filepath.Rel(in, dir)
		return err == nil && !only.below(rel)
	}, progress)
	kept := frames[:0]
	for _, f := range frames {
		if only.Has(f.Rel) {
			kept = append(kept, f)
		}
	}
	frames = kept
	if out != "" && Inside(in, filepath.Join(out, LibraryDir)) {
		return frames, errs
	}
	base := len(frames)
	more, e := ScanLibrary(out, func(n int) {
		if progress != nil {
			progress(base + n)
		}
	})
	return append(frames, more...), append(errs, e...)
}

// Elsewhere tells the layout of frames of the input left out by a choice
// of folders: their copies in the output are not in Gone.
func (l *Layout) Elsewhere(files []RemoteFile) {
	if l.elsewhere == nil {
		l.elsewhere = map[string]bool{}
	}
	for _, f := range files {
		l.elsewhere[elsewhereKey(path.Base(f.Path), f.Size)] = true
	}
}

func elsewhereKey(name string, size int64) string {
	return strings.ToLower(SafeName(name)) + "|" + strconv.FormatInt(size, 10)
}

// slash writes a relative path with / (a choice saved on Windows has \).
func slash(p string) string { return strings.ReplaceAll(p, `\`, "/") }
