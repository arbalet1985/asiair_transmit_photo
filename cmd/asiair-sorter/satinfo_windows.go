package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows"

	"github.com/arbalet1985/asiair_transmit_photo/internal/satinfo"
	"github.com/arbalet1985/asiair_transmit_photo/internal/sorter"
)

// «О спутнике»: what open sources know about a satellite that left a track
// (CelesTrak, SatNOGS, Wikidata): owner, launch, orbit, a picture and the
// pages about it. A frame of a receiver asks the receiver, which keeps the
// answers; a frame on disk (or an older receiver) asks the sources itself.

// satInfoStore: the answers of the sources kept by the sorter.
func satInfoStore() *satinfo.Store {
	return satinfo.New(filepath.Join(configDir(), "satinfo"), satinfo.UserAgent("asiair-sorter"))
}

// frameSats: the satellites of the frame's tracks, each once.
func frameSats(f *sorter.Frame) []*sorter.Satellite {
	var out []*sorter.Satellite
	seen := map[int]bool{}
	if f == nil {
		return nil
	}
	for _, t := range f.Tracks {
		if t.Sat != nil && t.Sat.NORAD > 0 && !seen[t.Sat.NORAD] {
			seen[t.Sat.NORAD] = true
			out = append(out, t.Sat)
		}
	}
	return out
}

// satInfoItems: the lines of the table menu for the satellites of a row.
func (w *window) satInfoItems(i int) []copyItem {
	if i < 0 || i >= len(w.model.rows) {
		return nil
	}
	f := w.model.rows[i].f
	var items []copyItem
	for k, s := range frameSats(f) {
		items = append(items, copyItem{label: "О спутнике " + s.Name + "…", run: func() { w.satInfoDialog(f, k) }})
	}
	return items
}

// fetchSatInfo asks the receiver of the frame, else the sources; photo is
// the picture, nil when none.
func fetchSatInfo(ctx context.Context, f *sorter.Frame, s *sorter.Satellite) (i *satinfo.Info, photo image.Image, err error) {
	if r := f.Src; r != nil {
		if i, err = r.SatInfo(ctx, s.NORAD, s.Name); err == nil {
			if i.Photo != nil {
				if b, err := r.SatPhoto(ctx, s.NORAD); err == nil {
					photo, _, _ = image.Decode(bytes.NewReader(b))
				}
			}
			i.Links = append(satinfo.Links(i.NORAD), extraLinks(i.Links)...)
			return i, photo, nil
		}
		if errors.Is(err, sorter.ErrLogin) || errors.Is(err, context.Canceled) {
			return nil, nil, err
		}
		// an older receiver: the sources themselves
	}
	st := satInfoStore()
	if i, err = st.Get(ctx, s.NORAD, s.Name); err != nil {
		return nil, nil, err
	}
	if i.Photo != nil {
		if b, err := os.ReadFile(st.PhotoPath(s.NORAD)); err == nil {
			photo, _, _ = image.Decode(bytes.NewReader(b))
		}
	}
	return i, photo, nil
}

// extraLinks: the links of an answer beyond those every satellite has.
func extraLinks(links []satinfo.Link) []satinfo.Link {
	common := map[string]bool{}
	for _, l := range satinfo.Links(0) {
		common[l.Name] = true
	}
	var out []satinfo.Link
	for _, l := range links {
		if !common[l.Name] {
			out = append(out, l)
		}
	}
	return out
}

// linkMarkup: a link for a LinkLabel.
func linkMarkup(text, url string) string {
	text = strings.NewReplacer("<", "‹", ">", "›", "&", "&&").Replace(text)
	return fmt.Sprintf(`<a href="%s">%s</a>`, strings.ReplaceAll(url, `"`, "%22"), text)
}

// openURL opens a page in the browser.
func openURL(u string) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return
	}
	windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// satInfoDialog shows the satellites of the frame, the pick-th first.
