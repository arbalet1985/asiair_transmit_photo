package satinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake answers as the sources do; down: the sources answer 429.
type fake struct {
	srv                         *httptest.Server
	satcat, satnogs, wiki, comm atomic.Int32
	down                        atomic.Bool
	wikiImage                   bool
}

func pngOf(w, h int) []byte {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, color.NRGBA{200, 100, 50, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "too many", http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/satcat":
			f.satcat.Add(1)
			switch q.Get("CATNR") {
			case "25544":
				w.Write([]byte(`[{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","NORAD_CAT_ID":25544,"OBJECT_TYPE":"PAY","OPS_STATUS_CODE":"+","OWNER":"ISS","LAUNCH_DATE":"1998-11-20","LAUNCH_SITE":"TYMSC","DECAY_DATE":null,"PERIOD":92.9,"INCLINATION":51.64,"APOGEE":420,"PERIGEE":415,"RCS":399.05}]`))
			case "44235":
				w.Write([]byte(`[{"OBJECT_NAME":"STARLINK-1007","OBJECT_ID":"2019-074A","NORAD_CAT_ID":44235,"OBJECT_TYPE":"PAY","OPS_STATUS_CODE":"+","OWNER":"US","LAUNCH_DATE":"2019-11-11","LAUNCH_SITE":"AFETR","PERIOD":95.6,"INCLINATION":53,"APOGEE":550,"PERIGEE":548,"RCS":null}]`))
			default:
				w.Write([]byte("No SATCAT records found"))
			}
		case "/satnogs":
			f.satnogs.Add(1)
			if q.Get("norad_cat_id") == "25544" {
				w.Write([]byte(`[{"sat_id":"XSKZ","norad_cat_id":25544,"name":"ISS","image":"satellites/ISS.png","operator":"None","countries":"RU,US","website":"https://www.nasa.gov/"}]`))
				return
			}
			w.Write([]byte(`[]`))
		case "/media/satellites/ISS.png":
			w.Write(pngOf(300, 200))
		case "/sparql":
			f.wiki.Add(1)
			if !strings.Contains(q.Get("query"), `"25544"`) {
				w.Write([]byte(`{"results":{"bindings":[]}}`))
				return
			}
			img := ""
			if f.wikiImage {
				img = `,"img":{"value":"http://commons.wikimedia.org/wiki/Special:FilePath/The%20station.jpg"}`
			}
			w.Write([]byte(`{"results":{"bindings":[{"i":{"value":"http://www.wikidata.org/entity/Q331785"}},
				{"i":{"value":"http://www.wikidata.org/entity/Q25271"},"ru":{"value":"https://ru.wikipedia.org/wiki/МКС"},"en":{"value":"https://en.wikipedia.org/wiki/ISS"}` + img + `}]}}`))
		case "/commons":
			f.comm.Add(1)
			title := q.Get("titles")
			w.Write([]byte(`{"query":{"pages":{"1":{"imageinfo":[{"thumburl":"` + f.srv.URL + `/thumb.png","descriptionurl":"https://commons/` + title +
				`","extmetadata":{"Artist":{"value":"<a href=\"x\">NASA</a> &amp; crew"},"LicenseShortName":{"value":"Public domain"}}}]}}}}`))
		case "/thumb.png":
			w.Write(pngOf(1280, 720))
		default:
			http.NotFound(w, r)
		}
	}))
	satcatURL, satnogsURL, satnogsImg = f.srv.URL+"/satcat", f.srv.URL+"/satnogs", f.srv.URL+"/media/"
	sparqlURL, commonsAPI = f.srv.URL+"/sparql", f.srv.URL+"/commons"
	t.Cleanup(f.srv.Close)
	return f
}

