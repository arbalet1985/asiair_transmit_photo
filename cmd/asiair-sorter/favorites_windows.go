package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The favorite objects of the planner, as on the page of the receiver: a
// search with hearts, the altitude of each object through the night with
// its rise, culmination and set as in ASIAIR, the filter of ASIAIR by
// altitude, side of the sky and time, and the best objects of the night.
// The list is the receiver's when one is known: the changes made here go
// to it, later when it cannot be reached.

const maxFavOps = 500

// favFilterSettings is the filter kept.
type favFilterSettings struct {
	On     bool           `json:"on,omitempty"`
	Filter planner.Filter `json:"filter"`         // its From and To are not kept: the times below
	From   string         `json:"from,omitempty"` // "ЧЧ:ММ" on the clock of the observatory; "" = the dark night
	To     string         `json:"to,omitempty"`
}

func defaultFavFilter() favFilterSettings {
	return favFilterSettings{Filter: planner.Filter{AltMin: 30, AltMax: 90}}
}

var favKinds = []struct{ label, kind string }{{"все", ""}, {"туманности", "nebula"}, {"галактики", "galaxy"}, {"скопления", "cluster"}}

func favIndex(l []planner.Favorite, name string) int {
	for i, f := range l {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// toggleFavorite hearts the object or takes the heart away.
func (p *plannerUI) toggleFavorite(o planner.Favorite) {
	if i := favIndex(p.state.Favorites, o.Name); i >= 0 {
		p.state.Favorites = append(p.state.Favorites[:i:i], p.state.Favorites[i+1:]...)
		p.state.FavOps = append(p.state.FavOps, favOp{Remove: o.Name})
	} else {
		p.state.Favorites = append(p.state.Favorites, o)
		p.state.FavOps = append(p.state.FavOps, favOp{Add: &o})
	}
	p.favChanged()
}

// setFavNote sets the note of a favorite.
func (p *plannerUI) setFavNote(name, note string) {
	i := favIndex(p.state.Favorites, name)
	if note = planner.CleanNote(note); i < 0 || p.state.Favorites[i].Note == note {
		return
	}
	p.state.Favorites[i].Note = note
	p.state.FavOps = append(p.state.FavOps, favOp{NoteOf: name, Note: note})
	p.favChanged()
}

func (p *plannerUI) favChanged() {
	if n := len(p.state.FavOps); n > maxFavOps {
		p.state.FavOps = p.state.FavOps[n-maxFavOps:]
	}
	p.w.settings().save()
	p.atlasChanged()
}

// syncFavorites sends the changes made here to the receiver and takes its
// list; after gets what to tell ("" when all went well or no receiver is
// known: then the list is only here).
func (p *plannerUI) syncFavorites(after func(msg string)) {
	addr := p.w.receiverAddr()
	if addr == "" {
		after("")
		return
	}
	if p.favSyncing {
		p.favAgain = true
		return
	}
	p.favSyncing = true
	ops := append([]favOp(nil), p.state.FavOps...)
	go func() {
		var list []planner.Favorite
		var err error
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Избранное с приёмника: сбой: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		r, err := p.w.connect(ctx, addr, "", "")
		if err == nil {
			if len(ops) > 0 {
				add, remove, notes := favOpsSum(ops)
				list, err = r.ChangeFavorites(ctx, add, remove, notes)
			} else {
				list, err = r.Favorites(ctx)
			}
		}
		p.w.mw.Synchronize(func() {
			p.favSyncing = false
			msg := ""
			switch {
			case errors.Is(err, sorter.ErrNoFavorites):
				msg = err.Error() + ": избранное пока хранится только в сортировщике."
			case err != nil:
				msg = "Приёмник не ответил (" + err.Error() + "): изменения избранного уйдут на него позже."
			default:
				p.state.FavOps = p.state.FavOps[min(len(ops), len(p.state.FavOps)):]
				if len(p.state.FavOps) == 0 {
					p.state.Favorites = list
					p.atlasChanged()
				} else {
					p.favAgain = true // changed while it was sent
				}
				p.w.settings().save()
			}
			if p.favAgain && err == nil {
				p.favAgain = false
				p.syncFavorites(after)
				return
			}
			p.favAgain = false
			after(msg)
		})
	}()
}

// favDialog is the window of the favorites.
type favDialog struct {
	p      *plannerUI
	dlg    *walk.Dialog
	query  *walk.LineEdit
	info   *walk.Label
	status *walk.Label
	panel  *walk.Composite // the filter
	rose   *walk.CustomWidget
	list   *walk.CustomWidget

	onBox, openBox       *walk.CheckBox
	altMin, altMax       *walk.NumberEdit
	fromBox, toBox, kind *walk.ComboBox
	tabFound, tabFav     *walk.RadioButton

	f     favFilterSettings
	times []time.Time // of the entries of fromBox and toBox after the first
	zone  *time.Location

	tab      int // 0: the search (or the best), 1: the favorites
	best     bool
	found    []planner.Favorite
	exp      map[string]float64 // the exposure of the objects of the frames
	vis      *planner.Visibility
	visFav   map[string]*planner.Vis
	visFound map[string]*planner.Vis
	rows     []favRow // as drawn
	gen      int
	timer    *time.Timer
	setting  bool // the fields are set by the program
	closed   bool
	// the pictures of the objects (catalog.Thumb) by favPicKey, and the ones
	// asked for
	pics    map[string]*walk.Bitmap
	picsAsk map[string]bool
	// the object in the field of the camera (catalog.View), shown over the
	// list while the mouse is on the small picture of row hover (-1: none)
	scroll   *walk.ScrollView
	hover    int
	views    map[string]*walk.Bitmap
	viewsAsk map[string]bool
	viewsErr map[string]bool
}

type favRow struct {
	o planner.Favorite
	v *planner.Vis
}

func (d *favDialog) dpi() float64 { return float64(d.list.DPI()) / 96 }

// openFavorites shows the window of the favorites.
func (p *plannerUI) openFavorites() {
	d := &favDialog{p: p, f: defaultFavFilter(), exp: map[string]float64{}, pics: map[string]*walk.Bitmap{}, picsAsk: map[string]bool{},
		hover: -1, views: map[string]*walk.Bitmap{}, viewsAsk: map[string]bool{}, viewsErr: map[string]bool{}}
	if p.state.FavFilter != nil {
		d.f = *p.state.FavFilter
	}
	if len(p.state.Favorites) == 0 {
		d.tab = 0
	} else {
		d.tab = 1
	}
	for _, o := range p.frameObjs {
		d.exp[o.name] = o.exp
	}
	var kinds []string
	for _, k := range favKinds {
		kinds = append(kinds, k.label)
	}
	gray := walk.RGB(110, 110, 110)
	var closeBtn *walk.PushButton
	d.setting = true // no handler runs while the window is made
	err := Dialog{
		AssignTo:     &d.dlg,
		Title:        "Избранные объекты",
		CancelButton: &closeBtn,
		MinSize:      Size{Width: 860, Height: 720},
		Layout:       VBox{},
		Children: []Widget{
			Label{AssignTo: &d.info, EllipsisMode: EllipsisEnd},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				LineEdit{AssignTo: &d.query, CueBanner: "M 31, NGC 7000, Медуза…", MinSize: Size{Width: 220},
					OnTextChanged: d.typed,
					OnKeyDown: func(k walk.Key) {
						if k == walk.KeyReturn {
							d.search(true)
						}
					}, ToolTipText: "Название или номер: M 31, NGC 7000, IC 1805, Sh2-274, «Медуза», «Сердце». Enter — искать и в SIMBAD"},
				PushButton{Text: "Найти", OnClicked: func() { d.search(true) }},
				PushButton{Text: "Лучшее этой ночью", OnClicked: d.showBest,
					ToolTipText: "Объекты каталога, которые этой ночью попадают в фильтр: сначала Мессье и известные, дольше всего в фильтре"},
				PushButton{Text: "Фильтр…", OnClicked: func() { d.panel.SetVisible(!d.panel.Visible()) },
					ToolTipText: "Фильтр по высоте, сторонам света и времени, как в ASIAIR"},
				HSpacer{},
			}},
			Composite{AssignTo: &d.panel, Visible: false, Layout: HBox{MarginsZero: true}, Children: []Widget{
				CustomWidget{AssignTo: &d.rose, PaintPixels: d.paintRose, DoubleBuffering: true, InvalidatesOnResize: true,
					MinSize: Size{Width: 180, Height: 180}, MaxSize: Size{Width: 180, Height: 180}, OnMouseDown: d.roseClick,
					ToolTipText: "Стороны света: щелчок по сектору выбирает его или снимает (ни одного — все стороны).\nКольца — высота, в центре зенит; закрашен выбранный диапазон высоты"},
				Composite{Layout: VBox{MarginsZero: true}, Children: []Widget{
					CheckBox{AssignTo: &d.onBox, Text: "фильтр включён", OnCheckedChanged: d.filterChanged},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{Text: "Высота от"},
						NumberEdit{AssignTo: &d.altMin, Decimals: 0, MinValue: 0, MaxValue: 90, Increment: 5, Suffix: "°", SpinButtonsVisible: true,
							MinSize: Size{Width: 70}, MaxSize: Size{Width: 80}, OnValueChanged: d.filterChanged},
						Label{Text: "до"},
						NumberEdit{AssignTo: &d.altMax, Decimals: 0, MinValue: 0, MaxValue: 90, Increment: 5, Suffix: "°", SpinButtonsVisible: true,
							MinSize: Size{Width: 70}, MaxSize: Size{Width: 80}, OnValueChanged: d.filterChanged},
						HSpacer{},
					}},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{Text: "Время с"},
						ComboBox{AssignTo: &d.fromBox, OnCurrentIndexChanged: d.filterChanged},
						Label{Text: "до"},
						ComboBox{AssignTo: &d.toBox, OnCurrentIndexChanged: d.filterChanged},
						HSpacer{},
					}},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{Text: "Тип"},
						ComboBox{AssignTo: &d.kind, Model: kinds, OnCurrentIndexChanged: d.filterChanged},
						CheckBox{AssignTo: &d.openBox, Text: "только открытое небо обсерватории", OnCheckedChanged: d.filterChanged,
							ToolTipText: "Объект ещё и не закрыт горизонтом и зонами планировщика"},
						HSpacer{},
					}},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						PushButton{Text: "Все стороны", OnClicked: func() { d.f.Filter.Dirs = 0; d.turnOn(); d.filterChanged() }},
						PushButton{Text: "Сбросить", OnClicked: func() { d.f = defaultFavFilter(); d.showFilter(); d.filterChanged() }},
						HSpacer{},
					}},
					Label{Text: "Объект проходит фильтр, если в какой-то момент выбранного времени он на этой высоте\nи в выбранной стороне неба. Не прошедшие фильтр — в конце списка, бледные.", TextColor: gray},
				}},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				RadioButton{AssignTo: &d.tabFound, Text: "Поиск", OnClicked: func() { d.setTab(0) }},
				RadioButton{AssignTo: &d.tabFav, Text: "♥ Избранные", OnClicked: func() { d.setTab(1) }},
				HSpacer{},
			}},
			ScrollView{AssignTo: &d.scroll, HorizontalFixed: true, Layout: VBox{MarginsZero: true}, StretchFactor: 1, Children: []Widget{
				CustomWidget{AssignTo: &d.list, PaintPixels: d.paintList, DoubleBuffering: true, InvalidatesOnResize: true,
					OnMouseDown: d.listClick, OnMouseMove: d.listMove},
			}},
			Label{AssignTo: &d.status, EllipsisMode: EllipsisEnd},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "♥ — в избранное или убрать. График — высота от заката до восхода: серое — тёмная ночь, зелёное — объект открыт телескопу, красная черта — сейчас.",
					TextColor: gray, EllipsisMode: EllipsisEnd},
				HSpacer{},
				PushButton{AssignTo: &closeBtn, Text: "Закрыть", OnClicked: func() { d.dlg.Cancel() }},
			}},
		},
	}.Create(p.w.mw)
	if err != nil {
		p.w.warn(err.Error())
		return
	}
	d.zone = p.params().Zone
	d.fillTimes()
	d.showFilter()
	d.setting = false
	d.setTab(d.tab)
	d.compute()
	setLong(d.status, "")
	p.syncFavorites(func(msg string) {
		if d.closed {
			return
		}
		setLong(d.status, msg)
		d.compute()
	})
	d.dlg.Run()
	d.closed = true
	if d.timer != nil {
		d.timer.Stop()
	}
	for _, b := range d.views {
		b.Dispose()
	}
	for _, b := range d.pics {
		b.Dispose()
	}
}

