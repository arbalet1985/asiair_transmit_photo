package main

import (
	"context"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// measureStars measures in the background the stars of the light frames of
// the layout the receiver has not measured (frames on disk) and finds the
// tracks of satellites on them; then it tells the satellites by their
// orbits. The table shows them as they come.
func (w *window) measureStars() {
	if w.measureCancel != nil {
		w.measureCancel()
		w.measureCancel = nil
	}
	l := w.layout
	if l == nil {
		return
	}
	out := w.scannedOut
	frames, paths := l.ToMeasure(out)
	w.measureLeft = len(frames)
	if len(frames) == 0 {
		return
	}
	if w.qualityCache == nil {
		w.qualityCache = sorter.LoadQualityCache(filepath.Join(configDir(), "stars.json"))
	}
	cache := w.qualityCache
	site := w.satSite()
	cat := satCatalog()
	w.mu.Lock()
	cat.STUser, cat.STPass = w.saved.SpaceTrackUser, config.Unseal(w.saved.SpaceTrackPass)
	w.mu.Unlock()
	for _, it := range l.Items {
		if it.Frame.Src != nil {
			cat.Source = it.Frame.Src // the catalogues of orbits the receiver gathered
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.measureCancel = cancel
	total := len(frames)
	var mu sync.Mutex
	var pending, all []sorter.Measurement
	seen := map[*sorter.Frame]bool{} // measured
	finished := false
	identifying := false // the stars are measured, the satellites are told
	// apply shows the measurements so far, on the window's thread
	apply := func(final bool, report string) {
		mu.Lock()
		got := pending
		pending = nil
		mu.Unlock()
		w.mw.Synchronize(func() {
			if w.layout != l || finished {
				return // scanned again meanwhile, or a late tick
			}
			changed := map[*sorter.Frame]bool{}
			for _, m := range got {
				m.Apply()
				changed[m.Frame], seen[m.Frame] = true, true
			}
			done := len(seen)
			w.measureLeft = total - done
			w.model.measured(changed)
			w.tracksChanged(changed)
			switch {
			case final:
				finished = true
				w.measureLeft = 0
				w.measureCancel = nil
				t := fmt.Sprintf("Звёзды оценены на кадрах: %d из %d (HFR и «Звёзд» в таблице).", done, total)
				if report != "" {
					t += " " + report
				}
				w.setStatus(t)
			case w.busy:
			case identifying:
				w.setStatus(fmt.Sprintf("Звёзды оценены на кадрах: %d из %d. Опознаю спутники по трекам на кадрах…", done, total))
			default:
				w.setStatus(fmt.Sprintf("Оцениваю звёзды и ищу треки спутников на кадрах: %d из %d…", done, total))
			}
		})
	}
	go func() {
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				log.Printf("Оценка звёзд прервана: %v", p)
			}
		}()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-tick.C:
					apply(false, "")
				case <-stop:
					return
				}
			}
		}()
		setDarks(l, out)
		sorter.MeasureFrames(ctx, frames, paths, cache, &hotPixels, func(m sorter.Measurement) {
			mu.Lock()
			pending = append(pending, m)
			all = append(all, m)
			mu.Unlock()
		})
		var rep sorter.SatReport
		if ctx.Err() == nil {
			apply(false, "") // the stars first: the satellites may wait for the catalogue
			w.mw.Synchronize(func() { identifying = true })
			rep = sorter.IdentifySatellites(ctx, all, cache, cat, site, func(m sorter.Measurement) {
				mu.Lock()
				pending = append(pending, m)
				mu.Unlock()
			})
			if rep.CatalogErr != "" {
				log.Printf("Каталог орбит спутников: %s", rep.CatalogErr)
			}
		}
		close(stop)
		if err := cache.Save(); err != nil {
			log.Printf("Оценки звёзд не сохранены: %v", err)
		}
		if ctx.Err() == nil {
			apply(true, rep.Text())
		}
	}()
}

// bestSession is a line of the dialog: a session (night) of the object
// with the filter, its folders of every exposure and camera together.
type bestSession struct {
	n                int
	night            string
	frames, measured int
	hfr              float64 // median of the frames measured
	on               bool
}

type bestModel struct {
	walk.TableModelBase
	rows    []*bestSession
	changed func() // a session was checked or unchecked
}