func (w *window) satInfoDialog(f *sorter.Frame, pick int) {
	sats := frameSats(f)
	if len(sats) == 0 {
		return
	}
	if pick < 0 || pick >= len(sats) {
		pick = 0
	}
	var names []string
	for _, s := range sats {
		names = append(names, fmt.Sprintf("%s (NORAD %d)", s.Name, s.NORAD))
	}
	var dlg *walk.Dialog
	var box *walk.ComboBox
	var img *walk.ImageView
	var credit, links *walk.LinkLabel
	var text *walk.TextEdit
	var status *walk.Label
	var closeBtn *walk.PushButton
	var bmp *walk.Bitmap
	gen, ready := 0, false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onLink := func(l *walk.LinkLabelLink) { openURL(l.URL()) }
	show := func(k int) {
		gen++
		my, s := gen, sats[k]
		img.SetVisible(false)
		credit.SetText("")
		links.SetText(linkMarkup("N2YO", satinfo.Links(s.NORAD)[0].URL) + "   " + linkMarkup("Heavens-Above", satinfo.Links(s.NORAD)[1].URL))
		text.SetText("")
		status.SetText("Ищу сведения в CelesTrak, SatNOGS и Wikidata… (первый раз — до минуты, потом сразу)")
		go func() {
			c, stop := context.WithTimeout(ctx, 2*time.Minute)
			i, photo, err := fetchSatInfo(c, f, s)
			stop()
			dlg.Synchronize(func() {
				if my != gen || ctx.Err() != nil {
					return
				}
				if err != nil {
					status.SetText("Сведения не получены: " + err.Error())
					return
				}
				var t strings.Builder
				t.WriteString(i.Name + "\r\n\r\n")
				for _, r := range i.Lines() {
					t.WriteString(r[0] + ": " + r[1] + "\r\n")
				}
				text.SetText(t.String())
				if bmp != nil {
					bmp.Dispose()
					bmp = nil
				}
				if photo != nil {
					if b, err := walk.NewBitmapFromImageForDPI(photo, 96); err == nil {
						bmp = b
						img.SetImage(b)
						img.SetVisible(true)
					}
				}
				if p := i.Photo; p != nil && photo != nil {
					c := strings.NewReplacer("<", "‹", ">", "›", "&", "&&").Replace(p.Credit())
					if p.Page != "" {
						c += " — " + linkMarkup("страница фото", p.Page)
					}
					credit.SetText("Фото: " + c)
				} else {
					credit.SetText("Фотографии в открытых источниках нет.")
				}
				var ls []string
				for _, l := range i.Links {
					ls = append(ls, linkMarkup(l.Name, l.URL))
				}
				links.SetText(strings.Join(ls, "   "))
				st := ""
				if len(i.Errors) > 0 {
					st = "Не ответили (спросим позже): " + strings.Join(i.Errors, "; ")
				}
				status.SetText(st)
			})
		}()
	}
	err := Dialog{
		AssignTo:     &dlg,
		Title:        "О спутнике",
		CancelButton: &closeBtn,
		MinSize:      Size{Width: 600, Height: 560},
		Layout:       VBox{},
		Children: []Widget{
			Composite{
				Layout:  HBox{MarginsZero: true},
				Visible: len(sats) > 1,
				Children: []Widget{
					Label{Text: "Спутник кадра:"},
					ComboBox{AssignTo: &box, Model: names, CurrentIndex: pick, OnCurrentIndexChanged: func() {
						if ready && box.CurrentIndex() >= 0 {
							show(box.CurrentIndex())
						}
					}},
					HSpacer{},
				},
			},
			ImageView{AssignTo: &img, Mode: ImageViewModeShrink, MinSize: Size{Width: 480, Height: 270}, Visible: false},
			LinkLabel{AssignTo: &credit, OnLinkActivated: onLink},
			TextEdit{AssignTo: &text, ReadOnly: true, VScroll: true, MinSize: Size{Height: 150}},
			LinkLabel{AssignTo: &links, OnLinkActivated: onLink},
			Label{AssignTo: &status, TextColor: walk.RGB(110, 110, 110)},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "Сведения: CelesTrak SATCAT, SatNOGS DB, Wikidata; фото — Wikimedia Commons или SatNOGS DB.", TextColor: walk.RGB(120, 120, 120)},
					HSpacer{},
					PushButton{AssignTo: &closeBtn, Text: "Закрыть", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(w.mw)
	if err != nil {
		w.warn(err.Error())
		return
	}
	ready = true
	show(pick)
	dlg.Run()
	if bmp != nil {
		bmp.Dispose()
	}
}
