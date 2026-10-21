package sorter

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A Light frame on disk gives its centre, camera angle and picture.
func TestFramingLocal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Light", "M 31", "Light_M 31_120.0s_Bin1_gain100_20261016-220000_0001.fit")
	writeFITS(t, p, map[string]string{"IMAGETYP": "'Light'", "OBJECT": "'M 31'", "EXPTIME": "120.", "FILTER": "'L'",
		"CRVAL1": "10.68", "CRVAL2": "41.27", "CRPIX1": "2.5", "CRPIX2": "2.5",
		"CD1_1": "-0.001", "CD1_2": "0", "CD2_1": "0", "CD2_2": "-0.001", "ROWORDER": "'TOP-DOWN'"})
	f, err := ReadFrame(p, "Light/M 31/"+filepath.Base(p))
	if err != nil {
		t.Fatal(err)
	}
	fr, err := Framing(context.Background(), &f, "")
	if err != nil {
		t.Fatal(err)
	}
	// rows from the top going south: north is up
	if fr.Name != "M 31" || math.Abs(fr.RA-10.68) > 1e-6 || math.Abs(fr.Dec-41.27) > 1e-6 || math.Min(fr.Angle, 360-fr.Angle) > 0.01 ||
		fr.Exposure != 120 || fr.Filter != "L" || len(fr.JPG) == 0 || fr.Approx || fr.WCS["NAXIS1"] != 4 {
		t.Fatalf("%+v", fr)
	}
	// no coordinates: no framing
	q := filepath.Join(t.TempDir(), "Light_x.fit")
	writeFITS(t, q, map[string]string{"IMAGETYP": "'Light'"})
	g, _ := ReadFrame(q, "Light_x.fit")
	if _, err := Framing(context.Background(), &g, ""); err == nil {
		t.Fatal("framing without coordinates")
	}
}

// The sky of the planner of a receiver: horizon, zones, site, clock.
func TestPlannerSky(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui/api/planner" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"state":{"sky":{"horizon":[{"az":0,"alt":20},{"az":180,"alt":25}],"zones":[{"az1":10,"az2":40,"alt1":0,"alt2":30}],"min_alt":15},
			"sky_set":true,"tz":5},"site":{"lat":56.8,"lon":60.3,"set":true}}`))
	}))
	defer srv.Close()
	r, err := NewReceiver(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sky, err := r.PlannerSky(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sky.Sky.Horizon) != 2 || len(sky.Sky.Zones) != 1 || sky.Sky.MinAlt != 15 || !sky.SiteSet || sky.Lat != 56.8 ||
		sky.TZ == nil || *sky.TZ != 5 || !sky.Edited {
		t.Fatalf("%+v", sky)
	}
}
