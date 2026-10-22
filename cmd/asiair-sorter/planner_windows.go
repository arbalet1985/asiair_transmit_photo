package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/arbalet1985/asiair_transmit_photo/internal/atlas"
	"github.com/arbalet1985/asiair_transmit_photo/internal/catalog"
	"github.com/arbalet1985/asiair_transmit_photo/internal/moon"
	"github.com/arbalet1985/asiair_transmit_photo/internal/planner"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
	"github.com/arbalet1985/asiair_transmit_photo/internal/weather"
)

// The planner: the sky of the observatory guessed from the frames taken
// (the horizon, the zones hidden by wires) and corrected with the mouse,
// the targets of the night with their priority, the plan and its
// animation. It is a tab of the main window.

// plannerSettings is what is kept of the planner.
type plannerSettings struct {
	Lat     float64          `json:"lat"`
	Lon     float64          `json:"lon"`
	TZ      float64          `json:"tz"` // hours from UTC of the observatory clock
	SiteSet bool             `json:"site_set,omitempty"`
	Sky     planner.Sky      `json:"sky"`
	SkySet  bool             `json:"sky_set,omitempty"` // edited by hand: the frames do not replace it
	Targets []planner.Target `json:"targets,omitempty"`
	Params  *planner.Params  `json:"params,omitempty"`
	// Optics: of the frames of the atlas (nil: the last setup of the frames)
	Optics *planner.Optics `json:"optics,omitempty"`
}

// frameTarget is an object of the frames taken, as a suggestion: where it
// was last pointed at, the exposure of the last frame, how much was shot.
type frameTarget struct {
	name    string
	ra, dec float64
	exp     float64
	frames  int
	total   float64 // seconds
	last    time.Time
	night   string
}

type plannerUI struct {
	w *window

	latEdit, lonEdit, tzEdit                    *walk.NumberEdit
	dateEdit                                    *walk.DateEdit
	twilightBox, speedBox                       *walk.ComboBox
	minAltEdit, switchEdit, focusEdit           *walk.NumberEdit
	focusEveryEdit, ditherEdit, ditherEveryEdit *walk.NumberEdit
	downloadEdit, flipAfterEdit, flipEdit       *walk.NumberEdit
	flipBeforeEdit                              *walk.NumberEdit
	blockEdit, moonSepEdit                      *walk.NumberEdit
	focusSwitchBox, flipBox, pointsBox          *walk.CheckBox
	focusFilterBox                              *walk.CheckBox
	onceBox, onlyShootBox                       *walk.CheckBox

	startEdit                             *walk.LineEdit
	nameEdit, raEdit, decEdit, filterEdit *walk.LineEdit
	foundBox, shotBox                     *walk.ComboBox
	expEdit, wantEdit                     *walk.NumberEdit
	targetTable, stepTable                *walk.TableView
	targets                               *targetModel
	steps                                 *stepModel
	timeSlider                            *walk.Slider
	btnPlay                               *walk.PushButton
	timeLabel, skyInfo, planInfo          *walk.Label
	wxView                                *walk.CustomWidget // the forecast of the clouds
	wxInfo                                *walk.Label

	sky   *skyView
	state plannerSettings // site, sky, targets (the parameters live in the fields)

	found      []catalog.Object
	frameObjs  []frameTarget
	guess      planner.Guess
	plan       *planner.Plan
	dusk, dawn time.Time // of the night chosen (also without a plan)
	animT      time.Time
	playing    bool
	playGen    int
	syncing    bool // the slider is moved by the program
	ready      bool
	// the forecast held, its error, the site it was asked for and when
	wx     *weather.Forecast
	wxErr  string
	wxKey  string
	wxAt   time.Time
	wxBusy bool

	atlas  *atlas.Server   // the atlas page, once opened
	setups []planner.Setup // the optics of the frames read
	// the frame over the sky of the atlas and the framing asked for it
	photo            *atlas.Photo
	photoV, frameReq int64
	frameI           int
}

func newPlanner(w *window, s plannerSettings) *plannerUI {
	p := &plannerUI{w: w, state: s, targets: &targetModel{}, steps: &stepModel{}}
	p.targets.p, p.steps.p = p, p
	p.sky = &skyView{p: p}
	if p.state.Sky.MinAlt == 0 && len(p.state.Sky.Horizon) == 0 {
		p.state.Sky.MinAlt = 15
	}
	return p
}

var twilights = []struct {
	label string
	alt   float64
}{{"астрономические (−18°)", -18}, {"навигационные (−12°)", -12}, {"гражданские (−6°)", -6}}

var animSpeeds = []struct {
	label string
	min   float64 // minutes of the night per second
}{{"5 мин/с", 5}, {"15 мин/с", 15}, {"30 мин/с", 30}, {"1 ч/с", 60}}

func num(assign **walk.NumberEdit, v float64, dec int, min, max float64, suffix, tip string) NumberEdit {
	return NumberEdit{AssignTo: assign, Value: v, Decimals: dec, MinValue: min, MaxValue: max, Suffix: suffix,
		MinSize: Size{Width: 62}, MaxSize: Size{Width: 80}, ToolTipText: tip}
}

