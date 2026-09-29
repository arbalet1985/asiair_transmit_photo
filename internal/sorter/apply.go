package sorter

import (
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
	Errors                       []error
}

// CalibrationFile is the note written into every session folder.
const CalibrationFile = "калибровка.txt"

// Apply lays the frames out under out. progress gets the number of files
// done, their total and the current name; stop, when it returns true, ends
// the work early.
func (l *Layout) Apply(out string, opt Options, progress func(done, total int, name string), stop func() bool) Result {
	var r Result
	total := len(l.Items)
	if opt.FillCalibration {
		for _, s := range l.Sessions {
			for _, g := range []*Group{s.Darks, s.Flats, s.Biases} {
				if g != nil {
					total += g.Count()
				}
			}
		}
	}
	done := 0
	step := func(name string) {
		done++
		if progress != nil {
			progress(done, total, name)
		}
	}
	fail := func(err error) { r.Errors = append(r.Errors, err) }

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
	for _, it := range l.Items {
		if stop != nil && stop() {
			return r
		}
		src := it.Frame.Path
		if it.Dup {
			step(it.Frame.Rel)
			continue
		}
		dst := filepath.Join(out, filepath.FromSlash(it.Dest))
		placed, err := place(src, dst, opt.Mode)
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
				if stop != nil && stop() {
					return r
				}
				src := now[it.Frame]
				if src == "" {
					src = it.Frame.Path
				}
				placed, err := place(src, filepath.Join(dir, c.sub, filepath.Base(it.Dest)), mode)
				if err != nil {
					fail(fmt.Errorf("%s: %w", it.Frame.Rel, err))
				} else if placed {
					r.Calibration++
				}
				step(it.Frame.Rel)
			}
		}
	}
	return r
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
		return errors.New("укажите входной и выходной каталоги")
	}
	if samePath(in, out) {
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
