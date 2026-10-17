package main

import (
	"math"
	"path/filepath"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/config"
	"github.com/arbalet1985/asiair_transmit_photo/internal/satellites"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// The tracks of satellites (planes, meteors) on the light frames: found
// while the stars are measured (or by the receiver), told with the orbits
// of the CelesTrak catalogue, drawn on the preview and told in the header.

// satCatalog: the catalogues of orbits, in the folder of the settings.
func satCatalog() *sorter.SatCatalog {
	return sorter.NewSatCatalog(filepath.Join(configDir(), "satellites"))
}

// satSite: the observatory of the planner, for frames without coordinates
// in the header (nil when not known).
func (w *window) satSite() *satellites.Site {
	if s := w.pl.state; s.Lat != 0 || s.Lon != 0 {
		return &satellites.Site{Lat: s.Lat, Lon: s.Lon}
	}
	return nil
}

// currentFrame: the frame of the row selected, nil when none.
func (w *window) currentFrame() *sorter.Frame {
	i := w.table.CurrentIndex()
	if i < 0 || i >= len(w.model.rows) {
		return nil
	}
	return w.model.rows[i].f
}

// withTracks puts the tracks of the frame selected before the header text.
func (w *window) withTracks(head string) string {
	f := w.currentFrame()
	if f == nil || len(f.Tracks) == 0 {
		return head
	}
	t := "Треки спутников (самолётов, метеоров) на кадре:\r\n" + f.TracksInfo()
	if len(frameSats(f)) > 0 {
		t += "\r\nВладелец, запуск, орбита, фото и ссылки — правой кнопкой по строке таблицы, «О спутнике …»."
	}
	return t + "\r\n\r\n" + head
}

// tracksChanged shows again the tracks of the frame selected, when they
// are among the frames changed.
func (w *window) tracksChanged(changed map[*sorter.Frame]bool) {
	f := w.currentFrame()
	if f == nil || !changed[f] || w.previewPending {
		return
	}
	if w.tilt != nil {
		w.showTilt(w.tilt, "")
		return
	}
	w.header.SetText(w.withTracks(w.previewHead))
	w.zoom.cw.Invalidate()
}

func (w *window) satsToggled() {
	on := w.satBox.Checked()
	w.mu.Lock()
	w.saved.HideTracks = !on
	w.mu.Unlock()
	w.zoom.cw.Invalidate()
}

// paintTracks draws the tracks of the frame shown beside them, with what
// left them, as the receiver's page does.
func (w *window) paintTracks(c *walk.Canvas, x0, y0, dw, dh float64) {
	f := w.currentFrame()
	z := w.zoom
	if f == nil || len(f.Tracks) == 0 || !w.satBox.Checked() || w.previewPending || z.srcW <= 0 || z.w <= 0 {
		return
	}
	srcW := float64(z.srcW)
	srcH := srcW * float64(z.h) / float64(z.w)
	kx, ky := dw/srcW, dh/srcH
	at := func(x, y float64) (float64, float64) {
		if f.FlipRows {
			y = srcH - 1 - y
		}
		return x0 + (x+0.5)*kx, y0 + (y+0.5)*ky
	}
	blue := walk.RGB(59, 180, 229)
	b, err := walk.NewSolidColorBrush(blue)
	if err != nil {
		return
	}
	defer b.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenDash|walk.PenCapFlat, 2, b)
	if err != nil {
		return
	}
	defer pen.Dispose()
	font, err := walk.NewFont("Segoe UI", 10, walk.FontBold)
	if err != nil {
		return
	}
	defer font.Dispose()
	cw := float64(z.cw.ClientBoundsPixels().Width)
	for _, t := range f.Tracks {
		ax, ay := at(t.X0, t.Y0)
		bx, by := at(t.X1, t.Y1)
		// beside the track, not on it
		l := math.Max(math.Hypot(bx-ax, by-ay), 1)
		nx, ny := -(by-ay)/l*8, (bx-ax)/l*8
		c.DrawLinePixels(pen, walk.Point{X: int(ax + nx), Y: int(ay + ny)}, walk.Point{X: int(bx + nx), Y: int(by + ny)})
		lx, ly := (ax+bx)/2+nx*2.5, (ay+by)/2+ny*2.5
		r := walk.Rectangle{X: int(lx), Y: int(ly) - 10, Width: 320, Height: 20}
		format := walk.TextLeft | walk.TextSingleLine | walk.TextVCenter
		if lx > cw*0.7 {
			r.X -= r.Width
			format = walk.TextRight | walk.TextSingleLine | walk.TextVCenter
		}
		label := f.TrackLabel(t)
		shadow := r
		shadow.X, shadow.Y = r.X+1, r.Y+1
		c.DrawTextPixels(label, font, walk.RGB(0, 0, 0), shadow, format)
		c.DrawTextPixels(label, font, blue, r, format)
	}
}

