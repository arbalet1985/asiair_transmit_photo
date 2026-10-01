//go:build ignore

// gen.go makes objects.tsv.gz, the catalog embedded in the package, from
// open catalogs:
//
//   - OpenNGC (https://github.com/mattiaverga/OpenNGC, CC-BY-SA 4.0): NGC,
//     IC, Messier, Caldwell and the addendum (Barnard 33 and others);
//   - the Sharpless catalog of HII regions, Sh2 (VizieR VII/20).
//
// Run it with `go generate ./internal/catalog`.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	ngcURL = "https://raw.githubusercontent.com/mattiaverga/OpenNGC/master/database_files/NGC.csv"
	addURL = "https://raw.githubusercontent.com/mattiaverga/OpenNGC/master/database_files/addendum.csv"
	sh2URL = "https://vizier.cds.unistra.fr/viz-bin/asu-tsv?-source=VII/20/catalog&-out=Sh2,Diam,_RAJ2000,_DEJ2000&-out.max=unlimited"
)

func fetch(u string) []byte {
	resp, err := http.Get(u)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Fatalf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	return b
}

type row struct {
	name, kind    string
	aliases       []string
	ra, dec, size float64
}

// the identifiers kept as aliases (the rest — PGC, 2MASX… — only weigh)
var keepID = regexp.MustCompile(`^(C|LBN|LDN|SH 2-|Mel|Cr|B|Abell|PN A66|vdB|Ced|Sh2-)\s*\d`)

// sexagesimal "hh:mm:ss.s" or "+dd:mm:ss" to a number
func sexa(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := strings.HasPrefix(s, "-")
	parts := strings.Split(strings.TrimLeft(s, "+-"), ":")
	v := 0.0
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		v += f / float64([]int{1, 60, 3600}[i])
	}
	if neg {
		v = -v
	}
	return v, true
}

// openNGC reads NGC.csv or addendum.csv.
func openNGC(b []byte) []row {
	var out []row
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var col map[string]int
	for sc.Scan() {
		f := strings.Split(sc.Text(), ";")
		if col == nil {
			col = map[string]int{}
			for i, n := range f {
				col[n] = i
			}
			continue
		}
		get := func(n string) string {
			if i, ok := col[n]; ok && i < len(f) {
				return strings.TrimSpace(f[i])
			}
			return ""
		}
		kind := get("Type")
		if kind == "Dup" || kind == "NonEx" {
			continue
		}
		ra, ok1 := sexa(get("RA"))
		dec, ok2 := sexa(get("Dec"))
		if !ok1 || !ok2 {
			continue
		}
		r := row{name: pretty(get("Name")), kind: kind, ra: ra * 15, dec: dec}
		r.size, _ = strconv.ParseFloat(get("MajAx"), 64)
		if m := get("M"); m != "" {
			n, _ := strconv.Atoi(m)
			r.aliases = append(r.aliases, fmt.Sprintf("M %d", n))
		}
		for _, id := range strings.Split(get("Identifiers"), ",") {
			if id = strings.TrimSpace(id); keepID.MatchString(id) {
				r.aliases = append(r.aliases, id)
			}
		}
		for _, n := range strings.Split(get("Common names"), ",") {
			if n = strings.TrimSpace(n); n != "" {
				r.aliases = append(r.aliases, n)
			}
		}
		out = append(out, r)
	}
	return out
}

// pretty: NGC0224 → NGC 224, IC0001 → IC 1, B033 → B 33, C009 → C 9.
func pretty(n string) string {
	m := regexp.MustCompile(`^([A-Za-z]+)0*(\d+.*)$`).FindStringSubmatch(n)
	if m == nil {
		return n
	}
	return m[1] + " " + m[2]
}

func sharpless(b []byte) []row {
	var out []row
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 4 || strings.HasPrefix(l, "#") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		ra, err1 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		dec, err2 := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		size, _ := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		out = append(out, row{name: fmt.Sprintf("Sh2-%d", n), kind: "HII", ra: ra, dec: dec, size: size,
			aliases: []string{fmt.Sprintf("SH 2-%d", n)}})
	}
	return out
}

func main() {
	rows := openNGC(fetch(ngcURL))
	rows = append(rows, openNGC(fetch(addURL))...)
	rows = append(rows, sharpless(fetch(sh2URL))...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	fmt.Fprintln(zw, "# OpenNGC (CC-BY-SA 4.0, github.com/mattiaverga/OpenNGC) and Sh2 (VizieR VII/20): name, type, RA°, Dec°, size′, aliases")
	for _, r := range rows {
		fmt.Fprintf(zw, "%s\t%s\t%.5f\t%.5f\t%g\t%s\n", r.name, r.kind, r.ra, r.dec, r.size, strings.Join(r.aliases, "|"))
	}
	zw.Close()
	if err := os.WriteFile("objects.tsv.gz", buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("%d objects, %d bytes", len(rows), buf.Len())
}
