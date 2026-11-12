// Package tiles serves the map of the photos: the raster tiles of
// OpenStreetMap, fetched by the server and kept in the data directory.
//
// The browser only ever talks to DATA-DEN, so the website keeps its strict
// Content-Security-Policy and the tile server never sees the users' addresses.
// Each tile is fetched once and kept for MaxAge, with a name for the program
// and at most two requests at a time, as OpenStreetMap's tile usage policy
// asks.
package tiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"sync"
	"time"
)

// DefaultURL is OpenStreetMap's own tile server.
const DefaultURL = "https://tile.openstreetmap.org"

const (
	// MaxZoom is the closest zoom OpenStreetMap draws.
	MaxZoom = 19
	// defaultMaxAge is how long a tile is kept before it is fetched again;
	// OpenStreetMap asks for at least a week.
	defaultMaxAge = 30 * 24 * time.Hour
	// maxTile is the largest tile accepted; OpenStreetMap's are a few KB.
	maxTile = 1 << 20
	// fetchers is how many tiles are fetched at the same time.
	fetchers = 2
)

// Config is where the tiles come from and where they are kept.
type Config struct {
	// URL is the tile server, such as DefaultURL; tiles are URL/z/x/y.png.
	URL string
	// UserAgent names the program, as the tile usage policy asks.
	UserAgent string
	// Dir keeps the tiles, as z/x/y.png; it is made when missing.
	Dir string
	// Client sends the requests; nil uses one with a timeout.
	Client *http.Client
	// MaxAge is how long a tile is kept, 0 for 30 days.
	MaxAge time.Duration
}

// Tiles serves the map tiles from Dir, fetching those it does not have.
type Tiles struct {
	cfg   Config
	dir   *os.Root
	slots chan struct{}
	mu    sync.Mutex
	// busy are the tiles being fetched; a second request waits for the first.
	busy map[string]chan struct{}
}

// New returns Tiles for cfg.
func New(cfg Config) (*Tiles, error) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = defaultMaxAge
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(cfg.Dir)
	if err != nil {
		return nil, err
	}
	return &Tiles{cfg: cfg, dir: dir, slots: make(chan struct{}, fetchers), busy: map[string]chan struct{}{}}, nil
}

// Register adds GET /api/tiles/{z}/{x}/{y} to mux, behind require.
func (t *Tiles) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/tiles/{z}/{x}/{y}", require(t.serve))
}

// tile reads z, x and y of a request; ok is false when they are no tile.
func tile(r *http.Request) (z, x, y int, ok bool) {
	z, errZ := strconv.Atoi(r.PathValue("z"))
	x, errX := strconv.Atoi(r.PathValue("x"))
	y, errY := strconv.Atoi(r.PathValue("y"))
	if errZ != nil || errX != nil || errY != nil || z < 0 || z > MaxZoom {
		return 0, 0, 0, false
	}
	n := 1 << z
	return z, x, y, x >= 0 && x < n && y >= 0 && y < n
}

func (t *Tiles) serve(w http.ResponseWriter, r *http.Request) {
	z, x, y, ok := tile(r)
	if !ok {
		http.Error(w, "Keine Kachel", http.StatusNotFound)
		return
	}
	rel := fmt.Sprintf("%d/%d/%d", z, x, y)
	file := rel + ".png"
	if !t.fresh(file) {
		if err := t.fetch(r.Context(), rel, file); err != nil {
			if _, statErr := t.dir.Stat(file); statErr != nil {
				// No old tile to show instead.
				if r.Context().Err() == nil {
					slog.Warn("fetching a map tile", "tile", rel, "err", err)
				}
				http.Error(w, "Kachel nicht verfügbar", http.StatusBadGateway)
				return
			}
		}
	}
	data, err := t.dir.ReadFile(file)
	if err != nil {
		http.Error(w, "Kachel nicht verfügbar", http.StatusBadGateway)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data) //nolint:gosec // a PNG, checked when fetched, served as image/png
}

// fresh is true when file, in Dir, is there and younger than MaxAge.
func (t *Tiles) fresh(file string) bool {
	info, err := t.dir.Stat(file)
	return err == nil && time.Since(info.ModTime()) < t.cfg.MaxAge
}

// fetch gets the tile rel into file, once even when several ask at once.
func (t *Tiles) fetch(ctx context.Context, rel, file string) error {
	t.mu.Lock()
	if done, ok := t.busy[rel]; ok {
		t.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if t.fresh(file) {
			return nil
		}
		return errors.New("the first request for it failed")
	}
	done := make(chan struct{})
	t.busy[rel] = done
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.busy, rel)
		t.mu.Unlock()
		close(done)
	}()

	select {
	case t.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-t.slots }()
	// A request the browser gave up on still fills the cache.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	data, err := t.download(ctx, rel)
	if err != nil {
		return err
	}
	if err := t.dir.MkdirAll(path.Dir(file), 0o750); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := t.dir.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return t.dir.Rename(tmp, file)
}

// png is how every PNG file starts.
const png = "\x89PNG\r\n\x1a\n"

func (t *Tiles) download(ctx context.Context, rel string) ([]byte, error) {
	//nolint:gosec // the tile server is the admin's setting; rel is z/x/y, three numbers
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.cfg.URL+"/"+rel+".png", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", t.cfg.UserAgent)
	resp, err := t.cfg.Client.Do(req) //nolint:gosec // as above
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tile server: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTile || len(data) < len(png) || string(data[:len(png)]) != png {
		return nil, errors.New("tile server: not a PNG tile")
	}
	return data, nil
}