func favPicKey(o planner.Favorite) string {
	return fmt.Sprintf("%.4f %.4f %.1f %s", o.RA, o.Dec, o.Size, o.Name)
}

// pic is the picture of the object, asked for in the background when not
// there yet (nil until it comes).
func (d *favDialog) pic(o planner.Favorite) *walk.Bitmap {
	key := favPicKey(o)
	if b := d.pics[key]; b != nil || d.picsAsk[key] {
		return b
	}
	d.picsAsk[key] = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Картинка объекта: сбой: %v", r)
			}
		}()
		size := o.Size
		if size <= 0 {
			size = catalog.SizeOf(o.Name)
		}
		b, err := catalog.Thumb(context.Background(), filepath.Join(configDir(), "thumbs"), o.RA, o.Dec, catalog.ThumbFOV(size))
		if err != nil {
			return
		}
		im, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return
		}
		d.p.w.mw.Synchronize(func() {
			if d.closed {
				return
			}
			if bmp, err := walk.NewBitmapFromImageForDPI(im, 96); err == nil {
				d.pics[key] = bmp
				d.list.Invalidate()
			}
		})
	}()
	return nil
}

// fillTimes: the times of the filter, every 30 minutes from the sunset to
// the sunrise of the night chosen in the planner.
func (d *favDialog) fillTimes() {
	prm := d.p.params()
	from, to, err := planner.Night(prm.Night, prm.Lat, prm.Lon, -0.833)
	d.times = nil
	if err == nil {
		for t := from.Truncate(30 * time.Minute).Add(30 * time.Minute); !t.After(to); t = t.Add(30 * time.Minute) {
			d.times = append(d.times, t)
		}
	}
	model := func(first string) []string {
		l := []string{first}
		for _, t := range d.times {
			l = append(l, t.In(d.zone).Format("15:04"))
		}
		return l
	}
	was := d.setting
	d.setting = true
	d.fromBox.SetModel(model("начала тёмной ночи"))
	d.toBox.SetModel(model("конца тёмной ночи"))
	d.setting = was
}

