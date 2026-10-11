package sorter

import (
	"strconv"
	"strings"
	"unicode"
)

// NormObject is an object name for comparing: "M 31", "m31" and "M-31"
// are the same object.
func NormObject(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '\t':
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// NaturalLess compares names with their numbers as numbers.
func NaturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, _ := strconv.Atoi(a[:da])
			nb, _ := strconv.Atoi(b[:db])
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// ObjectsMatching: the objects a text typed in the object box means — the
// one named exactly (ignoring case and spaces), else all whose name holds it.
func ObjectsMatching(all []string, text string) []string {
	q := NormObject(text)
	if q == "" {
		return all
	}
	var exact, part []string
	for _, o := range all {
		n := NormObject(o)
		if n == q {
			exact = append(exact, o)
		} else if strings.Contains(n, q) {
			part = append(part, o)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return part
}