func (m *bestModel) RowCount() int { return len(m.rows) }

func (m *bestModel) Value(row, col int) interface{} {
	r := m.rows[row]
	switch col {
	case 0:
		return strconv.Itoa(r.n)
	case 1:
		return sorter.NightLabel(r.night)
	case 2:
		return strconv.Itoa(r.frames)
	case 3:
		return strconv.Itoa(r.measured)
	case 4:
		if r.measured == 0 {
			return ""
		}
		return strings.Replace(fmt.Sprintf("%.2f", r.hfr), ".", ",", 1)
	}
	return ""
}

func (m *bestModel) Checked(row int) bool { return m.rows[row].on }

func (m *bestModel) SetChecked(row int, checked bool) error {
	m.rows[row].on = checked
	if m.changed != nil {
		m.changed()
	}
	return nil
}

// bestSessions sums the sessions of the object with the filter by number.
func bestSessions(l *sorter.Layout, object, filter string) []*bestSession {
	by := map[int]*bestSession{}
	hfrs := map[int][]float64{}
	for _, g := range l.BestSessions(object, filter) {
		s := by[g.Session]
		if s == nil {
			s = &bestSession{n: g.Session, night: g.Night, on: true}
			by[g.Session] = s
		}
		for _, it := range g.Items {
			if it.Dup {
				continue
			}
			s.frames++
			if f := it.Frame; f.Measured && f.Stars > 0 {
				s.measured++
				hfrs[g.Session] = append(hfrs[g.Session], f.HFR)
			}
		}
	}
	var out []*bestSession
	for n, s := range by {
		if h := hfrs[n]; len(h) > 0 {
			sort.Float64s(h)
			s.hfr = h[len(h)/2]
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	return out
}

// bestObjects: the object folders with sessions, and the filters of each.
func bestObjects(l *sorter.Layout) (objects []string, filters map[string][]string) {
	filters = map[string][]string{}
	seen := map[string]bool{}
	for _, g := range l.Sessions {
		o := g.ObjectDir()
		if _, ok := filters[o]; !ok {
			objects = append(objects, o)
			filters[o] = nil
		}
		if k := o + "\x00" + strings.ToLower(g.Filter); !seen[k] {
			seen[k] = true
			filters[o] = append(filters[o], g.Filter)
		}
	}
	sort.Slice(objects, func(i, j int) bool { return sorter.NaturalLess(path.Base(objects[i]), path.Base(objects[j])) })
	for _, fs := range filters {
		sort.Strings(fs)
	}
	return objects, filters
}

func filterLabel(f string) string {
	if f == "" {
		return "без фильтра"
	}
	return f
}

// bestDialog: pick the best frames of an object into Stars.
func (w *window) bestDialog() {
	l, out := w.layout, w.scannedOut
	if l == nil || out == "" {
		w.warn("Сначала «Просмотреть кадры».")
		return
	}
	objects, filters := bestObjects(l)
	if len(objects) == 0 {
		w.warn("Нет сессий с Light-кадрами.")
		return
	}
	names := make([]string, len(objects))
	cur := 0
	chosen := sorter.ObjectsMatching(w.objList, strings.TrimSpace(w.objBox.Text()))
	for i, o := range objects {
		names[i] = path.Base(o)
		if len(chosen) == 1 && sorter.NormObject(chosen[0]) == sorter.NormObject(names[i]) {
			cur = i
		}
	}
	w.mu.Lock()
	percent := w.saved.BestPercent
	roundOn, minRound := w.saved.BestRoundOn, w.saved.BestMinRound
	satOn, maxSat := w.saved.BestSatOn, w.saved.BestMaxSat
	w.mu.Unlock()
	if percent <= 0 || percent > 100 {
		percent = 30
	}
	if minRound <= 0 || minRound >= 1 {
		minRound = 0.8
	}
	if maxSat <= 0 {
		maxSat = 0.1
	}
	var roundBox, satBox *walk.CheckBox
	var roundEdit, satEdit *walk.NumberEdit

	var dlg *walk.Dialog
	var tv *walk.TableView
	var ok, cancel *walk.PushButton
	var objBox, filterBox *walk.ComboBox
	var pctEdit *walk.NumberEdit
	var info *walk.Label
	m := &bestModel{}
	object, filter := objects[cur], ""
	query := func() sorter.BestQuery {
		q := sorter.BestQuery{Object: object, Filter: filter, Percent: percent}
		if roundOn {
			q.MinRound = minRound
		}
		if satOn {
			q.MaxSatPct = maxSat
		}
		for _, r := range m.rows {
			if r.on {
				q.Sessions = append(q.Sessions, r.n)
			}
		}
		return q
	}
	update := func() {
		if info == nil {
			return
		}
		q := query()
		if len(q.Sessions) == 0 {
			info.SetText("Отметьте хотя бы одну сессию.")
			ok.SetEnabled(false)
			return
		}
		p := l.Best(q)
		t := fmt.Sprintf("Будет взято кадров: %d из %d", len(p.Keep), p.All)
		if p.Unmeasured > 0 {
			t += fmt.Sprintf("; без оценки звёзд (не берутся): %d", p.Unmeasured)
		}
		if p.Elongated > 0 {
			t += fmt.Sprintf("; с вытянутыми звёздами (не берутся): %d", p.Elongated)
		}
		if p.Saturated > 0 {
			t += fmt.Sprintf("; с пересветом (не берутся): %d", p.Saturated)
		}
		if w.measureLeft > 0 {
			t += fmt.Sprintf(". Ещё оцениваются: %d — подождите", w.measureLeft)
		}
		info.SetText(t + ".")
		ok.SetEnabled(len(p.Keep) > 0)
	}
	setSessions := func() {
		m.rows = bestSessions(l, object, filter)
		m.PublishRowsReset()
		update()
	}
	setFilters := func() {
		fs := filters[object]
		labels := make([]string, len(fs))
		for i, f := range fs {
			labels[i] = filterLabel(f)
		}
		filter = fs[0]
		if filterBox != nil {
			filterBox.SetModel(labels)
			filterBox.SetCurrentIndex(0)
		}
		setSessions()
	}
	accept := func() {
		percent = pctEdit.Value()
		w.mu.Lock()
		w.saved.BestPercent = percent
		w.saved.BestRoundOn, w.saved.BestMinRound = roundOn, minRound
		w.saved.BestSatOn, w.saved.BestMaxSat = satOn, maxSat
		w.mu.Unlock()
		dlg.Accept()
	}
	err := Dialog{
		AssignTo:      &dlg,
		Title:         "Лучшие кадры в каталог Stars",
		DefaultButton: &ok,
		CancelButton:  &cancel,
		MinSize:       Size{Width: 620, Height: 480},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Лучшие кадры объекта кладутся в Stars\\<объект>\\<сессия>\\lights — те же папки, что в Objects, с калибровкой,\n" +
				"чтобы сразу запустить скрипт Siril. Оценка как взвешенный FWHM в Siril: мелкие звёзды (HFR) и много звёзд — лучше.\n" +
				"Кадры всех отмеченных сессий оцениваются вместе. Новый отбор объекта и фильтра заменяет прежний (жёсткие ссылки, место не занимают)."},
			Composite{
				Layout: Grid{Columns: 4, MarginsZero: true},
				Children: []Widget{
					Label{Text: "Объект:"},
					ComboBox{AssignTo: &objBox, Model: names, CurrentIndex: cur, MinSize: Size{Width: 200}, OnCurrentIndexChanged: func() {
						if i := objBox.CurrentIndex(); i >= 0 {
							object = objects[i]
							setFilters()
						}
					}},
					Label{Text: "Фильтр:"},
					ComboBox{AssignTo: &filterBox, MinSize: Size{Width: 120}, OnCurrentIndexChanged: func() {
						if i := filterBox.CurrentIndex(); i >= 0 && i < len(filters[object]) {
							filter = filters[object][i]
							setSessions()
						}
					}},
				},
			},
			TableView{AssignTo: &tv, Model: m, CheckBoxes: true, AlternatingRowBG: true, NotSortableByHeaderClick: true,
				Columns: []TableViewColumn{
					{Title: "Сессия", Width: 70},
					{Title: "Ночь", Width: 140},
					{Title: "Кадров", Width: 70, Alignment: AlignFar},
					{Title: "Оценено", Width: 70, Alignment: AlignFar},
					{Title: "HFR, медиана", Width: 100, Alignment: AlignFar},
				}},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "Взять лучших, %:"},
					NumberEdit{AssignTo: &pctEdit, Value: percent, MinValue: 1, MaxValue: 100, Decimals: 0, MaxSize: Size{Width: 60},
						OnValueChanged: func() { percent = pctEdit.Value(); update() }},
					PushButton{Text: "Отметить все", OnClicked: func() {
						for _, r := range m.rows {
							r.on = true
						}
						m.PublishRowsReset()
						update()
					}},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &roundBox, Text: "Не брать кадры с вытянутыми звёздами: округлость ниже", Checked: roundOn,
						ToolTipText:      "Округлость — короткая ось звезды к длинной, как в Siril: 1 — круглые. Вытянутые звёзды — ветер, гидирование, перекладка",
						OnCheckedChanged: func() { roundOn = roundBox.Checked(); update() }},
					NumberEdit{AssignTo: &roundEdit, Value: minRound, MinValue: 0.3, MaxValue: 0.99, Decimals: 2, Increment: 0.05, MaxSize: Size{Width: 60},
						OnValueChanged: func() { minRound = roundEdit.Value(); update() }},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &satBox, Text: "Не брать кадры, где пересвечено больше, % кадра:", Checked: satOn,
						ToolTipText:      "Доля пикселей кадра на уровне насыщения (строка saturated статистики). Единицы пикселей — ядра ярких звёзд, это нормально",
						OnCheckedChanged: func() { satOn = satBox.Checked(); update() }},
					NumberEdit{AssignTo: &satEdit, Value: maxSat, MinValue: 0.001, MaxValue: 100, Decimals: 3, Increment: 0.01, MaxSize: Size{Width: 70},
						OnValueChanged: func() { maxSat = satEdit.Value(); update() }},
					HSpacer{},
				},
			},
			Label{AssignTo: &info},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &ok, Text: "Создать", OnClicked: accept},
					PushButton{AssignTo: &cancel, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(w.mw)
	if err != nil {
		w.warn(err.Error())
		return
	}
	m.changed = update
	copyMenu(tv, nil)
	setFilters()
	if dlg.Run() != walk.DlgCmdOK {
		return
	}
	w.makeBest(l, out, query())
}