// timeIndex: the entry of the time kept ("ЧЧ:ММ"), 0 when not in the night.
func (d *favDialog) timeIndex(hm string) int {
	for i, t := range d.times {
		if hm != "" && t.In(d.zone).Format("15:04") == hm {
			return i + 1
		}
	}
	return 0
}

// showFilter puts the filter into the fields.
func (d *favDialog) showFilter() {
	was := d.setting
	d.setting = true
	defer func() { d.setting = was }()
	d.onBox.SetChecked(d.f.On)
	d.altMin.SetValue(d.f.Filter.AltMin)
	d.altMax.SetValue(d.f.Filter.AltMax)
	d.fromBox.SetCurrentIndex(d.timeIndex(d.f.From))
	d.toBox.SetCurrentIndex(d.timeIndex(d.f.To))
	k := 0
	for i, x := range favKinds {
		if x.kind == d.f.Filter.Kind {
			k = i
		}
	}
	d.kind.SetCurrentIndex(k)
	d.openBox.SetChecked(d.f.Filter.Open)
	d.rose.Invalidate()
}

// filterChanged reads the fields: a change of any turns the filter on.
func (d *favDialog) filterChanged() {
	if d.setting {
		return
	}
	on := d.onBox.Checked()
	f := &d.f
	old := *f
	f.Filter.AltMin, f.Filter.AltMax = d.altMin.Value(), d.altMax.Value()
	hm := func(b *walk.ComboBox) string {
		if i := b.CurrentIndex(); i > 0 && i <= len(d.times) {
			return d.times[i-1].In(d.zone).Format("15:04")
		}
		return ""
	}
	f.From, f.To = hm(d.fromBox), hm(d.toBox)
	if i := d.kind.CurrentIndex(); i >= 0 && i < len(favKinds) {
		f.Filter.Kind = favKinds[i].kind
	}
	f.Filter.Open = d.openBox.Checked()
	f.On = on
	// the box itself turns it off; a change of the rest turns it on
	if on == old.On && *f != old {
		f.On = true
	}
	if f.On != on {
		d.setting = true
		d.onBox.SetChecked(f.On)
		d.setting = false
	}
	kept := *f
	d.p.state.FavFilter = &kept
	d.rose.Invalidate()
	d.compute()
}

// turnOn checks «фильтр включён» before a change made outside the fields.
func (d *favDialog) turnOn() {
	d.setting = true
	d.onBox.SetChecked(true)
	d.setting = false
}

// filter is the filter for the planner (nil: off).
func (d *favDialog) filter() *planner.Filter {
	if !d.f.On {
		return nil
	}
	f := d.f.Filter
	if f.AltMin > f.AltMax {
		f.AltMin, f.AltMax = f.AltMax, f.AltMin
	}
	f.From, f.To = 0, 0
	if i := d.timeIndex(d.f.From); i > 0 {
		f.From = d.times[i-1].UnixMilli()
	}
	if i := d.timeIndex(d.f.To); i > 0 {
		f.To = d.times[i-1].UnixMilli()
	}
	return &f
}

func (d *favDialog) setTab(t int) {
	d.tab = t
	d.tabFound.SetChecked(t == 0)
	d.tabFav.SetChecked(t == 1)
	d.render()
}

// typed: the search a moment after the last key.
func (d *favDialog) typed() {
	if d.setting {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(300*time.Millisecond, func() {
		d.p.w.mw.Synchronize(func() {
			if !d.closed {
				d.search(false)
			}
		})
	})
}

// search looks the name up in the frames and the catalog and, asked with
// simbad when nothing is found, in SIMBAD.
func (d *favDialog) search(simbad bool) {
	if d.timer != nil {
		d.timer.Stop()
	}
	q := strings.TrimSpace(d.query.Text())
	d.best = false
	d.found = nil
	if q == "" {
		d.compute()
		return
	}
	nq := catalog.Normalize(q)
	for _, f := range d.p.frameObjs {
		if strings.Contains(catalog.Normalize(f.name), nq) {
			d.found = append(d.found, planner.Favorite{Name: f.name, RA: f.ra, Dec: f.dec, Kind: "кадры", Size: catalog.SizeOf(f.name)})
		}
	}
	add := func(o catalog.Object) {
		for _, f := range d.found {
			if f.Name == o.Name {
				return
			}
		}
		d.found = append(d.found, planner.Favorite{Name: o.Name, Label: o.Label(), RA: o.RA, Dec: o.Dec, Kind: o.Kind, Size: o.Size})
	}
	if o, ok := catalog.Lookup(q); ok {
		add(o)
	}
	for _, o := range catalog.Search(q, 30) {
		add(o)
	}
	if len(d.found) > 30 {
		d.found = d.found[:30]
	}
	d.setTab(0)
	if len(d.found) > 0 || !simbad {
		d.compute()
		return
	}
	setLong(d.status, "В каталоге нет «"+q+"», ищу в SIMBAD…")
	go func() {
		o, err := catalog.Resolve(context.Background(), q)
		d.p.w.mw.Synchronize(func() {
			if d.closed || strings.TrimSpace(d.query.Text()) != q {
				return
			}
			if err != nil {
				setLong(d.status, err.Error())
				return
			}
			setLong(d.status, "Найдено в SIMBAD: "+o.Name)
			d.found = []planner.Favorite{{Name: o.Name, Label: o.Label(), RA: o.RA, Dec: o.Dec, Kind: o.Kind, Size: o.Size}}
			d.compute()
		})
	}()
}

func (d *favDialog) showBest() {
	d.setting = true
	d.query.SetText("")
	d.setting = false
	if d.timer != nil {
		d.timer.Stop()
	}
	d.best, d.found = true, nil
	d.setTab(0)
	setLong(d.status, "Подбираю объекты…")
	d.compute()
}

// compute: how the favorites and what was found are seen in the night, in
// the background; only the newest answer is shown.
func (d *favDialog) compute() {
	d.gen++
	gen := d.gen
	prm := d.p.params()
	d.zone = prm.Zone
	sky := copySky(d.p.state.Sky)
	f := d.filter()
	favs := append([]planner.Favorite(nil), d.p.state.Favorites...)
	found, best := append([]planner.Favorite(nil), d.found...), d.best
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Избранное: сбой расчёта: %v", r)
			}
		}()
		v, err := planner.See(prm.Night, prm.Lat, prm.Lon, prm.SunAlt, sky, favs, f)
		var fv *planner.Visibility
		if err == nil && best {
			bf := f
			if bf == nil {
				bf = &planner.Filter{AltMin: 30, AltMax: 90}
			}
			if found, err = planner.Best(prm.Night, prm.Lat, prm.Lon, prm.SunAlt, sky, bf); err == nil {
				fv, err = planner.See(prm.Night, prm.Lat, prm.Lon, prm.SunAlt, sky, found, f)
			}
		} else if err == nil && len(found) > 0 {
			fv, err = planner.See(prm.Night, prm.Lat, prm.Lon, prm.SunAlt, sky, found, f)
		}
		d.p.w.mw.Synchronize(func() {
			if d.closed || gen != d.gen {
				return
			}
			if err != nil {
				setLong(d.info, "Не посчитано: "+err.Error())
				return
			}
			d.vis = v
			d.visFav = map[string]*planner.Vis{}
			for i := range favs {
				d.visFav[favs[i].Name] = &v.Objects[i]
			}
			d.visFound = map[string]*planner.Vis{}
			if fv != nil {
				for i := range found {
					d.visFound[found[i].Name] = &fv.Objects[i]
				}
			}
			if best {
				d.found = found
				setLong(d.status, fmt.Sprintf("Лучшее этой ночью: %d объектов.", len(found)))
			}
			d.info.SetText(d.nightText())
			d.render()
		})
	}()
}

