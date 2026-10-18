package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
)

// The window comes back as it was left: its place and size (maximized or
// not), the split between the table and the preview, the tab, the width of
// each column. walk keeps the first three through its settings, given here
// a store inside settings.json; the columns are kept by their titles.

// uiStore is walk's settings kept in settings.json (key: the path of a
// window, value: its state).
type uiStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newUIStore(m map[string]string) *uiStore {
	c := map[string]string{}
	for k, v := range m {
		c[k] = v
	}
	return &uiStore{m: c}
}

func (u *uiStore) Get(key string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.m[key]
	return v, ok
}

func (u *uiStore) Timestamp(string) (time.Time, bool) { return time.Now(), true }

func (u *uiStore) Put(key, value string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.m[key] = notMinimized(value)
	return nil
}

func (u *uiStore) PutExpiring(key, value string) error { return u.Put(key, value) }

func (u *uiStore) Remove(key string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.m, key)
	return nil
}

func (u *uiStore) ExpireDuration() time.Duration   { return 0 }
func (u *uiStore) SetExpireDuration(time.Duration) {}
func (u *uiStore) Load() error                     { return nil }
func (u *uiStore) Save() error                     { return nil }

func (u *uiStore) all() map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := map[string]string{}
	for k, v := range u.m {
		c[k] = v
	}
	return c
}

// notMinimized: a window closed while minimized (from the taskbar) opens
// as it was before, not minimized. The state of a form is "flags showCmd
// …" of its WINDOWPLACEMENT.
func notMinimized(state string) string {
	f := strings.Fields(state)
	if len(f) != 10 {
		return state
	}
	const restoreToMaximized = 2 // WPF_RESTORETOMAXIMIZED
	flags, err1 := strconv.Atoi(f[0])
	show, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return state
	}
	switch show {
	case 2, 6, 7: // SW_SHOWMINIMIZED, SW_MINIMIZE, SW_SHOWMINNOACTIVE
		show = 1 // SW_SHOWNORMAL
		if flags&restoreToMaximized != 0 {
			show = 3 // SW_SHOWMAXIMIZED
		}
		f[1] = strconv.Itoa(show)
	}
	return strings.Join(f, " ")
}

// colState is a column as it was left.
type colState struct {
	Title  string `json:"title"`
	Width  int    `json:"width"`
	Hidden bool   `json:"hidden,omitempty"`
}

// keepColumns remembers the columns of a table (when the window closes).
func (w *window) keepColumns(name string, tv *walk.TableView) {
	if tv == nil {
		return
	}
	var cols []colState
	for i := 0; i < tv.Columns().Len(); i++ {
		c := tv.Columns().At(i)
		cols = append(cols, colState{Title: c.Title(), Width: c.Width(), Hidden: !c.Visible()})
	}
	w.mu.Lock()
	if w.saved.Columns == nil {
		w.saved.Columns = map[string][]colState{}
	}
	w.saved.Columns[name] = cols
	w.mu.Unlock()
}

// restoreColumns gives a table the widths kept; a column added or renamed
// since keeps its default.
func (w *window) restoreColumns(name string, tv *walk.TableView) {
	w.mu.Lock()
	kept := w.saved.Columns[name]
	w.mu.Unlock()
	byTitle := map[string]colState{}
	for _, c := range kept {
		byTitle[c.Title] = c
	}
	for i := 0; i < tv.Columns().Len(); i++ {
		c := tv.Columns().At(i)
		k, ok := byTitle[c.Title()]
		if !ok {
			continue
		}
		if k.Width >= 20 && k.Width <= 2000 {
			c.SetWidth(k.Width)
		}
		if k.Hidden {
			c.SetVisible(false)
		}
	}
}

// keptTables: the tables whose columns are kept, by name.
func (w *window) keptTables() map[string]*walk.TableView {
	m := map[string]*walk.TableView{"frames": w.table, "nights": w.nightTable}
	if w.pl != nil {
		m["targets"], m["steps"] = w.pl.targetTable, w.pl.stepTable
	}
	return m
}
