package catalog

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Resolve finds an object missing in the catalog through CDS Sesame
// (SIMBAD, NED, VizieR) over the internet. The object found has no type.
func Resolve(ctx context.Context, name string) (Object, error) {
	if o, ok := Lookup(name); ok {
		return o, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://cds.unistra.fr/cgi-bin/nph-sesame/-oI/SNV?"+url.PathEscape(strings.TrimSpace(name)), nil)
	if err != nil {
		return Object{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Object{}, fmt.Errorf("Sesame: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Object{}, fmt.Errorf("Sesame: %s", resp.Status)
	}
	return parseSesame(name, bufio.NewScanner(resp.Body))
}

func parseSesame(name string, sc *bufio.Scanner) (Object, error) {
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[0] == "%J" {
			ra, err1 := strconv.ParseFloat(f[1], 64)
			dec, err2 := strconv.ParseFloat(f[2], 64)
			if err1 == nil && err2 == nil {
				return Object{Name: strings.TrimSpace(name), RA: ra, Dec: dec}, nil
			}
		}
	}
	return Object{}, fmt.Errorf("объект «%s» не найден ни в каталоге, ни в SIMBAD", name)
}