func copySky(s planner.Sky) planner.Sky {
	c := planner.Sky{MinAlt: s.MinAlt, Horizon: append(planner.Horizon(nil), s.Horizon...)}
	for _, z := range s.Zones {
		z.Poly = append([]planner.Point(nil), z.Poly...)
		c.Zones = append(c.Zones, z)
	}
	return c
}

func (d *favDialog) hm(ms int64) string { return time.UnixMilli(ms).In(d.zone).Format("15:04") }

func (d *favDialog) nightText() string {
	v := d.vis
	night := d.p.params().Night
	s := fmt.Sprintf("Ночь %s.%s: закат %s, восход %s, ", night[8:10], night[5:7], d.hm(v.From.UnixMilli()), d.hm(v.To.UnixMilli()))
	if v.Dusk.IsZero() {
		s += "тёмной ночи нет"
	} else {
		s += "тёмная ночь " + d.hm(v.Dusk.UnixMilli()) + "–" + d.hm(v.Dawn.UnixMilli())
	}
	s += ". Время — часы обсерватории."
	if d.p.state.Lat == 0 && d.p.state.Lon == 0 {
		s += " Задайте координаты обсерватории в планировщике."
	}
	return s
}

// render puts the rows of the tab into the list.
func (d *favDialog) render() {
	if d.closed {
		return
	}
	d.tabFav.SetText(fmt.Sprintf("♥ Избранные (%d)", len(d.p.state.Favorites)))
	if d.best {
		d.tabFound.SetText("Лучшее этой ночью")
	} else {
		d.tabFound.SetText("Поиск")
	}
	list, vis := d.found, d.visFound
	if d.tab == 1 {
		list, vis = d.p.state.Favorites, d.visFav
	}
	d.rows = d.rows[:0]
	d.hover = -1 // its row may be another now
	for _, o := range list {
		d.rows = append(d.rows, favRow{o, vis[o.Name]})
	}
	// the ones out of the filter go last
	sort.SliceStable(d.rows, func(i, j int) bool {
		return (d.rows[i].v == nil || d.rows[i].v.Match) && d.rows[j].v != nil && !d.rows[j].v.Match
	})
	h := len(d.rows) * favRowH
	if h == 0 {
		h = 60
	}
	d.list.SetMinMaxSize(walk.Size{Height: h}, walk.Size{Height: h})
	d.list.Invalidate()
}

const favRowH = 192 // DIP

// the parts of a row, in DIP from its top left
const (
	favPicX   = 40
	favPicW   = 66
	favTextX  = 114
	favBtnW   = 90
	favNoteY  = 76
	favChartY = 96
	favChartH = 90 // with the axis of the azimuth on top
)

func (d *favDialog) listClick(x, y int, b walk.MouseButton) {
	if b != walk.LeftButton {
		return
	}
	s := d.dpi()
	i := int(float64(y) / (favRowH * s))
	if i < 0 || i >= len(d.rows) {
		return
	}
	top := float64(i) * favRowH * s
	yy := float64(y) - top
	W := float64(d.list.ClientBoundsPixels().Width)
	r := d.rows[i]
	switch {
	case float64(x) < favPicX*s && yy < 40*s:
		d.p.toggleFavorite(r.o)
		d.render()
		d.p.syncFavorites(func(msg string) {
			if !d.closed {
				setLong(d.status, msg)
				d.compute()
			}
		})
	case float64(x) > W-(favBtnW+8)*s && yy < 36*s:
		d.toPlan(r.o)
	case float64(x) > W-(favBtnW+8)*s && yy >= 38*s && yy < 66*s && favIndex(d.p.state.Favorites, r.o.Name) >= 0:
		d.editNote(r.o.Name)
	}
}

func favTargetName(o planner.Favorite) string {
	if o.Kind == "кадры" || o.Label == "" {
		return o.Name
	}
	return strings.SplitN(o.Label, " — ", 2)[0]
}

func (d *favDialog) inPlan(o planner.Favorite) bool {
	n := favTargetName(o)
	for _, t := range d.p.state.Targets {
		if t.Name == n {
			return true
		}
	}
	return false
}

// toPlan adds the object to the targets of the planner.
func (d *favDialog) toPlan(o planner.Favorite) {
	if d.inPlan(o) {
		return
	}
	setLong(d.status, d.p.addObject(o))
	d.list.Invalidate()
}

// addObject adds the object to the targets of the planner, with the
// exposure of its frames when it was shot; it says what was done.
func (p *plannerUI) addObject(o planner.Favorite) string {
	n := favTargetName(o)
	for _, t := range p.state.Targets {
		if t.Name == n {
			return "«" + n + "» уже в плане."
		}
	}
	exp := p.expEdit.Value()
	for _, f := range p.frameObjs {
		if f.name == o.Name && f.exp > 0 {
			exp = f.exp
		}
	}
	p.state.Targets = append(p.state.Targets, planner.Target{Name: n, RA: math.Round(o.RA*1e5) / 1e5, Dec: math.Round(o.Dec*1e5) / 1e5, Exposure: exp})
	p.renumber()
	p.targetTable.SetCurrentIndex(len(p.state.Targets) - 1)
	return "«" + n + "» добавлен в объекты планировщика."
}

