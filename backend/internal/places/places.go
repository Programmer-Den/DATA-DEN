// Package places names the town nearest to a position, offline, so the
// positions of the photos never leave the NAS.
//
// places.tsv is the Natural Earth "populated places" list (1:10m, public
// domain, about 7300 towns with the German name where one exists): name,
// latitude and longitude, one town per line. It is coarse: in Germany it holds
// the larger cities only, so a photo from a village is named after the city
// nearby.
package places

import (
	"cmp"
	_ "embed"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxKm is how far a town may be from a position to name it.
const MaxKm = 150

//go:embed places.tsv
var list string

type town struct {
	name     string
	lat, lon float64
}

var (
	once  sync.Once
	towns []town
)

func load() {
	for line := range strings.Lines(list) {
		fields := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if len(fields) != 3 {
			continue
		}
		lat, err1 := strconv.ParseFloat(fields[1], 64)
		lon, err2 := strconv.ParseFloat(fields[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		towns = append(towns, town{fields[0], lat, lon})
	}
	// Sorted by latitude, so Nearest only looks at the towns close enough
	// to the north and south.
	slices.SortFunc(towns, func(a, b town) int { return cmp.Compare(a.lat, b.lat) })
}

// kmPerDegree is the length of a degree of latitude.
const kmPerDegree = 111.2

// Nearest is the name of the town nearest to lat, lon, "" when none is within
// MaxKm.
func Nearest(lat, lon float64) string {
	once.Do(load)
	best, bestKm := "", float64(MaxKm)
	band := MaxKm / kmPerDegree
	start := sort.Search(len(towns), func(i int) bool { return towns[i].lat >= lat-band })
	for _, t := range towns[start:] {
		if t.lat > lat+band {
			break
		}
		if km := distance(lat, lon, t.lat, t.lon); km <= bestKm {
			best, bestKm = t.name, km
		}
	}
	return best
}

// distance is the great-circle distance in km.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthKm * math.Asin(math.Sqrt(min(1, a)))
}
