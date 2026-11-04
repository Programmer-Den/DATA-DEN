// Package places names the town where a photo was taken, from its GPS
// position, with the reverse geocoding of OpenStreetMap's Nominatim.
//
// Positions are rounded to 0.01° (about a kilometre) before they are sent,
// each is asked for once, and the answers are kept in a file, so a photo's
// position leaves the NAS at most once and only roughly. Nominatim's usage
// policy allows one request per second with a name for the program; the
// Geocoder keeps to it and asks in the background, so a new photo gets its
// town a moment after it is first listed.
package places

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is OpenStreetMap's own Nominatim.
const DefaultURL = "https://nominatim.openstreetmap.org"

const (
	// retryAfter is how long a position waits after a failed request.
	retryAfter = 10 * time.Minute
	// maxQueue is how many positions wait at most; more are asked for on a
	// later listing.
	maxQueue = 10_000
	// saveEvery is how often new answers are written to the file.
	saveEvery = time.Minute
)

// Config is how the Geocoder asks and where it keeps the answers.
type Config struct {
	// URL is the Nominatim server, such as DefaultURL.
	URL string
	// UserAgent names the program, as Nominatim's policy asks.
	UserAgent string
	// File keeps the answers between starts; "" keeps them in memory only.
	File string
	// Client sends the requests; nil uses one with a timeout.
	Client *http.Client
	// Interval is the time between two requests, 0 for one second.
	Interval time.Duration
}

// Geocoder names positions, from its file or by asking Nominatim.
type Geocoder struct {
	cfg Config
	mu  sync.Mutex
	// names by rounded position, "" for a position without a town (at sea).
	names map[string]string
	// waiting are the positions queued or being asked for.
	waiting map[string]bool
	// failed are positions not to ask for again before the time.
	failed map[string]time.Time
	queue  chan string
	dirty  bool
}

// New returns a Geocoder; Run must run for it to ask.
func New(cfg Config) *Geocoder {
	if cfg.Interval == 0 {
		cfg.Interval = time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 20 * time.Second}
	}
	g := &Geocoder{
		cfg: cfg, names: map[string]string{}, waiting: map[string]bool{},
		failed: map[string]time.Time{}, queue: make(chan string, maxQueue),
	}
	g.load()
	return g
}

// key rounds a position to 0.01°, "52.52,13.41".
func key(lat, lon float64) string {
	round := func(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64) }
	return round(lat) + "," + round(lon)
}

// Name is the town at lat, lon, "" when it is not known (yet). An unknown
// position is queued to be asked for.
func (g *Geocoder) Name(lat, lon float64) string {
	k := key(lat, lon)
	g.mu.Lock()
	defer g.mu.Unlock()
	if name, ok := g.names[k]; ok {
		return name
	}
	if g.waiting[k] || time.Now().Before(g.failed[k]) {
		return ""
	}
	select {
	case g.queue <- k:
		g.waiting[k] = true
	default:
	}
	return ""
}

// Run asks for the queued positions, one per Interval, until ctx is done,
// and keeps the answers in the file.
func (g *Geocoder) Run(ctx context.Context) {
	save := time.NewTicker(saveEvery)
	defer save.Stop()
	defer g.save()
	next := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-save.C:
			g.save()
		case k := <-g.queue:
			if wait := time.Until(next); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			next = time.Now().Add(g.cfg.Interval)
			name, err := g.ask(ctx, k)
			g.mu.Lock()
			delete(g.waiting, k)
			if err != nil {
				g.failed[k] = time.Now().Add(retryAfter)
			} else {
				g.names[k] = name
				g.dirty = true
			}
			g.mu.Unlock()
			if err != nil && ctx.Err() == nil {
				slog.Warn("naming a place", "err", err)
			}
		}
	}
}

// answer is the part of Nominatim's reverse answer that names the town.
type answer struct {
	Error   string `json:"error"`
	Address struct {
		City         string `json:"city"`
		Town         string `json:"town"`
		Village      string `json:"village"`
		Municipality string `json:"municipality"`
		County       string `json:"county"`
		State        string `json:"state"`
		Country      string `json:"country"`
	} `json:"address"`
}

func (g *Geocoder) ask(ctx context.Context, k string) (string, error) {
	lat, lon, _ := strings.Cut(k, ",")
	q := url.Values{
		"format": {"jsonv2"}, "lat": {lat}, "lon": {lon},
		// Zoom 10 is the level of towns; German names where there are some.
		"zoom": {"10"}, "accept-language": {"de"}, "addressdetails": {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.URL+"/reverse?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", g.cfg.UserAgent)
	resp, err := g.cfg.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nominatim: status %d", resp.StatusCode)
	}
	var a answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a); err != nil {
		return "", fmt.Errorf("nominatim: %w", err)
	}
	if a.Error != "" {
		// "Unable to geocode": at sea or nowhere; asked for once only.
		return "", nil
	}
	ad := a.Address
	for _, name := range []string{ad.City, ad.Town, ad.Village, ad.Municipality, ad.County, ad.State, ad.Country} {
		if name != "" {
			return name, nil
		}
	}
	return "", nil
}

func (g *Geocoder) load() {
	if g.cfg.File == "" {
		return
	}
	data, err := os.ReadFile(g.cfg.File)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(data, &g.names)
	}
	if err != nil {
		slog.Warn("reading the place names; they are asked for again", "file", g.cfg.File, "err", err)
		g.names = map[string]string{}
	}
}

// save writes the answers through a temporary file, when there are new ones.
func (g *Geocoder) save() {
	g.mu.Lock()
	if !g.dirty || g.cfg.File == "" {
		g.mu.Unlock()
		return
	}
	data, err := json.Marshal(g.names)
	g.dirty = false
	g.mu.Unlock()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(g.cfg.File), 0o750)
	}
	tmp := g.cfg.File + ".tmp"
	if err == nil {
		err = os.WriteFile(tmp, data, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, g.cfg.File)
	}
	if err != nil {
		slog.Warn("keeping the place names", "file", g.cfg.File, "err", err)
		g.mu.Lock()
		g.dirty = true
		g.mu.Unlock()
	}
}