func (d *favDialog) visText(v *planner.Vis) string {
	var parts []string
	if v.Rise != 0 || v.Set != 0 {
		r, s := "—", "—"
		if v.Rise != 0 {
			r = d.hm(v.Rise)
		}
		if v.Set != 0 {
			s = d.hm(v.Set)
		}
		if len(v.Az) > 0 {
			if v.Rise != 0 {
				r += fmt.Sprintf(" на %s (%.0f°)", planner.SideName(v.RiseAz), v.RiseAz)
			}
			if v.Set != 0 {
				s += fmt.Sprintf(" на %s (%.0f°)", planner.SideName(v.SetAz), v.SetAz)
			}
		}
		parts = append(parts, "восход "+r, "заход "+s)
	} else if v.Up {
		parts = append(parts, "не заходит")
	} else {
		parts = append(parts, "не восходит")
	}
	side := ""
	if len(v.Az) > 0 {
		side = planner.SideName(v.TransitAz) + ", "
	}
	parts = append(parts, fmt.Sprintf("кульминация %s на %s%.0f°", d.hm(v.Transit), side, v.TransitAlt))
	var spans []string
	for i, s := range v.Spans {
		if i == 3 {
			spans = append(spans, "…")
			break
		}
		spans = append(spans, d.hm(s[0])+"–"+d.hm(s[1]))
	}
	if len(spans) > 0 {
		parts = append(parts, "открыт "+strings.Join(spans, ", "))
	} else {
		parts = append(parts, "телескопу не открыт")
	}
	if v.Dark > 0 {
		parts = append(parts, "в темноте "+planner.Hours(v.Dark))
	}
	if d.f.On {
		if v.Match {
			parts = append(parts, "в фильтре "+planner.Hours(v.MatchSec))
		} else {
			parts = append(parts, "вне фильтра")
		}
	}
	return strings.Join(parts, " · ")
}

// favRA, favDec: as ASIAIR writes them, 00h42m44s and +41°16'09".
func favRA(deg float64) string {
	s := (int(math.Round(deg/15*3600)) + 86400) % 86400
	return fmt.Sprintf("%02dh%02dm%02ds", s/3600, s/60%60, s%60)
}

func favDec(deg float64) string {
	sign := "+"
	if deg < 0 {
		sign = "-"
	}
	s := int(math.Round(math.Abs(deg) * 3600))
	return fmt.Sprintf("%s%02d°%02d'%02d\"", sign, s/3600, s/60%60, s%60)
}