// page is the tab of the planner.
func (p *plannerUI) page() TabPage {
	prm := planner.DefaultParams()
	if p.state.Params != nil {
		prm = *p.state.Params
	}
	tw := 0
	for i, t := range twilights {
		if t.alt == prm.SunAlt {
			tw = i
		}
	}
	var speeds []string
	for _, s := range animSpeeds {
		speeds = append(speeds, s.label)
	}
	var tws []string
	for _, t := range twilights {
		tws = append(tws, t.label)
	}
	var tcols, scols []TableViewColumn
	for _, c := range targetColumns {
		col := TableViewColumn{Title: c.title, Width: c.width}
		if c.right {
			col.Alignment = AlignFar
		}
		tcols = append(tcols, col)
	}
	for _, c := range stepColumns {
		col := TableViewColumn{Title: c.title, Width: c.width}
		if c.right {
			col.Alignment = AlignFar
		}
		scols = append(scols, col)
	}
	changed := func() { p.paramsChanged() }
	gray := walk.RGB(90, 90, 90)
	// each row of parameters is two: one was wider than a 1536 px screen
	return TabPage{Title: "Планировщик", Layout: VBox{MarginsZero: true}, Children: []Widget{
		// scrolled on a small screen: the window is never wider than it
		// (a grid: a box would get a spacer taking half the height)
		ScrollView{Layout: Grid{Columns: 1}, Children: []Widget{
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Широта:"},
				NumberEdit{AssignTo: &p.latEdit, Value: p.state.Lat, Decimals: 4, MinValue: -90, MaxValue: 90, MinSize: Size{Width: 75}, MaxSize: Size{Width: 90},
					OnValueChanged: changed, ToolTipText: "Широта обсерватории, градусы (берётся из заголовков кадров SITELAT)"},
				Label{Text: "Долгота:"},
				NumberEdit{AssignTo: &p.lonEdit, Value: p.state.Lon, Decimals: 4, MinValue: -180, MaxValue: 180, MinSize: Size{Width: 75}, MaxSize: Size{Width: 90},
					OnValueChanged: changed, ToolTipText: "Долгота обсерватории, градусы, восток положительный (SITELONG)"},
				Label{Text: "Часы обсерватории: UTC"},
				NumberEdit{AssignTo: &p.tzEdit, Value: p.state.TZ, Decimals: 2, MinValue: -12, MaxValue: 14, Increment: 0.5, MinSize: Size{Width: 55}, MaxSize: Size{Width: 70},
					OnValueChanged: changed, ToolTipText: "Часовой пояс, в котором показан план (по кадрам: разница времени в имени файла и DATE-OBS)"},
				HSpacer{},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Ночь с:"},
				DateEdit{AssignTo: &p.dateEdit, Date: defaultNight(), OnDateChanged: changed, ToolTipText: "Вечер, с которого начинается ночь"},
				Label{Text: "Начать в:"},
				LineEdit{AssignTo: &p.startEdit, CueBanner: "ЧЧ:ММ", MaxSize: Size{Width: 55}, MinSize: Size{Width: 55}, OnEditingFinished: changed,
					ToolTipText: "Во сколько начать съёмку по часам обсерватории: пусто — с начала ночи.\nПригодится, когда начали позже плана — например пережидали облака"},
				PushButton{Text: "сейчас", MaxSize: Size{Width: 60}, OnClicked: p.startNow,
					ToolTipText: "Поставить текущее время обсерватории: план с этого момента"},
				Label{Text: "Сумерки:"},
				ComboBox{AssignTo: &p.twilightBox, Model: tws, CurrentIndex: tw, OnCurrentIndexChanged: changed,
					ToolTipText: "Ночь — пока Солнце ниже этой высоты"},
				Label{Text: "Не ниже:"},
				num(&p.minAltEdit, p.state.Sky.MinAlt, 0, 0, 80, "°", "Не снимать ниже этой высоты, где бы ни был горизонт"),
				HSpacer{},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Переход на объект:"},
				num(&p.switchEdit, prm.SwitchMin, 1, 0, 60, " мин", "Наведение, решение поля, запуск гида — на каждую смену объекта"),
				Label{Text: "Фокусировка:"},
				num(&p.focusEdit, prm.RefocusMin, 1, 0, 30, " мин", "Сколько длится автофокус"),
				Label{Text: "каждые"},
				num(&p.focusEveryEdit, prm.RefocusEveryMin, 0, 0, 600, " мин", "Как часто перефокусироваться (0 — не перефокусироваться)"),
				CheckBox{AssignTo: &p.focusSwitchBox, Text: "и после перехода", Checked: prm.RefocusAfterSwitch, OnCheckedChanged: changed},
				CheckBox{AssignTo: &p.focusFilterBox, Text: "и после смены фильтра", Checked: prm.RefocusAfterFilter, OnCheckedChanged: changed,
					ToolTipText: "Автофокус, когда следующий объект снимается с другим фильтром. Объект без фильтра смены не вызывает"},
				HSpacer{},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Дизеринг:"},
				num(&p.ditherEdit, prm.DitherSec, 0, 0, 600, " с", "Сдвиг и успокоение гида после него"),
				Label{Text: "каждые"},
				num(&p.ditherEveryEdit, float64(prm.DitherEvery), 0, 0, 100, " кадр.", "Дизеринг через столько кадров (0 — без дизеринга)"),
				Label{Text: "Между кадрами:"},
				num(&p.downloadEdit, prm.DownloadSec, 0, 0, 120, " с", "Скачивание кадра с камеры"),
				HSpacer{},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				CheckBox{AssignTo: &p.flipBox, Text: "Перекладка через меридиан:", Checked: prm.Flip, OnCheckedChanged: changed,
					ToolTipText: "Экваториальная монтировка: после меридиана телескоп перекладывается на другую сторону"},
				Label{Text: "стоп за"},
				num(&p.flipBeforeEdit, prm.FlipBeforeMin, 0, 0, 120, " мин", "За сколько минут до меридиана прекратить съёмку: монтировка останавливается и ждёт перекладки (0 — снимать до самого меридиана)"),
				Label{Text: "до меридиана, перекладка через"},
				num(&p.flipAfterEdit, prm.FlipAfterMin, 0, -60, 120, " мин", "Через сколько минут после меридиана переложиться (как в настройках ASIAIR)"),
				Label{Text: "после меридиана, занимает"},
				num(&p.flipEdit, prm.FlipMin, 1, 0, 60, " мин", "Перекладка, повторное наведение и запуск гида"),
				HSpacer{},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				Label{Text: "Не идти на объект меньше чем на"},
				num(&p.blockEdit, prm.MinBlockMin, 0, 0, 240, " мин", "Объект, который будет виден меньше, не берётся: переход не окупится"),
				Label{Text: "До Луны не ближе"},
				num(&p.moonSepEdit, prm.MoonSep, 0, 0, 180, "°", "0 — Луна не мешает"),
				CheckBox{AssignTo: &p.onceBox, Text: "Не повторять объект", Checked: !prm.Repeat, OnCheckedChanged: changed,
					ToolTipText: "Каждый объект снимается один раз: план выбирает для него время, когда он виден дольше всего,\nи после перехода на другой объект назад не возвращается"},
				HSpacer{},
			}},
			VSplitter{StretchFactor: 1, Children: []Widget{
				HSplitter{StretchFactor: 3, Children: []Widget{
					Composite{StretchFactor: 2, Layout: VBox{MarginsZero: true}, Children: []Widget{
						Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							Label{Text: "Объект:"},
							LineEdit{AssignTo: &p.nameEdit, MinSize: Size{Width: 110}, CueBanner: "M31, NGC 7000, Медуза…",
								OnKeyDown: func(k walk.Key) {
									if k == walk.KeyReturn {
										p.find()
									}
								}, ToolTipText: "Название или номер: M 31, NGC 7000, IC 1805, Sh2-274, «Медуза», «Сердце». Enter — найти"},
							PushButton{Text: "Найти", OnClicked: p.find, MaxSize: Size{Width: 60}},
							ComboBox{AssignTo: &p.foundBox, MinSize: Size{Width: 160}, OnCurrentIndexChanged: p.foundChosen,
								ToolTipText: "Найденное в каталоге (OpenNGC, Sh2), в кадрах и, если нет, в SIMBAD"},
							ComboBox{AssignTo: &p.shotBox, MinSize: Size{Width: 170}, Model: []string{"снятые объекты…"}, CurrentIndex: 0,
								OnCurrentIndexChanged: p.shotChosen,
								ToolTipText:           "Объекты, уже снятые (по прочитанным кадрам): координаты последнего наведения и выдержка — чтобы продолжить съёмку"},
						}},
						Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							Label{Text: "RA:"},
							LineEdit{AssignTo: &p.raEdit, MinSize: Size{Width: 75}, MaxSize: Size{Width: 90}, ToolTipText: "Прямое восхождение J2000, часы: 07 29 03 или 7.484"},
							Label{Text: "Dec:"},
							LineEdit{AssignTo: &p.decEdit, MinSize: Size{Width: 75}, MaxSize: Size{Width: 90}, ToolTipText: "Склонение J2000, градусы: +13 14 49 или 13.25"},
							Label{Text: "Выдержка:"},
							num(&p.expEdit, 300, 0, 1, 3600, " с", "Выдержка одного кадра"),
							Label{Text: "Нужно:"},
							num(&p.wantEdit, 0, 1, 0, 24, " ч", "Сколько часов выдержки набрать за ночь (0 — сколько получится)"),
							Label{Text: "Фильтр:"},
							LineEdit{AssignTo: &p.filterEdit, MaxSize: Size{Width: 60}},
						}},
						Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							PushButton{Text: "Добавить", OnClicked: p.addTarget},
							PushButton{Text: "Изменить", OnClicked: p.updateTarget, ToolTipText: "Записать поля в выбранную строку"},
							PushButton{Text: "Удалить", OnClicked: p.deleteTarget},
							PushButton{Text: "↑", MaxSize: Size{Width: 30}, OnClicked: func() { p.moveTarget(-1) }, ToolTipText: "Важнее"},
							PushButton{Text: "↓", MaxSize: Size{Width: 30}, OnClicked: func() { p.moveTarget(1) }, ToolTipText: "Менее важно"},
							HSpacer{},
							PushButton{Text: "Атлас…", OnClicked: p.openAtlas,
								ToolTipText: "Атлас неба в браузере: рамки поля зрения объектов плана (оптика по кадрам, редьюсер) и кадрирование — сдвиг и поворот рамки"},
						}},
						TableView{AssignTo: &p.targetTable, Model: p.targets, Columns: tcols, CheckBoxes: true, NotSortableByHeaderClick: true,
							OnCurrentIndexChanged: p.targetChosen, StretchFactor: 1},
						Label{Text: "Порядок строк — приоритет: снимается самый верхний из видимых. Галочка — объект в плане.",
							EllipsisMode: EllipsisEnd, TextColor: gray},
					}},
					Composite{StretchFactor: 3, Layout: VBox{MarginsZero: true}, Children: []Widget{
						CustomWidget{AssignTo: &p.sky.cw, PaintPixels: p.sky.paint, StretchFactor: 1, DoubleBuffering: true, InvalidatesOnResize: true,
							MinSize: Size{Width: 300, Height: 200}, OnMouseDown: p.sky.mouseDown, OnMouseMove: p.sky.mouseMove, OnMouseUp: p.sky.mouseUp,
							ToolTipText: "Горизонт: тащите точки; щелчок — новая точка; правая кнопка — удалить точку или зону.\n" +
								"Закрытые зоны (провода): Shift + протянуть — новый прямоугольник; тащить — сдвинуть, за угол или вершину — изменить;\n" +
								"Ctrl + щелчок — новый многоугольник: щелчками вершины, замкнуть щелчком по первой вершине или правой кнопкой;\n" +
								"правая кнопка по вершине многоугольника — удалить вершину;\n" +
								"щелчок по бледной «возможной» зоне — принять её."},
						Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
							PushButton{AssignTo: &p.btnPlay, Text: "▶", MaxSize: Size{Width: 32}, OnClicked: func() { p.setPlay(!p.playing) },
								ToolTipText: "Анимация ночи: как телескоп переходит между объектами и ведёт их"},
							Slider{AssignTo: &p.timeSlider, MinValue: 0, MaxValue: 600, Tracking: true, ToolTipsHidden: true, OnValueChanged: p.sliderMoved},
							Label{AssignTo: &p.timeLabel, MinSize: Size{Width: 45}},
							ComboBox{AssignTo: &p.speedBox, Model: speeds, CurrentIndex: 2, MaxSize: Size{Width: 80}},
							CheckBox{AssignTo: &p.pointsBox, Text: "кадры", Checked: true, OnCheckedChanged: p.sky.invalidate,
								ToolTipText: "Показать, где были снятые кадры (цвет — объект)"},
							PushButton{Text: "Горизонт по кадрам", OnClicked: p.skyFromFrames,
								ToolTipText: "Угадать горизонт и закрытые зоны заново по кадрам последнего «Просмотреть кадры»;\nточки и зоны, поставленные или поправленные руками, остаются"},
							PushButton{Text: "Сбросить", OnClicked: p.resetSky, ToolTipText: "Ровный горизонт на высоте «Не ниже», без зон"},
							PushButton{Text: "Небо с приёмника", OnClicked: p.skyFromReceiver,
								ToolTipText: "Взять горизонт, закрытые зоны и координаты обсерватории из планировщика приёмника\n(адрес — входной каталог или последний приёмник из прежних)"},
						}},
						CustomWidget{AssignTo: &p.wxView, PaintPixels: p.paintWeather, DoubleBuffering: true, InvalidatesOnResize: true, Visible: false,
							MinSize: Size{Height: 40}, MaxSize: Size{Height: 40},
							ToolTipText: "Прогноз облачности Open-Meteo по часам: чем темнее, тем облачнее; линия — время анимации"},
						Label{AssignTo: &p.wxInfo, EllipsisMode: EllipsisEnd},
						Label{AssignTo: &p.skyInfo, EllipsisMode: EllipsisEnd, TextColor: gray},
					}},
				}},
				Composite{StretchFactor: 2, Layout: VBox{MarginsZero: true}, Children: []Widget{
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						PushButton{Text: "Составить план", OnClicked: p.makePlan},
						PushButton{Text: "Копировать план", OnClicked: p.copyPlan, ToolTipText: "Текст плана — для ввода в план ASIAIR"},
						PushButton{Text: "Сохранить CSV…", OnClicked: p.saveCSV},
						CheckBox{AssignTo: &p.onlyShootBox, Text: "только съёмка", OnCheckedChanged: p.stepsChanged,
							ToolTipText: "Скрыть переходы, фокусировки, перекладки и ожидания"},
						Label{AssignTo: &p.planInfo, EllipsisMode: EllipsisEnd},
						HSpacer{},
					}},
					TableView{AssignTo: &p.stepTable, Model: p.steps, Columns: scols, AlternatingRowBG: true, NotSortableByHeaderClick: true,
						OnCurrentIndexChanged: p.stepChosen, StretchFactor: 1, StyleCell: p.steps.style},
				}},
			}},
		}},
	}}
}

