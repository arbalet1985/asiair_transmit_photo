package main

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
	"github.com/arbalet1985/asiair_transmit_photo/internal/stars"
)

// The tilt of each camera over the frames shown (stars.SummarizeTilt), as
// in the receiver: the newest camTiltFrames frames of each camera are
// measured and summed up with the outliers left out.

const camTiltFrames = 300

var camTiltBusy atomic.Bool

// cameraTilt measures the frames shown, by camera, and shows the summary.
func (w *window) cameraTilt() {
	if !camTiltBusy.CompareAndSwap(false, true) {
		w.setStatus("Тилт камер уже считается…")
		return
	}
	type item struct {
		f     *sorter.Frame
		local []string
	}
	groups := map[string][]item{}
	var keys []string
	for _, r := range w.model.rows {
		f := r.f
		if f.Kind != sorter.Light || f.Measured && f.Stars < 80 {
			continue
		}
		key := cameraKey(f)
		if groups[key] == nil {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], item{f, w.localPaths(r)})
	}
	if len(keys) == 0 {
		camTiltBusy.Store(false)
		w.warn("В таблице нет Light-кадров. Тилт камеры считается по показанным кадрам — снимите фильтры или выберите объект.")
		return
	}
	total := 0
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(a, b int) bool { return g[a].f.UTC.After(g[b].f.UTC) })
		if len(g) > camTiltFrames {
			groups[k] = g[:camTiltFrames]
		}
		total += len(groups[k])
	}
	shown := map[string]int{}
	for _, r := range w.model.rows {
		if r.f.Kind == sorter.Light {
			shown[cameraKey(r.f)]++
		}
	}
	w.setStatus(fmt.Sprintf("Тилт камер: измеряю %d кадров…", total))
	go func() {
		defer camTiltBusy.Store(false)
		var b strings.Builder
		done := 0
		for _, k := range keys {
			var ts []stars.Tilt
			failed := 0
			for _, it := range groups[k] {
				key := fmt.Sprintf("%p|%s", it.f.Src, it.f.Path)
				t, ok := w.tilts.get(key)
				if !ok {
					var err error
					if t, err = tiltOf(it.f, it.local); err != nil {
						failed++
					} else {
						w.tilts.put(key, t)
						ok = true
					}
				}
				if ok {
					ts = append(ts, t)
				}
				done++
				n := done
				w.mw.Synchronize(func() {
					w.setStatus(fmt.Sprintf("Тилт камер: измерено %d из %d кадров…", n, total))
				})
			}
			s, ok := stars.SummarizeTilt(ts)
			fmt.Fprintf(&b, "%s — кадров в таблице: %d, взято ближайших по времени: %d\r\n", k, shown[k], len(groups[k]))
			if !ok {
				fmt.Fprintf(&b, "  мало кадров, на которых тилт удалось измерить (%d, не удалось: %d)\r\n\r\n", len(ts), failed)
				continue
			}
			where := s.Bigger()
			if where == "" {
				where = "углы почти одинаковы"
			} else {
				where = "звёзды крупнее " + where
			}
			fmt.Fprintf(&b, "  ↖ %.2f   ↗ %.2f\r\n  ↙ %.2f   ↘ %.2f   (HFR в пикселях при HFR кадра %.2f)\r\n", s.Corners[0], s.Corners[1], s.Corners[2], s.Corners[3], s.HFR)
			fmt.Fprintf(&b, "  тилт      %.2f px (%.0f%%) — %s\r\n", s.Tilt, s.Percent, where)
			verdict := ""
			if s.Percent >= 5 {
				if s.Spread < 0.5*s.Percent {
					verdict = " — перекос устойчивый: похоже на наклон матрицы или фокусёра"
				} else {
					verdict = " — перекос меняется от кадра к кадру: скорее гидирование, ветер или прогиб"
				}
			}
			fmt.Fprintf(&b, "  разброс   ±%.1f%% по кадрам%s\r\n", s.Spread, verdict)
			fmt.Fprintf(&b, "  вне оси   %.2f px (края − центр: кривизна поля, кома)\r\n", s.OffAxis)
			fmt.Fprintf(&b, "  кадров взято %d из %d измеренных", s.Used, s.Frames)
			if s.Frames > s.Used {
				fmt.Fprintf(&b, " (%d отброшены как выбросы)", s.Frames-s.Used)
			}
			if failed > 0 {
				fmt.Fprintf(&b, ", не измерено %d", failed)
			}
			b.WriteString("\r\n\r\n")
		}
		text := b.String()
		w.mw.Synchronize(func() {
			w.setStatus("Тилт камер посчитан.")
			w.showCameraTilt(text)
		})
	}()
}

// cameraKey: a camera with its binning.
func cameraKey(f *sorter.Frame) string {
	k := f.Camera
	if k == "" {
		k = "камера не указана"
	}
	if f.Bin > 1 {
		k += fmt.Sprintf(", бин %d", f.Bin)
	}
	return k
}

func (w *window) showCameraTilt(text string) {
	var dlg *walk.Dialog
	var ok *walk.PushButton
	Dialog{
		AssignTo:      &dlg,
		Title:         "Тилт по 300 ближайшим кадрам",
		DefaultButton: &ok,
		CancelButton:  &ok,
		MinSize:       Size{Width: 640, Height: 460},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Для каждой камеры — 300 ближайших по времени (последних) кадров из показанных в таблице. Углы каждого кадра берутся\r\n" +
				"относительно его среднего (так уходят фокус, атмосфера и фильтр), кадры с перекосом, далёким от остальных, отбрасываются,\r\n" +
				"а по остальным берётся медиана."},
			TextEdit{ReadOnly: true, VScroll: true, Text: text, Font: Font{Family: "Consolas", PointSize: 10}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{HSpacer{}, PushButton{AssignTo: &ok, Text: "Закрыть", OnClicked: func() { dlg.Accept() }}}},
		},
	}.Run(w.mw)
}