func (d *favDialog) paintList(c *walk.Canvas, _ walk.Rectangle) error {
	cr := d.list.ClientBoundsPixels()
	s := d.dpi()
	px := func(v float64) int { return int(math.Round(v * s)) }
	fill := func(col walk.Color, r walk.Rectangle) {
		if b, err := walk.NewSolidColorBrush(col); err == nil {
			c.FillRectanglePixels(b, r)
			b.Dispose()
		}
	}
	fill(walk.RGB(255, 255, 255), walk.Rectangle{Width: cr.Width, Height: cr.Height})
	font := d.list.Font()
	bold, _ := walk.NewFont(font.Family(), font.PointSize()+1, walk.FontBold)
	small, _ := walk.NewFont(font.Family(), 8, 0)
	big, _ := walk.NewFont(font.Family(), 15, 0)
	for _, f := range []*walk.Font{bold, small, big} {
		if f == nil {
			return nil
		}
		defer f.Dispose()
	}
	text := func(t string, f *walk.Font, col walk.Color, r walk.Rectangle, fm walk.DrawTextFormat) {
		c.DrawTextPixels(t, f, col, r, fm)
	}
	if len(d.rows) == 0 {
		msg := "Введите название объекта: M 31, NGC 7000, IC 1805, Sh2-274, «Медуза». Enter — искать и в SIMBAD."
		switch {
		case d.tab == 1:
			msg = "Избранных пока нет: найдите объект и нажмите ♡."
		case d.best && d.vis != nil:
			msg = "Под фильтр ничего не попало."
		case d.best:
			msg = "Подбираю объекты…"
		}
		text(msg, font, walk.RGB(110, 110, 110), walk.Rectangle{X: px(8), Y: px(8), Width: cr.Width - px(16), Height: px(40)}, walk.TextWordbreak)
		return nil
	}
	W := cr.Width
	for i, r := range d.rows {
		top := int(float64(i) * favRowH * s)
		if top > cr.Height || top+px(favRowH) < 0 {
			continue
		}
		faded := r.v != nil && !r.v.Match
		ink, muted := walk.RGB(29, 35, 48), walk.RGB(106, 115, 132)
		if faded {
			ink, muted = walk.RGB(160, 165, 175), walk.RGB(185, 190, 198)
		}
		if i > 0 {
			fill(walk.RGB(221, 225, 231), walk.Rectangle{X: 0, Y: top, Width: W, Height: max(1, px(1))})
		}
		heart, hc := "♡", walk.RGB(200, 55, 55)
		if favIndex(d.p.state.Favorites, r.o.Name) >= 0 {
			heart = "♥"
		}
		if faded {
			hc = walk.RGB(230, 160, 160)
		}
		text(heart, big, hc, walk.Rectangle{X: px(4), Y: top + px(4), Width: px(34), Height: px(32)}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		// the sky around it (DSS2 from CDS)
		pr := walk.Rectangle{X: px(favPicX), Y: top + px(6), Width: px(favPicW), Height: px(favPicW)}
		if bmp := d.pic(r.o); bmp != nil {
			c.DrawImageStretchedPixels(bmp, pr)
		} else {
			fill(walk.RGB(20, 22, 28), pr)
		}
		// «В план»
		br := walk.Rectangle{X: W - px(favBtnW+8), Y: top + px(8), Width: px(favBtnW), Height: px(26)}
		bt, bc := "В план", walk.RGB(47, 111, 222)
		if d.inPlan(r.o) {
			bt, bc = "в плане", walk.RGB(150, 150, 150)
		}
		if pn, err := walk.NewCosmeticPen(walk.PenSolid, bc); err == nil {
			c.DrawRoundedRectanglePixels(pn, br, walk.Size{Width: px(8), Height: px(8)})
			pn.Dispose()
		}
		text(bt, font, bc, br, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		tx, tw := px(favTextX), W-px(favTextX+favBtnW+16)
		// the note: «✎ заметка» under «В план», the note under the times
		if fi := favIndex(d.p.state.Favorites, r.o.Name); fi >= 0 {
			text("✎ заметка", font, walk.RGB(47, 111, 222), walk.Rectangle{X: br.X, Y: top + px(40), Width: br.Width, Height: px(24)},
				walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
			if n := d.p.state.Favorites[fi].Note; n != "" {
				text("✎ "+n, font, walk.RGB(120, 80, 20), walk.Rectangle{X: tx, Y: top + px(favNoteY), Width: tw, Height: px(18)},
					walk.TextSingleLine|walk.TextEndEllipsis|walk.TextVCenter)
			}
		}
		label := r.o.Name
		if r.o.Label != "" && r.o.Label != r.o.Name {
			label = r.o.Label
			if !strings.HasPrefix(r.o.Label, r.o.Name) {
				label += " (" + r.o.Name + ")"
			}
		}
		text(label, bold, ink, walk.Rectangle{X: tx, Y: top + px(6), Width: tw, Height: px(20)}, walk.TextSingleLine|walk.TextEndEllipsis|walk.TextVCenter)
		meta := []string{}
		if k := planner.KindName[r.o.Kind]; k != "" {
			meta = append(meta, k)
		} else if r.o.Kind != "" {
			meta = append(meta, r.o.Kind)
		}
		meta = append(meta, "RA "+favRA(r.o.RA), "Dec "+favDec(r.o.Dec))
		text(strings.Join(meta, " · "), font, muted, walk.Rectangle{X: tx, Y: top + px(26), Width: tw, Height: px(18)}, walk.TextSingleLine|walk.TextEndEllipsis|walk.TextVCenter)
		if r.v == nil || d.vis == nil {
			continue
		}
		text(d.visText(r.v), font, muted, walk.Rectangle{X: tx, Y: top + px(44), Width: tw, Height: px(32)}, walk.TextWordbreak|walk.TextEndEllipsis)
		d.paintChart(c, r.v, walk.Rectangle{X: tx, Y: top + px(favChartY), Width: W - tx - px(10), Height: px(favChartH)}, faded, small)
	}
	d.paintView(c, font)
	return nil
}

// paintChart: the altitude through the night, as in the list of ASIAIR,
// the axis of the azimuth on top and the sides of the sky on the curve.
func (d *favDialog) paintChart(c *walk.Canvas, v *planner.Vis, r walk.Rectangle, faded bool, font *walk.Font) {
	vis := d.vis
	s := d.dpi()
	axisY := r.Y
	az := len(v.Az) == len(v.Alt)
	if az {
		r.Y += int(16 * s)
		r.Height -= int(16 * s)
	}
	ph := float64(r.Height) - 14*s
	from, to := vis.From.UnixMilli(), vis.To.UnixMilli()
	span := float64(to - from)
	if span <= 0 || len(v.Alt) == 0 {
		return
	}
	x := func(t int64) int { return r.X + int(float64(t-from)/span*float64(r.Width)) }
	y := func(alt float64) int { return r.Y + int((1-math.Max(0, alt)/90)*ph) }
	fill := func(col walk.Color, rr walk.Rectangle) {
		if b, err := walk.NewSolidColorBrush(col); err == nil {
			c.FillRectanglePixels(b, rr)
			b.Dispose()
		}
	}
	line := func(col walk.Color, style walk.PenStyle, width int, pts []walk.Point) {
		var pn walk.Pen
		var err error
		if width <= 1 {
			pn, err = walk.NewCosmeticPen(style, col)
		} else {
			b, e := walk.NewSolidColorBrush(col)
			if e != nil {
				return
			}
			defer b.Dispose()
			pn, err = walk.NewGeometricPen(style|walk.PenCapRound|walk.PenJoinRound, width, b)
		}
		if err != nil {
			return
		}
		if len(pts) == 2 {
			c.DrawLinePixels(pn, pts[0], pts[1])
		} else {
			c.DrawPolylinePixels(pn, pts)
		}
		pn.Dispose()
	}
	if !vis.Dusk.IsZero() {
		fill(walk.RGB(236, 239, 244), walk.Rectangle{X: x(vis.Dusk.UnixMilli()), Y: r.Y, Width: x(vis.Dawn.UnixMilli()) - x(vis.Dusk.UnixMilli()), Height: int(ph)})
	}
	if f := d.filter(); f != nil && !vis.FilterFrom.IsZero() {
		a, b := x(vis.FilterFrom.UnixMilli()), x(vis.FilterTo.UnixMilli())
		fill(walk.RGB(214, 238, 222), walk.Rectangle{X: a, Y: y(f.AltMax), Width: b - a, Height: y(f.AltMin) - y(f.AltMax)})
	}
	grid := walk.RGB(215, 220, 228)
	for _, a := range []float64{0, 30, 60} {
		line(grid, walk.PenSolid, 1, []walk.Point{{X: r.X, Y: y(a)}, {X: r.X + r.Width, Y: y(a)}})
		if a > 0 {
			c.DrawTextPixels(fmt.Sprintf("%.0f°", a), font, walk.RGB(130, 130, 130), walk.Rectangle{X: r.X + 2, Y: y(a) - int(14*s), Width: int(30 * s), Height: int(14 * s)}, walk.TextSingleLine|walk.TextBottom)
		}
	}
	// round hours, every two
	var ticks []int64
	for t := vis.From.In(d.zone).Truncate(time.Hour).Add(time.Hour); !t.After(vis.To); t = t.Add(time.Hour) {
		if t.In(d.zone).Hour()%2 != 0 {
			continue
		}
		ticks = append(ticks, t.UnixMilli())
		xx := x(t.UnixMilli())
		line(grid, walk.PenSolid, 1, []walk.Point{{X: xx, Y: r.Y}, {X: xx, Y: r.Y + int(ph)}})
		c.DrawTextPixels(t.In(d.zone).Format("15:04"), font, walk.RGB(130, 130, 130), walk.Rectangle{X: xx - int(25*s), Y: r.Y + int(ph), Width: int(50 * s), Height: int(14 * s)},
			walk.TextCenter|walk.TextSingleLine)
	}
	every := planner.VisEvery.Milliseconds()
	pts := make([]walk.Point, len(v.Alt))
	for i, a := range v.Alt {
		pts[i] = walk.Point{X: x(from + int64(i)*every), Y: y(a)}
	}
	alt, open := walk.RGB(140, 145, 155), walk.RGB(31, 138, 76)
	if faded {
		alt, open = walk.RGB(195, 198, 205), walk.RGB(160, 205, 178)
	}
	line(alt, walk.PenDash, 1, pts)
	// the parts open to the telescope
	var run []walk.Point
	flush := func() {
		if len(run) > 1 {
			line(open, walk.PenSolid, max(2, int(2.2*s)), run)
		}
		run = nil
	}
	for i, p := range pts {
		if i < len(v.Open) && v.Open[i] == '1' {
			run = append(run, p)
		} else {
			flush()
		}
	}
	flush()
	if az {
		d.paintAzimuth(c, v, ticks, from, x, y, axisY, r, faded, font)
	}
	if now := time.Now().UnixMilli(); now > from && now < to {
		line(walk.RGB(200, 55, 55), walk.PenSolid, 1, []walk.Point{{X: x(now), Y: r.Y}, {X: x(now), Y: r.Y + int(ph)}})
	}
}

// paintAzimuth: the azimuth at the times marked on the axis above the chart
// (axisY) and the side of the sky on the curve while the object is up.
func (d *favDialog) paintAzimuth(c *walk.Canvas, v *planner.Vis, ticks []int64, from int64, x func(int64) int, y func(float64) int,
	axisY int, r walk.Rectangle, faded bool, font *walk.Font) {
	s := d.dpi()
	px := func(f float64) int { return int(math.Round(f * s)) }
	ink, low := walk.RGB(29, 35, 48), walk.RGB(175, 180, 190)
	if faded {
		ink = walk.RGB(160, 165, 175)
	}
	bold, err := walk.NewFont(font.Family(), font.PointSize(), walk.FontBold)
	if err != nil {
		return
	}
	defer bold.Dispose()
	if pn, err := walk.NewCosmeticPen(walk.PenSolid, walk.RGB(215, 220, 228)); err == nil {
		c.DrawLinePixels(pn, walk.Point{X: r.X, Y: r.Y - 1}, walk.Point{X: r.X + r.Width, Y: r.Y - 1})
		pn.Dispose()
	}
	c.DrawTextPixels("азимут", font, walk.RGB(130, 130, 130), walk.Rectangle{X: r.X + px(2), Y: axisY, Width: px(44), Height: r.Y - axisY - 1},
		walk.TextSingleLine|walk.TextVCenter)
	brush, err := walk.NewSolidColorBrush(ink)
	if err != nil {
		return
	}
	defer brush.Dispose()
	every := planner.VisEvery.Milliseconds()
	for _, t := range ticks {
		i := int(math.Round(float64(t-from) / float64(every)))
		if i < 0 || i >= len(v.Az) {
			continue
		}
		xx := x(t)
		if xx-r.X >= px(44) { // not over «азимут»
			col := ink
			if v.Alt[i] <= 0 {
				col = low
			}
			c.DrawTextPixels(fmt.Sprintf("%.0f°", v.Az[i]), font, col, walk.Rectangle{X: xx - px(25), Y: axisY, Width: px(50), Height: r.Y - axisY - 1},
				walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
		if v.Alt[i] <= 3 {
			continue
		}
		yy := y(v.Alt[i])
		c.FillEllipsePixels(brush, walk.Rectangle{X: xx - px(2), Y: yy - px(2), Width: px(4), Height: px(4)})
		lr := walk.Rectangle{X: xx - px(15), Y: yy - px(17), Width: px(30), Height: px(14)}
		if yy-r.Y < px(18) { // near the top: under the curve
			lr.Y = yy + px(3)
		}
		c.DrawTextPixels(planner.SideName(v.Az[i]), bold, ink, lr, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
}

// the rose of the sides of the sky: its radius and centre in pixels
func (d *favDialog) roseGeom() (cx, cy, R float64) {
	cr := d.rose.ClientBoundsPixels()
	s := float64(d.rose.DPI()) / 96
	return float64(cr.Width) / 2, float64(cr.Height) / 2, math.Min(float64(cr.Width), float64(cr.Height))/2 - 16*s
}

func (d *favDialog) paintRose(c *walk.Canvas, _ walk.Rectangle) error {
	cr := d.rose.ClientBoundsPixels()
	cx, cy, R := d.roseGeom()
	if R < 10 {
		return nil
	}
	f := d.f.Filter
	lo, hi := math.Min(f.AltMin, f.AltMax), math.Max(f.AltMin, f.AltMax)
	im := image.NewRGBA(image.Rect(0, 0, cr.Width, cr.Height))
	for py := 0; py < cr.Height; py++ {
		for px := 0; px < cr.Width; px++ {
			dx, dy := float64(px)-cx, float64(py)-cy
			r := math.Hypot(dx, dy)
			col := color.RGBA{255, 255, 255, 255}
			if r <= R {
				az := math.Mod(math.Atan2(dx, -dy)*180/math.Pi+360, 360)
				bit := planner.Side(az)
				chosen := f.Dirs&bit != 0
				col = color.RGBA{240, 242, 246, 255}
				if chosen {
					col = color.RGBA{205, 235, 214, 255}
				}
				if alt := 90 * (1 - r/R); (f.Dirs == 0 || chosen) && alt >= lo && alt <= hi {
					col = color.RGBA{96, 176, 128, 255}
				}
			}
			im.SetRGBA(px, py, col)
		}
	}
	if bmp, err := walk.NewBitmapFromImageForDPI(im, 96); err == nil {
		c.DrawImagePixels(bmp, walk.Point{})
		bmp.Dispose()
	}
	s := float64(d.rose.DPI()) / 96
	if pn, err := walk.NewCosmeticPen(walk.PenSolid, walk.RGB(200, 205, 214)); err == nil {
		for _, a := range []float64{0, 30, 60} {
			rr := R * (1 - a/90)
			c.DrawEllipsePixels(pn, walk.Rectangle{X: int(cx - rr), Y: int(cy - rr), Width: int(2 * rr), Height: int(2 * rr)})
		}
		for k := 0; k < 8; k++ {
			a := (22.5 + 45*float64(k)) * math.Pi / 180
			c.DrawLinePixels(pn, walk.Point{X: int(cx), Y: int(cy)}, walk.Point{X: int(cx + R*math.Sin(a)), Y: int(cy - R*math.Cos(a))})
		}
		pn.Dispose()
	}
	font := d.rose.Font()
	for k, name := range planner.Sides {
		a := 45 * float64(k) * math.Pi / 180
		x, y := cx+(R+9*s)*math.Sin(a), cy-(R+9*s)*math.Cos(a)
		c.DrawTextPixels(name, font, walk.RGB(90, 95, 105), walk.Rectangle{X: int(x - 14*s), Y: int(y - 8*s), Width: int(28 * s), Height: int(16 * s)},
			walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
	return nil
}

func (d *favDialog) roseClick(x, y int, b walk.MouseButton) {
	if b != walk.LeftButton {
		return
	}
	cx, cy, R := d.roseGeom()
	dx, dy := float64(x)-cx, float64(y)-cy
	if math.Hypot(dx, dy) > R+16*float64(d.rose.DPI())/96 {
		return
	}
	d.f.Filter.Dirs ^= planner.Side(math.Mod(math.Atan2(dx, -dy)*180/math.Pi+360, 360))
	if d.f.Filter.Dirs == 255 {
		d.f.Filter.Dirs = 0
	}
	d.turnOn()
	d.filterChanged()
}

// picRect: the small picture of row i, in pixels of the list.
func (d *favDialog) picRect(i int) walk.Rectangle {
	s := d.dpi()
	px := func(v float64) int { return int(math.Round(v * s)) }
	return walk.Rectangle{X: px(favPicX), Y: int(float64(i)*favRowH*s) + px(6), Width: px(favPicW), Height: px(favPicW)}
}

func inRect(x, y int, r walk.Rectangle) bool {
	return x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

// listMove shows the object in the field of the camera while the mouse is
// on its small picture.
func (d *favDialog) listMove(x, y int, _ walk.MouseButton) {
	i := int(float64(y) / (favRowH * d.dpi()))
	if i < 0 || i >= len(d.rows) || !inRect(x, y, d.picRect(i)) {
		i = -1
	}
	if i == d.hover {
		return
	}
	d.hover = i
	d.list.Invalidate()
	if i >= 0 {
		for k := range d.viewsErr {
			delete(d.viewsAsk, k)
			delete(d.viewsErr, k)
		}
		d.watchHover()
	}
}

// watchHover hides the picture when the mouse has left the small one (the
// list hears no more of the mouse once it is outside).
func (d *favDialog) watchHover() {
	time.AfterFunc(150*time.Millisecond, func() {
		d.p.w.mw.Synchronize(func() {
			if d.closed || d.hover < 0 {
				return
			}
			var pt win.POINT
			win.GetCursorPos(&pt)
			win.ScreenToClient(d.list.Handle(), &pt)
			if d.hover >= len(d.rows) || !inRect(int(pt.X), int(pt.Y), d.picRect(d.hover)) {
				d.hover = -1
				d.list.Invalidate()
				return
			}
			d.watchHover()
		})
	})
}

// viewField: the field of the camera, degrees, and what the picture tells
// of it.
func (d *favDialog) viewField(o planner.Favorite) (fw, fh float64, text string) {
	op := planner.InEffect(d.p.state.Optics, d.p.setups)
	fw, fh = op.FOV()
	if fw > 0 && fh > 0 {
		deg := func(v float64) string {
			if v < 1 {
				return fmt.Sprintf("%d′", int(math.Round(v*60)))
			}
			return strings.Replace(fmt.Sprintf("%.2f°", v), ".", ",", 1)
		}
		f := op.Focal
		if op.Reducer > 0 {
			f *= op.Reducer
		}
		text = "В кадре телескопа: поле " + deg(fw) + " × " + deg(fh)
		if op.Camera != "" {
			text += " · " + op.Camera
		}
		return fw, fh, text + fmt.Sprintf(" · %.0f мм. Север вверху.", f)
	}
	size := o.Size
	if size <= 0 {
		size = catalog.SizeOf(o.Name)
	}
	if size <= 0 {
		size = 15
	}
	fw = math.Min(math.Max(size*3/60, 0.5), 6)
	return fw, fw, "Оптика не известна (задаётся в атласе или берётся из кадров): поле по размеру объекта. Север вверху."
}

// view is the picture of the object in the field fw × fh, asked for in the
// background when not there yet (nil until it comes).
func (d *favDialog) view(o planner.Favorite, fw, fh float64) *walk.Bitmap {
	key := fmt.Sprintf("%.4f %.4f %.4f %.4f", o.RA, o.Dec, fw, fh)
	if b := d.views[key]; b != nil || d.viewsAsk[key] {
		return b
	}
	d.viewsAsk[key] = true
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Снимок поля объекта: сбой: %v", r)
			}
		}()
		var im image.Image
		b, err := catalog.View(context.Background(), filepath.Join(configDir(), "thumbs"), o.RA, o.Dec, fw, fh)
		if err == nil {
			im, _, err = image.Decode(bytes.NewReader(b))
		}
		d.p.w.mw.Synchronize(func() {
			if d.closed {
				return
			}
			if err == nil {
				var bmp *walk.Bitmap
				if bmp, err = walk.NewBitmapFromImageForDPI(im, 96); err == nil {
					d.views[key] = bmp
				}
			}
			if err != nil {
				d.viewsErr[key] = true // asked again the next time the mouse comes
			}
			d.list.Invalidate()
		})
	}()
	return nil
}

// paintView draws the object in the field of the camera beside the small
// picture the mouse is on, within the part of the list seen.
func (d *favDialog) paintView(c *walk.Canvas, font *walk.Font) {
	if d.hover < 0 || d.hover >= len(d.rows) {
		return
	}
	o := d.rows[d.hover].o
	s := d.dpi()
	px := func(v float64) int { return int(math.Round(v * s)) }
	// the part of the list seen: the client of the scroll view, in pixels
	// of the list
	var rc win.RECT
	win.GetClientRect(d.scroll.Handle(), &rc)
	a, b := win.POINT{X: rc.Left, Y: rc.Top}, win.POINT{X: rc.Right, Y: rc.Bottom}
	win.ClientToScreen(d.scroll.Handle(), &a)
	win.ClientToScreen(d.scroll.Handle(), &b)
	win.ScreenToClient(d.list.Handle(), &a)
	win.ScreenToClient(d.list.Handle(), &b)
	seen := walk.Rectangle{X: int(a.X), Y: int(a.Y), Width: int(b.X - a.X), Height: int(b.Y - a.Y)}
	fw, fh, text := d.viewField(o)
	pad, capH := px(6), px(36)
	k := math.Min(float64(px(480)), float64(seen.Width-4*pad))
	k = math.Min(k, float64(seen.Height-capH-3*pad)*math.Max(fw, fh)/fh) / math.Max(fw, fh)
	w, h := int(fw*k), int(fh*k)
	if w < px(60) || h < px(30) {
		return
	}
	box := walk.Rectangle{Width: w + 2*pad, Height: h + capH + 2*pad}
	pr := d.picRect(d.hover)
	box.X = pr.X + pr.Width + px(10)
	if box.X+box.Width > seen.X+seen.Width-pad {
		box.X = max(seen.X+pad, seen.X+seen.Width-pad-box.Width)
	}
	box.Y = min(max(pr.Y, seen.Y+pad), seen.Y+seen.Height-pad-box.Height)
	fill := func(col walk.Color, r walk.Rectangle) {
		if br, err := walk.NewSolidColorBrush(col); err == nil {
			c.FillRectanglePixels(br, r)
			br.Dispose()
		}
	}
	fill(walk.RGB(150, 156, 168), walk.Rectangle{X: box.X - 1, Y: box.Y - 1, Width: box.Width + 2, Height: box.Height + 2})
	fill(walk.RGB(255, 255, 255), box)
	ir := walk.Rectangle{X: box.X + pad, Y: box.Y + pad, Width: w, Height: h}
	if bmp := d.view(o, fw, fh); bmp != nil {
		c.DrawImageStretchedPixels(bmp, ir)
	} else {
		fill(walk.RGB(10, 12, 16), ir)
		msg := "Загружаю снимок…"
		if d.viewsErr[fmt.Sprintf("%.4f %.4f %.4f %.4f", o.RA, o.Dec, fw, fh)] {
			msg = "Снимок не получен (нужен интернет)"
		}
		c.DrawTextPixels(msg, font, walk.RGB(160, 170, 170), ir, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
	c.DrawTextPixels(text, font, walk.RGB(80, 88, 104), walk.Rectangle{X: ir.X, Y: ir.Y + h + px(4), Width: w, Height: capH - px(4)},
		walk.TextWordbreak|walk.TextEndEllipsis)
}

// editNote asks for the note of the favorite name.
func (d *favDialog) editNote(name string) {
	i := favIndex(d.p.state.Favorites, name)
	if i < 0 {
		return
	}
	var dlg *walk.Dialog
	var edit *walk.LineEdit
	var ok, cancel *walk.PushButton
	note := ""
	label := d.p.state.Favorites[i].Label
	if label == "" {
		label = name
	}
	res, err := Dialog{
		AssignTo:      &dlg,
		Title:         "Заметка: " + label,
		DefaultButton: &ok,
		CancelButton:  &cancel,
		MinSize:       Size{Width: 460},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: fmt.Sprintf("Несколько слов об объекте (до %d знаков): что снимать, с каким фильтром, что не получилось.\nПусто — убрать заметку. Заметка общая со страницей приёмника.", planner.MaxNote)},
			LineEdit{AssignTo: &edit, Text: d.p.state.Favorites[i].Note, MaxLength: planner.MaxNote},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &ok, Text: "Сохранить", OnClicked: func() { note = edit.Text(); dlg.Accept() }},
				PushButton{AssignTo: &cancel, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Run(d.dlg)
	if err != nil || res != walk.DlgCmdOK {
		return
	}
	d.p.setFavNote(name, note)
	d.render()
	d.p.syncFavorites(func(msg string) {
		if !d.closed {
			setLong(d.status, msg)
			d.render()
		}
	})
}