// startNow puts the clock of the observatory into «Начать в».
func (p *plannerUI) startNow() {
	p.startEdit.SetText(time.Now().In(p.params().Zone).Format("15:04"))
}

// defaultNight: this evening, or last evening before noon.
func defaultNight() time.Time {
	t := time.Now()
	if t.Hour() < 12 {
		t = t.AddDate(0, 0, -1)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// init runs when the window is made.
func (p *plannerUI) init() {
	if !p.state.SiteSet {
		p.tzEdit.SetValue(float64(offsetOf(time.Now())) / 3600)
	}
	p.ready = true
	p.targets.PublishRowsReset()
	p.paramsChanged()
}

func offsetOf(t time.Time) int {
	_, off := t.Zone()
	return off
}

// params are the fields of the planner.
func (p *plannerUI) params() planner.Params {
	prm := planner.Params{
		Night:   p.dateEdit.Date().Format("2006-01-02"),
		StartAt: strings.TrimSpace(p.startEdit.Text()),
		Lat:     p.latEdit.Value(), Lon: p.lonEdit.Value(),
		Zone:               time.FixedZone(tzName(p.tzEdit.Value()), int(math.Round(p.tzEdit.Value()*3600))),
		SunAlt:             twilights[max(p.twilightBox.CurrentIndex(), 0)].alt,
		SwitchMin:          p.switchEdit.Value(),
		RefocusEveryMin:    p.focusEveryEdit.Value(),
		RefocusMin:         p.focusEdit.Value(),
		RefocusAfterSwitch: p.focusSwitchBox.Checked(),
		RefocusAfterFilter: p.focusFilterBox.Checked(),
		DitherEvery:        int(p.ditherEveryEdit.Value()),
		DitherSec:          p.ditherEdit.Value(),
		DownloadSec:        p.downloadEdit.Value(),
		Flip:               p.flipBox.Checked(),
		FlipBeforeMin:      p.flipBeforeEdit.Value(),
		FlipAfterMin:       p.flipAfterEdit.Value(),
		FlipMin:            p.flipEdit.Value(),
		MinBlockMin:        p.blockEdit.Value(),
		MoonSep:            p.moonSepEdit.Value(),
		Repeat:             !p.onceBox.Checked(),
	}
	return prm
}

func tzName(h float64) string {
	if h == 0 {
		return "UTC"
	}
	m := int(math.Round(math.Abs(h) * 60))
	sign := "+"
	if h < 0 {
		sign = "−"
	}
	if m%60 == 0 {
		return fmt.Sprintf("UTC%s%d", sign, m/60)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, m/60, m%60)
}

// settings: what to keep (called with w.mu held).
func (p *plannerUI) settings() plannerSettings {
	s := p.state
	if p.ready {
		s.Lat, s.Lon, s.TZ = p.latEdit.Value(), p.lonEdit.Value(), p.tzEdit.Value()
		s.Sky.MinAlt = p.minAltEdit.Value()
		prm := p.params()
		s.Params = &prm
	}
	return s
}

// paramsChanged: the night or the site changed — the plan is made again.
func (p *plannerUI) paramsChanged() {
	if !p.ready {
		return
	}
	prm := p.params()
	p.state.Sky.MinAlt = p.minAltEdit.Value()
	if p.latEdit.Value() != p.state.Lat || p.lonEdit.Value() != p.state.Lon {
		p.state.SiteSet = true
	}
	p.state.Lat, p.state.Lon, p.state.TZ = prm.Lat, prm.Lon, p.tzEdit.Value()
	dusk, dawn, err := planner.Night(prm.Night, prm.Lat, prm.Lon, prm.SunAlt)
	p.dusk, p.dawn = dusk, dawn
	if err != nil {
		p.dusk, p.dawn = time.Time{}, time.Time{}
		setLong(p.skyInfo, err.Error()+": выберите сумерки повыше")
	} else {
		m := moon.At(dusk.Add(dawn.Sub(dusk)/2), prm.Lat, prm.Lon, true)
		setLong(p.skyInfo, fmt.Sprintf("Ночь %s – %s (%s), Луна %s, освещена на %d%%.", dusk.In(prm.Zone).Format("15:04"),
			dawn.In(prm.Zone).Format("15:04"), planner.Hours(dawn.Sub(dusk).Seconds()), phaseName(&m), int(math.Round(m.Illum*100))))
	}
	p.weatherNeeded()
	if p.plan != nil {
		p.makePlan()
		return
	}
	p.setTime(p.dusk)
	p.sky.invalidate()
}

// setFrames takes the frames of a scan: the site, the clock and the sky
// they tell.
func (p *plannerUI) setFrames(frames []sorter.Frame) {
	var obs []planner.Obs
	var lats, lons []float64
	offsets := map[int]int{}
	objIdx := map[string]int{}
	p.frameObjs = nil
	var sf []planner.SetupFrame
	for i := range frames {
		f := &frames[i]
		if f.Kind == sorter.Light {
			sf = append(sf, planner.SetupFrame{Camera: f.Camera, Focal: f.Focal, Pixel: f.PixelUm, Width: f.Width, Height: f.Height, Time: f.UTC})
		}
		if f.Kind != sorter.Light || !f.HasTarget || !f.HasSite || f.UTC.IsZero() {
			continue
		}
		obs = append(obs, planner.Obs{Object: f.Object, Night: f.Night, UTC: f.UTC, Exposure: f.Exposure,
			RA: f.RA, Dec: f.Dec, Lat: f.Lat, Lon: f.Lon})
		lats, lons = append(lats, f.Lat), append(lons, f.Lon)
		// the clock of the observatory: the name tells it, DATE-OBS is UTC
		wall := time.Date(f.Time.Year(), f.Time.Month(), f.Time.Day(), f.Time.Hour(), f.Time.Minute(), f.Time.Second(), 0, time.UTC)
		off := int(math.Round(wall.Sub(f.UTC).Minutes()/15)) * 15
		offsets[off]++
		if f.Object != "" {
			k, ok := objIdx[f.Object]
			if !ok {
				k = len(p.frameObjs)
				objIdx[f.Object] = k
				p.frameObjs = append(p.frameObjs, frameTarget{name: f.Object})
			}
			o := &p.frameObjs[k]
			o.frames++
			o.total += f.Exposure
			if f.UTC.After(o.last) { // the last pointing
				o.ra, o.dec, o.exp, o.last, o.night = f.RA, f.Dec, f.Exposure, f.UTC, f.Night
			}
		}
	}
	sort.Slice(p.frameObjs, func(i, j int) bool { return p.frameObjs[i].name < p.frameObjs[j].name })
	p.showShot()
	p.setups = planner.SetupsOf(sf)
	p.atlasChanged()
	if len(obs) == 0 {
		return
	}
	if !p.state.SiteSet {
		sort.Float64s(lats)
		sort.Float64s(lons)
		p.latEdit.SetValue(lats[len(lats)/2])
		p.lonEdit.SetValue(lons[len(lons)/2])
		best, n := 0, 0
		for off, c := range offsets {
			if c > n {
				best, n = off, c
			}
		}
		if math.Abs(float64(best)) <= 14*60 {
			p.tzEdit.SetValue(float64(best) / 60)
		}
		p.state.SiteSet = false // taken from the frames, not typed
	}
	p.guess = planner.Infer(obs, p.minAltEdit.Value())
	if !p.state.SkySet {
		p.useGuess()
	}
	p.paramsChanged()
}

func (p *plannerUI) useGuess() {
	p.state.Sky.Horizon = append(planner.Horizon(nil), p.guess.Horizon...)
	p.state.Sky.Zones = append([]planner.Zone(nil), p.guess.Zones...)
}

func (p *plannerUI) skyFromFrames() {
	if len(p.guess.Points) == 0 {
		p.w.warn("Нет кадров Light с координатами объекта и обсерватории (RA, DEC, SITELAT, SITELONG в заголовке): нажмите «Просмотреть кадры».")
		return
	}
	p.guess = planner.Infer(p.guess.Points, p.minAltEdit.Value())
	p.state.Sky = p.state.Sky.Reguess(p.guess, p.state.SkySet)
	p.state.SkySet = p.state.Sky.Manual()
	p.skyChanged()
}

func (p *plannerUI) resetSky() {
	p.state.Sky.Horizon, p.state.Sky.Zones, p.state.SkySet = nil, nil, true
	p.skyChanged()
}

// skyChanged: the horizon or the zones were edited.
func (p *plannerUI) skyChanged() {
	if p.plan != nil {
		p.makePlan()
	}
	p.sky.invalidate()
}

// find looks the name up in the catalog, in the frames and, when nothing
// is found, in SIMBAD.
func (p *plannerUI) find() {
	q := strings.TrimSpace(p.nameEdit.Text())
	if q == "" {
		return
	}
	var found []catalog.Object
	for _, f := range p.frameObjs {
		if strings.Contains(catalog.Normalize(f.name), catalog.Normalize(q)) {
			found = append(found, catalog.Object{Name: f.name, RA: f.ra, Dec: f.dec, Kind: "кадры"})
		}
	}
	if o, ok := catalog.Lookup(q); ok {
		found = append(found, o)
	}
	for _, o := range catalog.Search(q, 30) {
		if len(found) == 0 || o.Name != found[len(found)-1].Name {
			found = append(found, o)
		}
	}
	if len(found) > 0 {
		p.showFound(found)
		return
	}
	p.showFound(nil)
	setLong(p.planInfo, "В каталоге нет «"+q+"», ищу в SIMBAD…")
	go func() {
		o, err := catalog.Resolve(context.Background(), q)
		p.w.mw.Synchronize(func() {
			if err != nil {
				setLong(p.planInfo, err.Error())
				return
			}
			setLong(p.planInfo, "Найдено в SIMBAD: "+o.Name)
			p.showFound([]catalog.Object{o})
		})
	}()
}

func (p *plannerUI) showFound(found []catalog.Object) {
	p.found = found
	var labels []string
	for _, o := range found {
		l := o.Label()
		if o.Kind == "кадры" {
			l += " (из кадров)"
		}
		labels = append(labels, l)
	}
	p.foundBox.SetModel(labels)
	if len(found) > 0 {
		p.foundBox.SetCurrentIndex(0)
		p.foundChosen()
	}
}

func (p *plannerUI) foundChosen() {
	i := p.foundBox.CurrentIndex()
	if i < 0 || i >= len(p.found) {
		return
	}
	o := p.found[i]
	p.raEdit.SetText(formatRA(o.RA))
	p.decEdit.SetText(formatDec(o.Dec))
}

// showShot lists the objects already shot in their box.
func (p *plannerUI) showShot() {
	labels := []string{fmt.Sprintf("снятые объекты (%d)…", len(p.frameObjs))}
	for _, o := range p.frameObjs {
		l := fmt.Sprintf("%s — %s, кадров %d", o.name, planner.Hours(o.total), o.frames)
		if len(o.night) == 10 {
			l += ", посл. " + o.night[8:10] + "." + o.night[5:7]
		}
		labels = append(labels, l)
	}
	p.shotBox.SetModel(labels)
	p.shotBox.SetCurrentIndex(0)
}

// shotChosen puts an object already shot into the fields: its last
// pointing and exposure, to plan it again.
func (p *plannerUI) shotChosen() {
	i := p.shotBox.CurrentIndex() - 1
	if i < 0 || i >= len(p.frameObjs) {
		return
	}
	o := p.frameObjs[i]
	p.shotBox.SetCurrentIndex(0)
	p.nameEdit.SetText(o.name)
	p.showFound([]catalog.Object{{Name: o.name, RA: o.ra, Dec: o.dec, Kind: "кадры"}})
	if o.exp > 0 {
		p.expEdit.SetValue(o.exp)
	}
	msg := fmt.Sprintf("«%s»: последнее наведение и выдержка из кадров — нажмите «Добавить».", o.name)
	for k, t := range p.state.Targets {
		if t.Name == o.name {
			msg = fmt.Sprintf("«%s» уже в списке (строка %d) — «Изменить» запишет поля в выбранную строку.", o.name, k+1)
			break
		}
	}
	setLong(p.planInfo, msg)
}

func formatRA(deg float64) string {
	s := int(math.Round(deg / 15 * 3600))
	s = (s + 86400) % 86400
	return fmt.Sprintf("%02d %02d %02d", s/3600, s/60%60, s%60)
}

func formatDec(deg float64) string {
	sign := "+"
	if deg < 0 {
		sign = "-"
	}
	s := int(math.Round(math.Abs(deg) * 3600))
	return fmt.Sprintf("%s%02d %02d %02d", sign, s/3600, s/60%60, s%60)
}

// fieldsTarget reads the fields into a target.
func (p *plannerUI) fieldsTarget() (planner.Target, bool) {
	name := strings.TrimSpace(p.nameEdit.Text())
	if i := p.foundBox.CurrentIndex(); i >= 0 && i < len(p.found) && (name == "" || catalog.Normalize(name) != catalog.Normalize(p.found[i].Name)) {
		if o := p.found[i]; o.Kind == "кадры" {
			name = o.Name
		} else if l := o.Label(); l != "" {
			name = strings.SplitN(l, " — ", 2)[0]
		}
	}
	ra, ok1 := moon.ParseAngle(p.raEdit.Text())
	dec, ok2 := moon.ParseAngle(p.decEdit.Text())
	if name == "" || !ok1 || !ok2 || ra < 0 || ra >= 24 || math.Abs(dec) > 90 {
		p.w.warn("Укажите объект и его координаты: найдите его по названию или впишите RA (часы, 0–24) и Dec (градусы).")
		return planner.Target{}, false
	}
	return planner.Target{Name: name, RA: ra * 15, Dec: dec, Exposure: p.expEdit.Value(), WantSec: p.wantEdit.Value() * 3600,
		Filter: strings.TrimSpace(p.filterEdit.Text())}, true
}

func (p *plannerUI) renumber() {
	for i := range p.state.Targets {
		p.state.Targets[i].Priority = i + 1
	}
	p.plan = nil
	p.steps.PublishRowsReset()
	p.targets.PublishRowsReset()
	p.sky.invalidate()
	p.atlasChanged()
}

func (p *plannerUI) addTarget() {
	t, ok := p.fieldsTarget()
	if !ok {
		return
	}
	p.state.Targets = append(p.state.Targets, t)
	p.renumber()
	p.targetTable.SetCurrentIndex(len(p.state.Targets) - 1)
}

func (p *plannerUI) updateTarget() {
	i := p.targetTable.CurrentIndex()
	if i < 0 || i >= len(p.state.Targets) {
		p.addTarget()
		return
	}
	t, ok := p.fieldsTarget()
	if !ok {
		return
	}
	t.Off = p.state.Targets[i].Off
	t.Angle = p.state.Targets[i].Angle
	p.state.Targets[i] = t
	p.renumber()
	p.targetTable.SetCurrentIndex(i)
}

func (p *plannerUI) deleteTarget() {
	i := p.targetTable.CurrentIndex()
	if i < 0 || i >= len(p.state.Targets) {
		return
	}
	p.state.Targets = append(p.state.Targets[:i], p.state.Targets[i+1:]...)
	p.renumber()
}

func (p *plannerUI) moveTarget(d int) {
	i := p.targetTable.CurrentIndex()
	j := i + d
	if i < 0 || j < 0 || j >= len(p.state.Targets) {
		return
	}
	ts := p.state.Targets
	ts[i], ts[j] = ts[j], ts[i]
	p.renumber()
	p.targetTable.SetCurrentIndex(j)
}

// targetChosen puts the row into the fields.
func (p *plannerUI) targetChosen() {
	i := p.targetTable.CurrentIndex()
	if i < 0 || i >= len(p.state.Targets) {
		return
	}
	t := p.state.Targets[i]
	p.nameEdit.SetText(t.Name)
	p.found = nil
	p.foundBox.SetModel([]string{})
	p.raEdit.SetText(formatRA(t.RA))
	p.decEdit.SetText(formatDec(t.Dec))
	p.expEdit.SetValue(t.Exposure)
	p.wantEdit.SetValue(t.WantSec / 3600)
	p.filterEdit.SetText(t.Filter)
	p.sky.invalidate()
	p.atlasChanged()
}

func (p *plannerUI) makePlan() {
	prm := p.params()
	if len(p.state.Targets) == 0 {
		setLong(p.planInfo, "Добавьте объекты: найдите по названию и нажмите «Добавить».")
		return
	}
	sky := p.state.Sky
	sky.MinAlt = p.minAltEdit.Value()
	plan, err := planner.Schedule(p.state.Targets, sky, prm)
	if err != nil {
		p.plan = nil
		p.steps.PublishRowsReset()
		setLong(p.planInfo, err.Error())
		p.sky.invalidate()
		return
	}
	p.plan = plan
	p.steps.PublishRowsReset()
	p.targets.PublishRowsReset()
	var parts []string
	for i, t := range p.state.Targets {
		if !t.Off {
			parts = append(parts, fmt.Sprintf("%s %s", t.Name, planner.Hours(plan.Seconds[i])))
		}
	}
	setLong(p.planInfo, "План: "+strings.Join(parts, ", "))
	if p.animT.Before(plan.Dusk) || p.animT.After(plan.Dawn) {
		p.setTime(plan.Start)
	}
	p.sky.invalidate()
}

func (p *plannerUI) copyPlan() {
	if p.plan == nil {
		p.makePlan()
	}
	if p.plan == nil {
		return
	}
	if err := walk.Clipboard().SetText(strings.ReplaceAll(p.plan.Text(), "\n", "\r\n")); err != nil {
		p.w.warn(err.Error())
		return
	}
	setLong(p.planInfo, "План скопирован: вставьте его в заметки и перенесите в план ASIAIR.")
}

func (p *plannerUI) saveCSV() {
	if p.plan == nil {
		p.makePlan()
	}
	if p.plan == nil {
		return
	}
	dlg := walk.FileDialog{Title: "Сохранить план", Filter: "CSV (*.csv)|*.csv", FilePath: "план " + p.plan.Params.Night + ".csv"}
	if ok, err := dlg.ShowSave(p.w.mw); err != nil || !ok {
		return
	}
	name := dlg.FilePath
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		name += ".csv"
	}
	// with a BOM: Excel then reads the Russian letters right
	if err := os.WriteFile(name, append([]byte("\xEF\xBB\xBF"), p.plan.CSV()...), 0o644); err != nil {
		p.w.warn(err.Error())
	}
}

func (p *plannerUI) stepChosen() {
	i := p.stepTable.CurrentIndex()
	if i < 0 || i >= len(p.steps.rows) {
		return
	}
	p.setPlay(false)
	p.setTime(p.steps.rows[i].start)
}

// stepsChanged: the table is filled again (the «только съёмка» box).
func (p *plannerUI) stepsChanged() {
	if !p.ready {
		return
	}
	p.steps.PublishRowsReset()
}

// the animation

func (p *plannerUI) setTime(t time.Time) {
	p.animT = t
	if !p.dusk.IsZero() {
		p.syncing = true
		p.timeSlider.SetRange(0, int(p.dawn.Sub(p.dusk).Minutes()))
		p.timeSlider.SetValue(int(t.Sub(p.dusk).Minutes()))
		p.syncing = false
	}
	if !t.IsZero() {
		p.timeLabel.SetText(t.In(p.params().Zone).Format("15:04"))
	} else {
		p.timeLabel.SetText("")
	}
	p.sky.invalidate()
	if p.wxView != nil {
		p.wxView.Invalidate()
	}
}

func (p *plannerUI) sliderMoved() {
	if p.syncing || p.dusk.IsZero() {
		return
	}
	p.setTime(p.dusk.Add(time.Duration(p.timeSlider.Value()) * time.Minute))
}

func (p *plannerUI) setPlay(on bool) {
	on = on && !p.dusk.IsZero()
	p.playing = on
	p.playGen++
	if on {
		p.btnPlay.SetText("⏸")
		if !p.animT.Before(p.dawn) || p.animT.Before(p.dusk) {
			p.setTime(p.dusk)
		}
		p.tick(p.playGen)
	} else {
		p.btnPlay.SetText("▶")
	}
}

const frameEvery = 40 * time.Millisecond

func (p *plannerUI) tick(gen int) {
	time.AfterFunc(frameEvery, func() {
		p.w.mw.Synchronize(func() {
			if gen != p.playGen || !p.playing {
				return
			}
			sp := animSpeeds[max(p.speedBox.CurrentIndex(), 0)].min
			t := p.animT.Add(time.Duration(sp * float64(time.Minute) * frameEvery.Seconds()))
			if !t.Before(p.dawn) {
				p.setTime(p.dawn)
				p.setPlay(false)
				return
			}
			p.setTime(t)
			p.tick(gen)
		})
	})
}

// the tables

var targetColumns = []struct {
	title string
	width int
	right bool
}{
	{"№", 40, true}, {"Объект", 110, false}, {"RA", 70, false}, {"Dec", 75, false}, {"Выдержка", 65, true},
	{"Нужно", 60, true}, {"Фильтр", 50, false}, {"Макс. высота", 85, true}, {"В плане", 130, true},
}

type targetModel struct {
	walk.TableModelBase
	p *plannerUI
}

func (m *targetModel) RowCount() int { return len(m.p.state.Targets) }

func (m *targetModel) Value(i, col int) interface{} {
	t := m.p.state.Targets[i]
	switch col {
	case 0:
		return fmt.Sprint(i + 1)
	case 1:
		return t.Name
	case 2:
		return formatRA(t.RA)
	case 3:
		return formatDec(t.Dec)
	case 4:
		return fmt.Sprintf("%g с", t.Exposure)
	case 5:
		if t.WantSec <= 0 {
			return "сколько выйдет"
		}
		return planner.Hours(t.WantSec)
	case 6:
		return t.Filter
	case 7:
		if m.p.dusk.IsZero() {
			return ""
		}
		best, at := -90.0, time.Time{}
		for u := m.p.dusk; !u.After(m.p.dawn); u = u.Add(10 * time.Minute) {
			if a := moon.Object(u, m.p.state.Lat, m.p.state.Lon, t.RA, t.Dec).Alt; a > best {
				best, at = a, u
			}
		}
		return fmt.Sprintf("%d° в %s", int(math.Round(best)), at.In(m.p.params().Zone).Format("15:04"))
	case 8:
		if pl := m.p.plan; pl != nil && i < len(pl.Frames) && !t.Off {
			return fmt.Sprintf("%d × %gс · %s", pl.Frames[i], t.Exposure, planner.Hours(pl.Seconds[i]))
		}
	}
	return ""
}

func (m *targetModel) Checked(i int) bool { return !m.p.state.Targets[i].Off }

func (m *targetModel) SetChecked(i int, on bool) error {
	m.p.state.Targets[i].Off = !on
	// the plan is made again after the table has finished with the click
	m.p.w.mw.Synchronize(func() {
		if m.p.plan != nil {
			m.p.makePlan()
		}
		m.p.sky.invalidate()
		m.p.atlasChanged()
	})
	return nil
}

var stepColumns = []struct {
	title string
	width int
	right bool
}{
	{"Начало", 55, false}, {"Конец", 55, false}, {"Действие", 90, false}, {"Объект", 110, false},
	{"Кадров", 55, true}, {"Высота", 80, true}, {"До Луны", 60, true}, {"", 160, false},
}

// stepRow is a row of the table: the head of a block (the visit to a
// target, with its total) or a step of it.
type stepRow struct {
	head  bool
	block planner.Block
	step  int // index in Plan.Steps, head: the first of the block
	start time.Time
}

type stepModel struct {
	walk.TableModelBase
	p    *plannerUI
	rows []stepRow
}

// rebuild fills the rows from the plan.
func (m *stepModel) rebuild() {
	m.rows = nil
	pl := m.p.plan
	if pl == nil {
		return
	}
	only := m.p.onlyShootBox != nil && m.p.onlyShootBox.Checked()
	for _, b := range pl.Blocks() {
		if only && b.Frames == 0 {
			continue
		}
		m.rows = append(m.rows, stepRow{head: true, block: b, step: b.First, start: b.Start})
		for k := b.First; k < b.Last; k++ {
			if only && pl.Steps[k].Kind != planner.Shoot {
				continue
			}
			m.rows = append(m.rows, stepRow{block: b, step: k, start: pl.Steps[k].Start})
		}
	}
}

func (m *stepModel) PublishRowsReset() {
	m.rebuild()
	m.TableModelBase.PublishRowsReset()
}

func (m *stepModel) RowCount() int { return len(m.rows) }

func (m *stepModel) Value(i, col int) interface{} {
	pl := m.p.plan
	r := m.rows[i]
	if r.head {
		b := r.block
		switch col {
		case 0:
			return pl.Local(b.Start).Format("15:04")
		case 1:
			return pl.Local(b.End).Format("15:04")
		case 3:
			if b.Target >= 0 {
				return pl.Targets[b.Target].Name
			}
			return "без объекта"
		case 4:
			if b.Frames > 0 {
				return fmt.Sprint(b.Frames)
			}
		case 7:
			if b.Seconds > 0 {
				return "всего " + planner.Hours(b.Seconds)
			}
		}
		return ""
	}
	s := pl.Steps[r.step]
	switch col {
	case 0:
		return pl.Local(s.Start).Format("15:04")
	case 1:
		return pl.Local(s.End).Format("15:04")
	case 2:
		return planner.KindLabel[s.Kind]
	case 3:
		if s.Target >= 0 {
			return pl.Targets[s.Target].Name
		}
	case 4:
		if s.Kind == planner.Shoot {
			return fmt.Sprint(s.Frames)
		}
	case 5:
		if s.Target >= 0 {
			a, b := pl.Where(s.Target, s.Start), pl.Where(s.Target, s.End)
			return fmt.Sprintf("%d° → %d°", int(math.Round(a.Alt)), int(math.Round(b.Alt)))
		}
	case 6:
		if s.Target >= 0 {
			mid := s.Start.Add(s.End.Sub(s.Start) / 2)
			mn := moon.At(mid, pl.Params.Lat, pl.Params.Lon, true)
			w := pl.Where(s.Target, mid)
			return fmt.Sprintf("%d°", int(math.Round(moon.Separation(moon.Pos{Alt: w.Alt, Az: w.Az}, moon.Pos{Alt: mn.Alt, Az: mn.Az}))))
		}
	case 7:
		return s.Note
	}
	return ""
}

func (m *stepModel) style(c *walk.CellStyle) {
	pl := m.p.plan
	if pl == nil || c.Row() < 0 || c.Row() >= len(m.rows) {
		return
	}
	r := m.rows[c.Row()]
	if r.head {
		c.BackgroundColor = walk.RGB(232, 234, 238)
		if r.block.Target >= 0 {
			col := targetColor(r.block.Target)
			c.BackgroundColor = walk.RGB(255-(255-col.R())/2, 255-(255-col.G())/2, 255-(255-col.B())/2)
		}
		return
	}
	s := pl.Steps[r.step]
	switch {
	case s.Kind == planner.Shoot:
		col := targetColor(s.Target)
		c.BackgroundColor = walk.RGB(255-(255-col.R())/5, 255-(255-col.G())/5, 255-(255-col.B())/5)
	case s.Kind == planner.Wait:
		c.TextColor = walk.RGB(150, 150, 150)
	default:
		c.TextColor = walk.RGB(90, 90, 90)
	}
}