// makeBest makes the Stars folders of the pick, in the background.
func (w *window) makeBest(l *sorter.Layout, out string, q sorter.BestQuery) {
	p := l.Best(q)
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.setBusy(true)
	go func() {
		defer cancel()
		var last time.Time
		r := l.ApplyBest(ctx, out, q, p, func(done, total int) {
			if time.Since(last) < 200*time.Millisecond && done != total {
				return
			}
			last = time.Now()
			w.mw.Synchronize(func() {
				w.setProgress(done * 1000 / max(total, 1))
				w.setStatus(fmt.Sprintf("Лучшие кадры: %d из %d", done, total))
			})
		})
		w.mw.Synchronize(func() {
			w.setBusy(false)
			w.setProgress(0)
			dir := filepath.Join(out, filepath.FromSlash(sorter.BestDir(q.Object)))
			msg := fmt.Sprintf("Лучшие кадры %s (%s): %d из %d положены в\n%s", path.Base(q.Object), filterLabel(q.Filter), r.Placed, p.All, dir)
			if r.Calibration > 0 {
				msg += fmt.Sprintf("\nкалибровочных в папках сессий: %d", r.Calibration)
			}
			if r.NotLaidOut > 0 {
				msg += fmt.Sprintf("\n\nЕщё не разложены в выходной каталог: %d — сначала «Разложить», затем отбор снова.", r.NotLaidOut)
			}
			if ctx.Err() != nil {
				msg = "Остановлено. " + msg
			}
			icon := walk.MsgBoxIconInformation
			if len(r.Errors) > 0 {
				icon = walk.MsgBoxIconWarning
				msg += fmt.Sprintf("\n\nОшибок: %d", len(r.Errors))
				for i, e := range r.Errors {
					if i == 10 {
						msg += "\n…"
						break
					}
					msg += "\n" + e.Error()
				}
				for _, e := range r.Errors {
					log.Print(e)
				}
			}
			w.setStatus(strings.SplitN(msg, "\n", 2)[0])
			walk.MsgBox(w.mw, "ASIAIR Sorter", msg, icon)
		})
	}()
}
