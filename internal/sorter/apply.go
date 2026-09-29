package sorter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Mode is how frames get into the output.
type Mode int

const (
	Copy Mode = iota // the originals stay
	Move             // the originals are moved
	Link             // hard links: no extra space, the output must be on the same disk
)

// ModeNames in the order of the modes, for the window.
var ModeNames = []string{"Копировать", "Перемещать", "Жёсткие ссылки (без копирования, тот же диск)"}

// Options of Apply.
type Options struct {
	Mode Mode
	// FillCalibration puts the matched darks, flats and biases into the
	// darks/flats/biases folders of every session (copies, or hard links in
	// Link mode), so Siril's script can run there right away.
	FillCalibration bool
}

// Result counts what Apply did.
type Result struct {
	Placed, Skipped, Calibration int
	CalibrationThere             int   // calibration already in the session folders
	Sessions                     int   // session folders renamed to a new layout
	Moved                        int   // library frames moved to a new layout
	Downloaded                   int64 // bytes fetched from a receiver
	Errors                       []error
}

// Progress of Apply.
type Progress struct {
	Done, Total int    // files
	Name        string // the file being placed
	// bytes to download from a receiver: done and in all (0 without one)
	Bytes, TotalBytes int64
}

// CalibrationFile is the note written into every session folder.
const CalibrationFile = "калибровка.txt"

// Missing tells how many frames of a receiver are not in out yet and how
// many bytes they take (a download begun before counts as fetched).
func (l *Layout) Missing(out string) (files int, bytes int64) {
	for _, it := range l.Items {
		if it.Dup || it.Frame.Src == nil {
			continue
		}
		if st, err := os.Stat(it.Existing); it.Existing != "" && err == nil && st.Size() == it.Frame.Size {
			continue // moved from the old place, not downloaded
		}
		dst := filepath.Join(out, filepath.FromSlash(it.Dest))
		if st, err := os.Stat(dst); err == nil && st.Size() == it.Frame.Size {
			continue
		}
		files++
		bytes += it.Frame.Size
		if st, err := os.Stat(dst + PartSuffix); err == nil && st.Size() <= it.Frame.Size {
			bytes -= st.Size()
		}
	}
	return files, bytes
}

// Apply lays the frames out under out: files on disk are copied, moved or
// linked, frames of a receiver downloaded (only the missing ones, resuming
// a download that stopped). Cancelling ctx stops it early.
func (l *Layout) Apply(ctx context.Context, out string, opt Options, progress func(Progress)) Result {
	var r Result
	var p Progress
	p.Total = len(l.Items)
	if opt.FillCalibration {
		for _, s := range l.Sessions {
			for _, g := range []*Group{s.Darks, s.Flats, s.Biases} {
				if g != nil {
					p.Total += g.Count()
				}
			}
		}
	}
	_, p.TotalBytes = l.Missing(out)
	report := func() {
		if progress != nil {
			progress(p)
		}
	}
	step := func(name string) {
		p.Done++
		p.Name = name
		report()
	}
	fail := func(err error) { r.Errors = append(r.Errors, err) }

	// sessions laid out under an earlier name move whole, with their
	// calibration and Siril's results; before the new folders are made
	for _, err := range l.moveSessions(out, &r) {
		fail(err)
	}

	for _, g := range l.Groups {
		dirs := []string{g.Dir}
		if g.Kind == Light {
			dirs = nil
			for _, d := range SessionDirs {
				dirs = append(dirs, g.Dir+"/"+d)
			}
		}
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(out, filepath.FromSlash(d)), 0o755); err != nil {
				fail(err)
			}
		}
	}

	// where each placed frame is now, for filling the sessions
	now := map[*Frame]string{}
	emptied := map[string]bool{} // folders frames were moved out of
	defer removeEmpty(emptied, out)
	for _, it := range l.Items {
		if ctx.Err() != nil {
			return r
		}
		if it.Dup {
			step(it.Frame.Rel)
			continue
		}
		dst := filepath.Join(out, filepath.FromSlash(it.Dest))
		var placed bool
		var err error
		if moveFrom := libraryMove(it, dst); moveFrom != "" {
			// the library of an earlier layout: move the frame to its new folder
			placed, err = place(moveFrom, dst, Move)
			if placed {
				r.Moved++
				emptied[filepath.Dir(moveFrom)] = true
			}
		} else if f := it.Frame; f.Src != nil {
			p.Name = f.Rel
			base := p.Bytes
			var got int64
			placed, got, err = fetch(ctx, f, dst, func(n int64) {
				p.Bytes = base + n
				report()
			})
			p.Bytes = base + got
			r.Downloaded += got
		} else {
			placed, err = place(it.Frame.Path, dst, opt.Mode)
		}
		switch {
		case err != nil:
			fail(fmt.Errorf("%s: %w", it.Frame.Rel, err))
		case placed:
			r.Placed++
			now[it.Frame] = dst
		default:
			r.Skipped++
			now[it.Frame] = dst
		}
		step(it.Frame.Rel)
	}

	for _, s := range l.Sessions {
		dir := filepath.Join(out, filepath.FromSlash(s.Dir))
		if err := os.WriteFile(filepath.Join(dir, CalibrationFile), []byte(s.CalibrationText()), 0o644); err != nil {
			fail(err)
		}
		if !opt.FillCalibration {
			continue
		}
		mode := Copy
		if opt.Mode == Link {
			mode = Link
		}
		for _, c := range []struct {
			sub string
			g   *Group
		}{{"darks", s.Darks}, {"flats", s.Flats}, {"biases", s.Biases}} {
			if c.g == nil {
				continue
			}
			for _, it := range c.g.Items {
				if it.Dup {
					continue
				}
				if ctx.Err() != nil {
					return r
				}
				src := now[it.Frame]
				if src == "" {
					if it.Frame.Src != nil {
						step(it.Frame.Rel) // not downloaded: nothing to put in
						continue
					}
					src = it.Frame.Path
				}
				placed, err := place(src, filepath.Join(dir, c.sub, filepath.Base(it.Dest)), mode)
				if err != nil {
					fail(fmt.Errorf("%s: %w", it.Frame.Rel, err))
				} else if placed {
					r.Calibration++
				} else {
					r.CalibrationThere++
				}
				step(it.Frame.Rel)
			}
		}
	}
	return r
}