func TestGet(t *testing.T) {
	f := newFake(t)
	f.wikiImage = true
	s := New(t.TempDir(), "test")
	ctx := context.Background()
	i, err := s.Get(ctx, 25544, "ISS (ZARYA)")
	if err != nil {
		t.Fatal(err)
	}
	if i.Intl != "1998-067A" || i.OwnerText != "МКС (несколько стран)" || i.SiteText != "Байконур" || i.TypeText == "" || i.StatusText != "работает" {
		t.Errorf("satcat %+v", i)
	}
	if i.RCS != 399.05 || i.Countries != "RU,US" || i.Operator != "" {
		t.Errorf("rcs %v, satnogs %q %q", i.RCS, i.Countries, i.Operator)
	}
	if i.Photo == nil || i.Photo.Source != "Wikimedia Commons" || i.Photo.Author != "NASA & crew" || i.Photo.Width != photoWidth || i.Photo.Height != 360 {
		t.Fatalf("photo %+v", i.Photo)
	}
	if !strings.Contains(i.Photo.Page, "File:The station.jpg") {
		t.Errorf("the file of the picture: %s", i.Photo.Page)
	}
	if st, err := os.Stat(s.PhotoPath(25544)); err != nil || st.Size() == 0 {
		t.Errorf("picture not kept: %v", err)
	}
	var names []string
	for _, l := range i.Links {
		names = append(names, l.Name)
	}
	if got := strings.Join(names, ","); got != "N2YO,Heavens-Above,CelesTrak,SatNOGS,Википедия,Wikipedia,Wikidata,Сайт" {
		t.Errorf("links %s", got)
	}
	if len(i.Lines()) < 5 {
		t.Errorf("lines %v", i.Lines())
	}
	// asked once: the second time from the disk
	if _, err := s.Get(ctx, 25544, ""); err != nil || f.satcat.Load() != 1 {
		t.Errorf("asked again: %d, %v", f.satcat.Load(), err)
	}
	// the links are the same read back
	if c := s.Cached(25544); c == nil || len(c.Links) != len(i.Links) {
		t.Errorf("cached links %v", c)
	}
}

func TestPictureFallbacks(t *testing.T) {
	newFake(t)
	s := New(t.TempDir(), "test")
	ctx := context.Background()
	// no picture on Wikidata: that of SatNOGS
	i, err := s.Get(ctx, 25544, "")
	if err != nil {
		t.Fatal(err)
	}
	if i.Photo == nil || i.Photo.Source != "SatNOGS DB" || i.Photo.Width != 300 || i.Photo.Page == "" {
		t.Errorf("satnogs photo %+v", i.Photo)
	}
	// a Starlink: the picture of the type
	i, err = s.Get(ctx, 44235, "STARLINK-1007")
	if err != nil {
		t.Fatal(err)
	}
	if i.Photo == nil || i.Photo.OfType != "Starlink" || !strings.Contains(i.Photo.Credit(), "того же типа") {
		t.Errorf("type photo %+v", i.Photo)
	}
	// nothing known anywhere: still an answer with the links, no picture
	i, err = s.Get(ctx, 99999, "OBJECT X")
	if err != nil {
		t.Fatal(err)
	}
	if i.Photo != nil || i.Name != "OBJECT X" || len(i.Links) != 3 {
		t.Errorf("unknown %+v", i)
	}
}

func TestSourcesDown(t *testing.T) {
	f := newFake(t)
	f.wikiImage = true
	s := New(t.TempDir(), "test")
	ctx := context.Background()
	f.down.Store(true)
	if _, err := s.Get(ctx, 25544, "ISS"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("nothing known: %v", err)
	}
	f.down.Store(false)
	i, err := s.Get(ctx, 25544, "ISS")
	if err != nil || i.Photo == nil {
		t.Fatal(err)
	}
	// old and the sources down: what was known stays, with the reason
	i.Fetched = time.Now().Add(-keepFor - time.Hour)
	b := s.Cached(25544)
	b.Fetched = i.Fetched
	b.Links = nil
	writeJSONFile(t, s, b)
	f.down.Store(true)
	i, err = s.Get(ctx, 25544, "ISS")
	if err != nil {
		t.Fatal(err)
	}
	if i.Intl != "1998-067A" || i.Photo == nil || len(i.Errors) != 3 || !strings.Contains(i.Links[len(i.Links)-1].URL, "nasa") {
		t.Errorf("kept %+v", i)
	}
	if _, err := os.Stat(s.PhotoPath(25544)); err != nil {
		t.Errorf("picture lost: %v", err)
	}
	// asked again only after a while
	n := f.satcat.Load()
	s.Get(ctx, 25544, "ISS")
	if f.satcat.Load() != n {
		t.Error("the sources down are asked again at once")
	}
}

func writeJSONFile(t *testing.T, s *Store, i *Info) {
	t.Helper()
	b := s.Cached(i.NORAD)
	*b = *i
	if err := writeFile(s.infoPath(i.NORAD), mustJSON(t, b)); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