// tracksMatch: the frame is shown by the filter of the tracks (trackFilters).
func tracksMatch(f *sorter.Frame, mode string) bool {
	if f.Kind != sorter.Light {
		return false
	}
	switch mode {
	case "with":
		return len(f.Tracks) > 0
	case "without":
		return f.Measured && len(f.Tracks) == 0 // an unmeasured frame is not known to be clean
	case "planes":
		for _, t := range f.Tracks {
			if t.Plane != nil {
				return true
			}
		}
	case "unknown":
		for _, t := range f.Tracks {
			if f.SatTold && t.Sat == nil && t.Plane == nil {
				return true
			}
		}
	}
	return false
}

// spaceTrackDialog asks the login of space-track.org: the orbits of the
// past, for frames on the disk taken when no catalogue of CelesTrak was
// downloaded near their time.
func (w *window) spaceTrackDialog() {
	w.mu.Lock()
	user, passSet := w.saved.SpaceTrackUser, w.saved.SpaceTrackPass != ""
	w.mu.Unlock()
	var dlg *walk.Dialog
	var ok, cancel *walk.PushButton
	var userEdit, passEdit *walk.LineEdit
	cue := "пароль"
	if passSet {
		cue = "сохранён — введите новый, чтобы заменить"
	}
	accept := func() {
		u, p := strings.TrimSpace(userEdit.Text()), passEdit.Text()
		if u != "" && p == "" && !passSet {
			w.warn("Введите пароль Space-Track.")
			return
		}
		w.mu.Lock()
		w.saved.SpaceTrackUser = u
		switch {
		case u == "":
			w.saved.SpaceTrackPass = ""
		case p != "":
			w.saved.SpaceTrackPass = config.Seal(p)
		}
		s := w.saved
		w.mu.Unlock()
		s.save()
		dlg.Accept()
	}
	err := Dialog{
		AssignTo:      &dlg,
		Title:         "Архив орбит Space-Track",
		DefaultButton: &ok,
		CancelButton:  &cancel,
		MinSize:       Size{Width: 520},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "CelesTrak даёт только текущие орбиты, поэтому треки на старых кадрах опознаются по далёким орбитам и хуже.\n" +
				"На space-track.org (бесплатная регистрация) есть архив орбит за любую дату: сортер скачивает орбиты\n" +
				"каждой такой ночи один раз и хранит их. Если кадры берутся с приёмника, архив сначала берётся у него.\n" +
				"API-ключей Space-Track не выдаёт — нужны логин и пароль; лучше отдельный пароль, нигде больше не используемый.\n" +
				"Пароль хранится зашифрованным средствами Windows. Чтобы удалить вход, оставьте логин пустым."},
			Composite{
				Layout: Grid{Columns: 2, MarginsZero: true},
				Children: []Widget{
					Label{Text: "Логин (e-mail):"},
					LineEdit{AssignTo: &userEdit, Text: user},
					Label{Text: "Пароль:"},
					LineEdit{AssignTo: &passEdit, PasswordMode: true, CueBanner: cue},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &ok, Text: "OK", OnClicked: accept},
					PushButton{AssignTo: &cancel, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(w.mw)
	if err != nil {
		w.warn(err.Error())
		return
	}
	if dlg.Run() == walk.DlgCmdOK {
		w.setStatus("Вход в Space-Track сохранён — архив орбит скачается при следующей оценке кадров.")
	}
}
