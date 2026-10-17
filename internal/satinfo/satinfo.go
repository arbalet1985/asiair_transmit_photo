// Package satinfo tells about a satellite by its NORAD number what open
// sources know: the CelesTrak satellite catalogue (SATCAT: owner, launch,
// orbit, kind of object), SatNOGS DB (operator, site, a picture of many
// small satellites) and Wikidata with Wikimedia Commons (pictures and the
// Wikipedia articles of the better known ones). A satellite of a big
// constellation without a picture of its own gets a picture of its type.
//
// What is found is kept on disk, the picture beside it, so that every
// satellite is asked about once a month at most: the sources limit how
// often they may be asked.
package satinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // pictures of SatNOGS
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arbalet1985/asiair_transmit_photo/internal/protocol"
)

// Info is what is known about a satellite.
type Info struct {
	NORAD      int     `json:"norad"`
	Name       string  `json:"name,omitempty"`
	Intl       string  `json:"intl,omitempty"`        // international designator, 1998-067A
	Type       string  `json:"type,omitempty"`        // SATCAT: PAY, R/B, DEB, UNK
	TypeText   string  `json:"type_text,omitempty"`   // in words
	Status     string  `json:"status,omitempty"`      // SATCAT: +, -, P, B, S, X, D, ?
	StatusText string  `json:"status_text,omitempty"` // in words
	Owner      string  `json:"owner,omitempty"`       // SATCAT code: US, CIS, PRC…
	OwnerText  string  `json:"owner_text,omitempty"`  // in words
	Launch     string  `json:"launch,omitempty"`      // YYYY-MM-DD
	Site       string  `json:"site,omitempty"`        // SATCAT code of the launch site
	SiteText   string  `json:"site_text,omitempty"`
	Decay      string  `json:"decay,omitempty"`  // YYYY-MM-DD: no more in orbit
	Period     float64 `json:"period,omitempty"` // minutes
	Incl       float64 `json:"incl,omitempty"`   // degrees
	Apogee     float64 `json:"apogee,omitempty"` // km
	Perigee    float64 `json:"perigee,omitempty"`
	RCS        float64 `json:"rcs,omitempty"` // radar cross-section, m²
	// SatNOGS DB
	Operator  string `json:"operator,omitempty"`
	Countries string `json:"countries,omitempty"`
	Website   string `json:"website,omitempty"`
	// the picture, nil when none was found
	Photo *Photo `json:"photo,omitempty"`
	Links []Link `json:"links"`
	// Errors: the sources not reached (they are asked again later)
	Errors  []string  `json:"errors,omitempty"`
	Fetched time.Time `json:"fetched"`
}

