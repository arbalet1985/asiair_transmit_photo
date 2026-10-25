// Package catalog finds deep-sky objects by name: M 31, NGC 7000, IC 1805,
// Sh2-274, Caldwell, Barnard, LBN numbers, English and Russian popular
// names («Медуза», «Сердце»). The objects come from OpenNGC (CC-BY-SA 4.0,
// github.com/mattiaverga/OpenNGC) and the Sharpless catalog (VizieR VII/20),
// see gen.go; what is not there can be asked of CDS Sesame (Resolve).
package catalog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:generate go run gen.go

//go:embed objects.tsv.gz
var data []byte

// Object is a deep-sky object.
type Object struct {
	Name    string   // NGC 224, Sh2-274, …
	Kind    string   // OpenNGC type: G, HII, PN, OCl, Neb, …
	RA, Dec float64  // J2000, degrees
	Size    float64  // largest extent, arcminutes (0 = unknown)
	Aliases []string // M 31, Andromeda Galaxy, Андромеда, …
}

// Label is the name a person knows best: the Messier number, else the
// catalog name, with the popular name.
func (o Object) Label() string {
	n := o.Name
	for _, a := range o.Aliases {
		if strings.HasPrefix(a, "M ") {
			n = a
			break
		}
	}
	for _, a := range o.Aliases {
		if a != n && !isID(a) {
			return n + " — " + a
		}
	}
	return n
}

var idRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ]*-?\s*\d+[A-Za-z]?$`)

func isID(s string) bool { return idRe.MatchString(s) }

// extra: what OpenNGC lacks (the Abell planetary nebulae) and the names
// used in Russian, as aliases of catalog objects.
var extra = []Object{
	{Name: "Abell 21", Kind: "PN", RA: 112.261, Dec: 13.247, Size: 10, Aliases: []string{"PN A66 21", "Sh2-274", "Medusa Nebula", "Медуза"}},
	{Name: "Abell 31", Kind: "PN", RA: 133.555, Dec: 8.898, Size: 16, Aliases: []string{"PN A66 31", "Sh2-290"}},
	{Name: "Abell 33", Kind: "PN", RA: 144.788, Dec: -2.809, Size: 4.5, Aliases: []string{"PN A66 33"}},
	{Name: "Abell 39", Kind: "PN", RA: 246.890, Dec: 27.909, Size: 2.9, Aliases: []string{"PN A66 39"}},
	{Name: "Abell 7", Kind: "PN", RA: 75.781, Dec: -15.606, Size: 13, Aliases: []string{"PN A66 7"}},
}

var russian = map[string][]string{
	"NGC 224":  {"Андромеда", "Туманность Андромеды"},
	"NGC 598":  {"Треугольник", "Галактика Треугольника"},
	"NGC 1976": {"Туманность Ориона", "Орион"},
	"Mel 22":   {"Плеяды"},
	"IC 1805":  {"Сердце"},
	"IC 1848":  {"Душа"},
	"NGC 7635": {"Пузырь"},
	"NGC 7000": {"Северная Америка"},
	"IC 5070":  {"Пеликан"},
	"B 33":     {"Конская голова"},
	"NGC 2024": {"Пламя"},
	"NGC 2237": {"Розетка"},
	"NGC 6523": {"Лагуна"},
	"NGC 6514": {"Трёхраздельная"},
	"NGC 6611": {"Орёл"},
	"NGC 6618": {"Омега", "Лебедь (M 17)"},
	"NGC 6853": {"Гантель"},
	"NGC 6720": {"Кольцо"},
	"NGC 6960": {"Западная Вуаль", "Вуаль"},
	"NGC 6992": {"Восточная Вуаль"},
	"NGC 6888": {"Полумесяц"},
	"NGC 7023": {"Ирис"},
	"IC 1396":  {"Слоновий хобот"},
	"NGC 281":  {"Пакман"},
	"NGC 6543": {"Кошачий глаз"},
	"NGC 5194": {"Водоворот"},
	"NGC 3034": {"Сигара"},
	"NGC 3031": {"Галактика Боде"},
	"NGC 5457": {"Вертушка"},
	"NGC 5055": {"Подсолнух"},
	"NGC 4594": {"Сомбреро"},
	"NGC 1952": {"Крабовидная", "Краб"},
	"NGC 1499": {"Калифорния"},
	"NGC 7293": {"Улитка"},
	"NGC 3587": {"Сова"},
	"Sh2-155":  {"Пещера"},
	"Sh2-101":  {"Тюльпан"},
	"IC 410":   {"Головастики"},
	"NGC 1909": {"Голова ведьмы"},
	"NGC 2174": {"Обезьянья голова"},
	"NGC 2264": {"Конус", "Рождественская ёлка"},
	"IC 63":    {"Призрак Кассиопеи"},
	"NGC 6205": {"Шаровое скопление Геркулеса"},
	"NGC 869":  {"Двойное скопление"},
}

var (
	once    sync.Once
	objects []Object
	byName  map[string]int // normalized name or alias → index
)

func load() {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 6 {
			continue
		}
		o := Object{Name: f[0], Kind: f[1]}
		o.RA, _ = strconv.ParseFloat(f[2], 64)
		o.Dec, _ = strconv.ParseFloat(f[3], 64)
		o.Size, _ = strconv.ParseFloat(f[4], 64)
		if f[5] != "" {
			o.Aliases = strings.Split(f[5], "|")
		}
		o.Aliases = append(o.Aliases, russian[o.Name]...)
		objects = append(objects, o)
	}
	objects = append(objects, extra...)
	byName = map[string]int{}
	// later objects do not take a name from earlier ones; the Abell PNs
	// come last but their own names are unique, and Sh2-274 stays the HII
	// entry with the same place
	for i, o := range objects {
		for _, n := range append([]string{o.Name}, o.Aliases...) {
			k := Normalize(n)
			if _, ok := byName[k]; !ok && k != "" {
				byName[k] = i
			}
		}
	}
}

var (
	zerosRe = regexp.MustCompile(`([a-z])0+(\d)`)
	wordsRe = regexp.MustCompile(`(nebula|galaxy|cluster|туманность|туманности|галактика|скопление)`)
)

// Normalize makes a name comparable: lower case, no spaces, hyphens and
// leading zeros, ё as е, without "nebula", "galaxy", "туманность"…
// ("M 031" → "m31", "Sh 2-274" → "sh2274", "Туманность Медуза" → "медуза").
func Normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	s = wordsRe.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '-', '_', '\'', '"', '.', '(', ')', ',':
			continue
		}
		b.WriteRune(r)
	}
	s = b.String()
	s = strings.TrimPrefix(s, "the")
	s = zerosRe.ReplaceAllString(s, "$1$2")
	// "messier31" → "m31", "caldwell20" → "c20"
	for long, short := range map[string]string{"messier": "m", "caldwell": "c", "sharpless": "sh", "barnard": "b"} {
		if strings.HasPrefix(s, long) {
			s = short + s[len(long):]
		}
	}
	return s
}

// Lookup finds an object by any of its names.
func Lookup(name string) (Object, bool) {
	once.Do(load)
	i, ok := byName[Normalize(name)]
	if !ok {
		return Object{}, false
	}
	return objects[i], true
}

// Search: up to n objects with a name that starts with prefix, the
// Messier and best known first.
func Search(prefix string, n int) []Object {
	once.Do(load)
	p := Normalize(prefix)
	if p == "" {
		return nil
	}
	type hit struct {
		i, rank int
	}
	var hits []hit
	for i, o := range objects {
		best := -1
		for j, name := range append([]string{o.Name}, o.Aliases...) {
			k := Normalize(name)
			if !strings.HasPrefix(k, p) {
				continue
			}
			r := 3
			switch {
			case k == p:
				r = 0
			case strings.HasPrefix(name, "M "):
				r = 1
			case j > 0 && !isID(name):
				r = 2 // a popular name
			}
			if best < 0 || r < best {
				best = r
			}
		}
		if best >= 0 {
			hits = append(hits, hit{i, best})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].rank != hits[b].rank {
			return hits[a].rank < hits[b].rank
		}
		return len(objects[hits[a].i].Name) < len(objects[hits[b].i].Name)
	})
	var out []Object
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, objects[h.i])
	}
	return out
}

// All is every object of the catalog.
func All() []Object {
	once.Do(load)
	return objects
}

// Group is what kind of object it is, as the filter of ASIAIR sorts them:
// "galaxy", "nebula", "cluster", or "" for stars and the rest.
func Group(kind string) string {
	switch kind {
	case "G", "GPair", "GTrpl", "GGroup":
		return "galaxy"
	case "HII", "Neb", "EmN", "RfN", "SNR", "PN", "Cl+N", "DrkN":
		return "nebula"
	case "OCl", "GCl", "*Ass":
		return "cluster"
	}
	return ""
}

// Fame: 0 for a Messier object, 1 for a Caldwell one or one with a popular
// name, 2 for the rest.
func (o Object) Fame() int {
	f := 2
	for _, a := range o.Aliases {
		switch {
		case strings.HasPrefix(a, "M "):
			return 0
		case strings.HasPrefix(a, "C ") || !isID(a) && !strings.HasPrefix(a, "PN "):
			f = 1
		}
	}
	return f
}