// moveSessions renames the session folders laid out under an earlier
// name (before the camera was part of it) to their new names, with
// everything in them. A folder whose lights now belong to several sessions
// (two cameras) is not renamed: its lights move one by one.
func (l *Layout) moveSessions(out string, r *Result) []error {
	targets := map[string]map[string]bool{} // old session folder -> new ones
	for _, it := range l.Items {
		f := it.Frame
		if !f.InLibrary || f.Kind != Light || filepath.Base(filepath.Dir(f.Path)) != "lights" {
			continue
		}
		old := filepath.Dir(filepath.Dir(f.Path))
		if targets[old] == nil {
			targets[old] = map[string]bool{}
		}
		targets[old][filepath.Join(out, filepath.FromSlash(it.Group.Dir))] = true
	}
	var errs []error
	for old, set := range targets {
		if len(set) != 1 {
			continue
		}
		var nu string
		for d := range set {
			nu = d
		}
		if samePath(old, nu) {
			continue
		}
		if _, err := os.Stat(nu); err == nil {
			continue // the new one exists: the lights move one by one
		}
		if err := os.MkdirAll(filepath.Dir(nu), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(old, nu); err != nil {
			errs = append(errs, fmt.Errorf("папка сессии %s: %w", old, err))
			continue
		}
		r.Sessions++
		os.Remove(filepath.Dir(old)) // the object's folder, when left empty
		prefix := old + string(filepath.Separator)
		for _, it := range l.Items {
			if strings.HasPrefix(it.Frame.Path, prefix) {
				it.Frame.Path = nu + it.Frame.Path[len(old):]
			}
			if strings.HasPrefix(it.Existing, prefix) {
				it.Existing = nu + it.Existing[len(old):]
			}
		}
	}
	return errs
}

// libraryMove tells the frame in the output's library to move to dst,
// "" when there is none or it is there already.
func libraryMove(it *Item, dst string) string {
	from := it.Existing
	if it.Frame.InLibrary {
		from = it.Frame.Path
	}
	if from == "" || samePath(from, dst) {
		return ""
	}
	if _, err := os.Stat(dst); err == nil {
		return "" // placed already; the old copy stays for the user to see
	}
	return from
}

// removeEmpty deletes the folders left empty by moving frames out, and
// their parents up to (not including) top.
func removeEmpty(dirs map[string]bool, top string) {
	for d := range dirs {
		for ; Inside(d, top) && !samePath(d, top); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
}

// fetch downloads a frame of a receiver to dst unless it is there already.
// It returns how many bytes came in this time.
func fetch(ctx context.Context, f *Frame, dst string, progress func(int64)) (placed bool, got int64, err error) {
	if st, err := os.Stat(dst); err == nil {
		if st.Size() == f.Size {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("в %s уже есть другой файл с таким именем", dst)
	}
	var start int64
	if st, err := os.Stat(dst + PartSuffix); err == nil && st.Size() <= f.Size {
		start = st.Size()
	}
	var last int64
	err = f.Src.Download(ctx, f.Path, dst, f.Size, f.MTime, func(have int64) {
		if have < start { // the receiver sent the whole file again
			start = 0
		}
		last = have - start
		progress(last)
	})
	return err == nil, last, err
}

// place puts src at dst. It does nothing (false) when src already is dst,
// or dst holds a file of the same size.
func place(src, dst string, mode Mode) (bool, error) {
	if samePath(src, dst) {
		return false, nil
	}
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	if di, err := os.Stat(dst); err == nil {
		if os.SameFile(si, di) || di.Size() == si.Size() {
			return false, nil // placed before; in Move mode the original stays
		}
		return false, fmt.Errorf("в %s уже есть другой файл с таким именем", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	switch mode {
	case Move:
		if os.Rename(src, dst) == nil {
			return true, nil
		}
		// another disk: copy, then remove the original
		if err := copyFile(src, dst, si); err != nil {
			return false, err
		}
		return true, os.Remove(src)
	case Link:
		if os.Link(src, dst) == nil {
			return true, nil
		}
		// another disk or no hard links there: copy
	}
	return true, copyFile(src, dst, si)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// copyFile copies through a temporary name, so a stopped copy leaves no
// half file that would look placed, and keeps the file time.
func copyFile(src, dst string, si os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(dst, si.ModTime(), si.ModTime())
	return nil
}

// ErrSame: the output would be the input.
var ErrSame = errors.New("входной и выходной каталог совпадают")

// CheckDirs tells when the folders cannot be used together.
func CheckDirs(in, out string) error {
	if in == "" || out == "" {
		return errors.New("укажите, откуда брать кадры и выходной каталог")
	}
	if !IsURL(in) && samePath(in, out) {
		return ErrSame
	}
	return nil
}

// Inside tells whether dir is p or inside it.
func Inside(dir, p string) bool {
	rel, err := filepath.Rel(p, dir)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