// Photo is the picture of a satellite (or of its type) and where it is from.
type Photo struct {
	Source  string `json:"source"`            // Wikimedia Commons, SatNOGS DB
	Page    string `json:"page,omitempty"`    // the page of the picture
	Author  string `json:"author,omitempty"`  // as the source writes it
	License string `json:"license,omitempty"` // CC BY-SA 4.0, Public domain…
	// OfType: the picture shows a satellite of the same type, named here
	OfType string `json:"of_type,omitempty"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Link is a page about the satellite.
type Link struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Links: the pages that know any satellite by its NORAD number.
func Links(norad int) []Link {
	n := strconv.Itoa(norad)
	return []Link{
		{"N2YO", "https://www.n2yo.com/satellite/?s=" + n},
		{"Heavens-Above", "https://www.heavens-above.com/SatInfo.aspx?satid=" + n},
		{"CelesTrak", "https://celestrak.org/satcat/table-satcat.php?CATNR=" + n},
	}
}

// The sources; tests point them elsewhere.
var (
	satcatURL  = "https://celestrak.org/satcat/records.php"
	satnogsURL = "https://db.satnogs.org/api/satellites/"
	satnogsWeb = "https://db.satnogs.org/satellite/"
	satnogsImg = "https://db-satnogs.freetls.fastly.net/media/"
	sparqlURL  = "https://query.wikidata.org/sparql"
	commonsAPI = "https://commons.wikimedia.org/w/api.php"
)

const (
	keepFor      = 30 * 24 * time.Hour // what was found is asked again after
	retryAfter   = 6 * time.Hour       // a source not reached is asked again after
	photoWidth   = 640                 // the picture kept, pixels wide at most
	maxPhotoSize = 8 << 20
)

// ofType: a picture of the type for the satellites of a constellation that
// have none of their own, by the start of the name. Only pictures checked
// to be free to show: a file of Wikimedia Commons.
var ofType = []struct{ prefix, name, file string }{
	{"STARLINK", "Starlink", "Starlink Mission (47926144123).jpg"},
}

// Store finds and keeps what is known about satellites.
type Store struct {
	dir       string
	client    *http.Client
	userAgent string

	mu   sync.Mutex
	busy map[int]*call
}

type call struct {
	done chan struct{}
	info *Info
	err  error
}

// New keeps the information in dir; userAgent names the program to the
// sources (Wikimedia asks for a contact in it).
func New(dir, userAgent string) *Store {
	return &Store{dir: dir, userAgent: userAgent, client: &http.Client{Timeout: 40 * time.Second}, busy: map[int]*call{}}
}

func (s *Store) infoPath(norad int) string {
	return filepath.Join(s.dir, strconv.Itoa(norad)+".json")
}

// PhotoPath is the file of the picture of the satellite (JPEG), when it
// has one.
func (s *Store) PhotoPath(norad int) string {
	return filepath.Join(s.dir, strconv.Itoa(norad)+".jpg")
}

// Cached is what was found before, nil when nothing.
func (s *Store) Cached(norad int) *Info {
	b, err := os.ReadFile(s.infoPath(norad))
	if err != nil {
		return nil
	}
	var i Info
	if json.Unmarshal(b, &i) != nil || i.NORAD != norad {
		return nil
	}
	i.Links = linksOf(&i)
	return &i
}

// stale: the information is to be asked again.
func stale(i *Info, now time.Time) bool {
	age := now.Sub(i.Fetched)
	return age > keepFor || len(i.Errors) > 0 && age > retryAfter
}

// Get tells about the satellite: what was found before, asked again when
// old. name is the name of the catalogue of orbits (for the picture of
// the type). err only when nothing is known at all.
func (s *Store) Get(ctx context.Context, norad int, name string) (*Info, error) {
	if norad <= 0 {
		return nil, errors.New("нет номера NORAD")
	}
	old := s.Cached(norad)
	if old != nil && !stale(old, time.Now()) {
		return old, nil
	}
	s.mu.Lock()
	c := s.busy[norad]
	if c == nil {
		c = &call{done: make(chan struct{})}
		s.busy[norad] = c
		s.mu.Unlock()
		// asked once for all who wait, and not stopped by the first who leaves
		go func() {
			c.info, c.err = s.refresh(norad, name, old)
			s.mu.Lock()
			delete(s.busy, norad)
			s.mu.Unlock()
			close(c.done)
		}()
	} else {
		s.mu.Unlock()
	}
	select {
	case <-c.done:
		return c.info, c.err
	case <-ctx.Done():
		if old != nil {
			return old, nil
		}
		return nil, ctx.Err()
	}
}

// refresh asks the sources and keeps the answer. A source not reached
// keeps what it told before.
func (s *Store) refresh(norad int, name string, old *Info) (*Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	i := &Info{NORAD: norad, Name: name, Fetched: time.Now().UTC()}
	var errs []string
	fail := func(src string, err error) { errs = append(errs, src+": "+shortErr(err)) }
	found := false
	if err := s.satcat(ctx, i); err != nil {
		fail("CelesTrak", err)
		if old != nil {
			copySatcat(i, old)
		}
	} else {
		found = true
	}
	var nogsPhoto string
	if p, err := s.satnogs(ctx, i); err != nil {
		fail("SatNOGS", err)
		if old != nil {
			i.Operator, i.Countries, i.Website = old.Operator, old.Countries, old.Website
			if l := old.satnogsPage(); l != "" {
				i.Links = append(i.Links, Link{"SatNOGS", l})
			}
		}
	} else {
		found = true
		nogsPhoto = p
	}
	wiki, err := s.wikidata(ctx, i)
	if err != nil {
		fail("Wikidata", err)
		if old != nil {
			i.Links = append(i.Links, old.wikiLinks()...)
		}
	} else {
		found = true
	}
	// the picture: its own on Wikimedia Commons, else on SatNOGS, else of its type
	var pic []byte
	var photo *Photo
	if wiki != "" {
		if photo, pic, err = s.commonsPhoto(ctx, wiki); err != nil {
			fail("Wikimedia Commons", err)
		}
	}
	if pic == nil && nogsPhoto != "" {
		if pic, err = s.download(ctx, nogsPhoto); err != nil {
			fail("SatNOGS (картинка)", err)
		} else {
			photo = &Photo{Source: "SatNOGS DB", Page: i.satnogsPage(), License: "CC BY-SA 4.0"}
		}
	}
	if pic == nil {
		if t, file := typeOf(i.Name); file != "" {
			if photo, pic, err = s.commonsPhoto(ctx, file); err != nil {
				fail("Wikimedia Commons", err)
			} else {
				photo.OfType = t
			}
		}
	}
	if pic != nil {
		w, h, data, err := normalize(pic)
		if err != nil {
			pic = nil
			fail("картинка", err)
		} else {
			photo.Width, photo.Height = w, h
			if err := writeFile(s.PhotoPath(norad), data); err != nil {
				return nil, err
			}
			i.Photo = photo
		}
	}
	if i.Photo == nil && old != nil && old.Photo != nil && len(errs) > 0 {
		i.Photo = old.Photo // a source failed this time: the picture kept stays
	}
	if i.Photo == nil {
		os.Remove(s.PhotoPath(norad))
	}
	if !found && old == nil {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	i.Errors = errs
	i.texts()
	b, err := json.MarshalIndent(i, "", " ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(s.infoPath(norad), b); err != nil {
		return nil, err
	}
	i.Links = linksOf(i)
	return i, nil
}

// wikiLinks: the links found on Wikidata (kept with the information).
func (i *Info) wikiLinks() []Link {
	var out []Link
	for _, l := range i.Links {
		if strings.Contains(l.URL, "wikipedia.org") || strings.Contains(l.URL, "wikidata.org") {
			out = append(out, l)
		}
	}
	return out
}

func (i *Info) satnogsPage() string {
	for _, l := range i.Links {
		if strings.HasPrefix(l.URL, satnogsWeb) {
			return l.URL
		}
	}
	return ""
}

// linksOf: the pages of the satellite: those found, then those that know
// every satellite.
func linksOf(i *Info) []Link {
	out := Links(i.NORAD)
	seen := map[string]bool{}
	for _, l := range out {
		seen[l.URL] = true
	}
	for _, l := range i.Links {
		if !seen[l.URL] {
			seen[l.URL] = true
			out = append(out, l)
		}
	}
	if i.Website != "" && !seen[i.Website] {
		out = append(out, Link{"Сайт", i.Website})
	}
	return out
}

func copySatcat(i, old *Info) {
	i.Name, i.Intl, i.Type, i.Status, i.Owner = old.Name, old.Intl, old.Type, old.Status, old.Owner
	i.Launch, i.Site, i.Decay, i.Period, i.Incl = old.Launch, old.Site, old.Decay, old.Period, old.Incl
	i.Apogee, i.Perigee, i.RCS = old.Apogee, old.Perigee, old.RCS
}

// typeOf: the type of a satellite of a constellation and its picture.
func typeOf(name string) (string, string) {
	n := strings.ToUpper(strings.TrimSpace(name))
	for _, t := range ofType {
		if strings.HasPrefix(n, t.prefix) {
			return t.name, t.file
		}
	}
	return "", ""
}

func (s *Store) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("просит спрашивать реже (429), спросим позже")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ответ %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxPhotoSize))
}

func (s *Store) getJSON(ctx context.Context, u string, v any) error {
	b, err := s.get(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		t := strings.TrimSpace(string(b))
		if len(t) > 80 {
			t = t[:80]
		}
		return fmt.Errorf("ответ не JSON: %q", t)
	}
	return nil
}

func (s *Store) download(ctx context.Context, u string) ([]byte, error) { return s.get(ctx, u) }

// satcat reads the record of the CelesTrak satellite catalogue.
func (s *Store) satcat(ctx context.Context, i *Info) error {
	q := url.Values{"CATNR": {strconv.Itoa(i.NORAD)}, "FORMAT": {"JSON"}}
	b, err := s.get(ctx, satcatURL+"?"+q.Encode())
	if err != nil {
		return err
	}
	var recs []struct {
		Name    string   `json:"OBJECT_NAME"`
		ID      string   `json:"OBJECT_ID"`
		NORAD   int      `json:"NORAD_CAT_ID"`
		Type    string   `json:"OBJECT_TYPE"`
		Status  string   `json:"OPS_STATUS_CODE"`
		Owner   string   `json:"OWNER"`
		Launch  string   `json:"LAUNCH_DATE"`
		Site    string   `json:"LAUNCH_SITE"`
		Decay   *string  `json:"DECAY_DATE"`
		Period  *float64 `json:"PERIOD"`
		Incl    *float64 `json:"INCLINATION"`
		Apogee  *float64 `json:"APOGEE"`
		Perigee *float64 `json:"PERIGEE"`
		RCS     *float64 `json:"RCS"`
	}
	if json.Unmarshal(b, &recs) != nil {
		// "No SATCAT records found": not in the catalogue
		if bytes.Contains(bytes.ToLower(b), []byte("no ")) {
			return nil
		}
		return errors.New("ответ не прочитан")
	}
	for _, r := range recs {
		if r.NORAD != i.NORAD {
			continue
		}
		if r.Name != "" {
			i.Name = r.Name
		}
		i.Intl, i.Type, i.Status, i.Owner = r.ID, r.Type, r.Status, r.Owner
		i.Launch, i.Site = r.Launch, r.Site
		f := func(p *float64) float64 {
			if p == nil {
				return 0
			}
			return *p
		}
		if r.Decay != nil {
			i.Decay = *r.Decay
		}
		i.Period, i.Incl, i.Apogee, i.Perigee, i.RCS = f(r.Period), f(r.Incl), f(r.Apogee), f(r.Perigee), f(r.RCS)
	}
	return nil
}

// satnogs reads SatNOGS DB; picture: the address of its picture, if any.
func (s *Store) satnogs(ctx context.Context, i *Info) (picture string, err error) {
	q := url.Values{"norad_cat_id": {strconv.Itoa(i.NORAD)}, "format": {"json"}}
	var recs []struct {
		ID        string `json:"sat_id"`
		NORAD     int    `json:"norad_cat_id"`
		Name      string `json:"name"`
		Image     string `json:"image"`
		Operator  string `json:"operator"`
		Countries string `json:"countries"`
		Website   string `json:"website"`
		Launched  string `json:"launched"`
	}
	if err := s.getJSON(ctx, satnogsURL+"?"+q.Encode(), &recs); err != nil {
		return "", err
	}
	for _, r := range recs {
		if r.NORAD != i.NORAD {
			continue
		}
		if r.Operator != "None" {
			i.Operator = r.Operator
		}
		i.Countries, i.Website = r.Countries, r.Website
		if i.Name == "" {
			i.Name = r.Name
		}
		if i.Launch == "" && len(r.Launched) >= 10 {
			i.Launch = r.Launched[:10]
		}
		if r.ID != "" {
			i.Links = append(i.Links, Link{"SatNOGS", satnogsWeb + url.PathEscape(r.ID)})
		}
		if r.Image != "" {
			picture = r.Image
			if !strings.HasPrefix(picture, "http") {
				picture = satnogsImg + strings.TrimLeft(picture, "/")
			}
		}
		break
	}
	return picture, nil
}

// wikidata finds the satellite by its NORAD number: its picture (a file
// of Wikimedia Commons) and its articles.
func (s *Store) wikidata(ctx context.Context, i *Info) (picture string, err error) {
	ids := fmt.Sprintf("%q", strconv.Itoa(i.NORAD))
	if i.NORAD < 10000 {
		ids += fmt.Sprintf(" %q", fmt.Sprintf("%05d", i.NORAD))
	}
	query := `SELECT ?i ?img ?ru ?en WHERE { VALUES ?n { ` + ids + ` } ?i wdt:P377 ?n .
 OPTIONAL { ?i wdt:P18 ?img }
 OPTIONAL { ?ru schema:about ?i; schema:isPartOf <https://ru.wikipedia.org/> }
 OPTIONAL { ?en schema:about ?i; schema:isPartOf <https://en.wikipedia.org/> } }`
	var res struct {
		Results struct {
			Bindings []map[string]struct {
				Value string `json:"value"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := s.getJSON(ctx, sparqlURL+"?"+url.Values{"format": {"json"}, "query": {query}}.Encode(), &res); err != nil {
		return "", err
	}
	// one number may name several items (the station and its first
	// module): the one told best, with a picture and articles
	best, score := -1, -1
	for k, b := range res.Results.Bindings {
		n := 0
		for _, f := range []string{"img", "ru", "en"} {
			if b[f].Value != "" {
				n++
			}
		}
		if n > score {
			best, score = k, n
		}
	}
	if best < 0 {
		return "", nil
	}
	b := res.Results.Bindings[best]
	if b["img"].Value != "" {
		picture = commonsFile(b["img"].Value)
	}
	for _, l := range []Link{{"Википедия", b["ru"].Value}, {"Wikipedia", b["en"].Value}, {"Wikidata", b["i"].Value}} {
		if l.URL != "" {
			i.Links = append(i.Links, l)
		}
	}
	return picture, nil
}

// commonsFile: the name of the file in a link to Special:FilePath.
func commonsFile(u string) string {
	if k := strings.LastIndex(u, "/"); k >= 0 {
		u = u[k+1:]
	}
	if n, err := url.PathUnescape(u); err == nil {
		return n
	}
	return u
}

var tags = regexp.MustCompile(`<[^>]*>`)

// plain: an HTML snippet of Commons as text.
func plain(s string) string {
	s = tags.ReplaceAllString(s, "")
	for _, r := range [][2]string{{"&amp;", "&"}, {"&quot;", `"`}, {"&#039;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&nbsp;", " "}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return strings.Join(strings.Fields(s), " ")
}

// commonsPhoto reads a file of Wikimedia Commons, made smaller there, with
// its author and license.
func (s *Store) commonsPhoto(ctx context.Context, file string) (*Photo, []byte, error) {
	q := url.Values{"action": {"query"}, "format": {"json"}, "titles": {"File:" + file}, "prop": {"imageinfo"},
		"iiprop": {"url|extmetadata"}, "iiurlwidth": {strconv.Itoa(photoWidth)}}
	var res struct {
		Query struct {
			Pages map[string]struct {
				ImageInfo []struct {
					Thumb string `json:"thumburl"`
					URL   string `json:"url"`
					Page  string `json:"descriptionurl"`
					Meta  map[string]struct {
						Value any `json:"value"`
					} `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := s.getJSON(ctx, commonsAPI+"?"+q.Encode(), &res); err != nil {
		return nil, nil, err
	}
	for _, p := range res.Query.Pages {
		if len(p.ImageInfo) == 0 {
			continue
		}
		ii := p.ImageInfo[0]
		meta := func(k string) string {
			if v, ok := ii.Meta[k].Value.(string); ok {
				return plain(v)
			}
			return ""
		}
		u := ii.Thumb
		if u == "" {
			u = ii.URL
		}
		b, err := s.download(ctx, u)
		if err != nil {
			return nil, nil, err
		}
		author := meta("Artist")
		if author == "" {
			author = meta("Credit")
		}
		if len([]rune(author)) > 120 {
			author = string([]rune(author)[:120]) + "…"
		}
		return &Photo{Source: "Wikimedia Commons", Page: ii.Page, Author: author, License: meta("LicenseShortName")}, b, nil
	}
	return nil, nil, errors.New("файла нет: " + file)
}

// normalize: the picture as a JPEG at most photoWidth wide.
func normalize(b []byte) (w, h int, out []byte, err error) {
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("не прочитана: %w", err)
	}
	r := im.Bounds()
	w, h = r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return 0, 0, nil, errors.New("пустая")
	}
	if w > photoWidth {
		nw, nh := photoWidth, max(1, h*photoWidth/w)
		im = shrink(im, nw, nh)
		w, h = nw, nh
	} else {
		// the transparent parts of a PNG white, not black
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
		draw.Draw(dst, dst.Bounds(), im, r.Min, draw.Over)
		im = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 88}); err != nil {
		return 0, 0, nil, err
	}
	return w, h, buf.Bytes(), nil
}

// shrink averages the pixels of the picture into w×h (on white).
func shrink(src image.Image, w, h int) image.Image {
	r := src.Bounds()
	sw, sh := r.Dx(), r.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := r.Min.Y+y*sh/h, r.Min.Y+max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := r.Min.X+x*sw/w, r.Min.X+max((x+1)*sw/w, x*sw/w+1)
			var rs, gs, bs, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					// on white: what is transparent is white
					rs += uint64(cr + 0xffff - ca)
					gs += uint64(cg + 0xffff - ca)
					bs += uint64(cb + 0xffff - ca)
					n++
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o] = uint8(rs / n >> 8)
			dst.Pix[o+1] = uint8(gs / n >> 8)
			dst.Pix[o+2] = uint8(bs / n >> 8)
			dst.Pix[o+3] = 255
		}
	}
	return dst
}

// writeFile writes through a temporary file, so a file is whole or absent.
func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// shortErr: an error of the network without the long address in it.
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "не ответил вовремя"
	}
	return err.Error()
}

// UserAgent names the program to the sources, with where to find it
// (Wikimedia asks for a way to reach the author).
func UserAgent(program string) string {
	return program + "/" + version() + " (https://github.com/arbalet1985/asiair_transmit_photo)"
}

func version() string { return protocol.Version }
