package tiles

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tilePNG = png + "tile"

// start serves Tiles from a test tile server that answers with status, or
// a PNG when status is 0; it returns the mux and the request count.
func start(t *testing.T, status *atomic.Int32, cfg Config) (*http.ServeMux, *atomic.Int32, *Config) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("User-Agent") != "DATA-DEN test" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		if r.URL.Path != "/3/4/5.png" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte(tilePNG))
	}))
	t.Cleanup(srv.Close)
	cfg.URL, cfg.UserAgent = srv.URL, "DATA-DEN test"
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	tiles, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	tiles.Register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	return mux, &count, &cfg
}

func get(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesAndKeepsTiles(t *testing.T) {
	var status atomic.Int32
	mux, count, cfg := start(t, &status, Config{})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			rec := get(mux, "/api/tiles/3/4/5")
			if rec.Code != http.StatusOK || rec.Body.String() != tilePNG {
				t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "image/png" {
				t.Errorf("Content-Type %q", got)
			}
		})
	}
	wg.Wait()
	if got := count.Load(); got != 1 {
		t.Errorf("fetched %d times, want once", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "3", "4", "5.png")); err != nil {
		t.Errorf("not kept: %v", err)
	}
	// From the cache, even when the tile server fails.
	status.Store(http.StatusInternalServerError)
	if rec := get(mux, "/api/tiles/3/4/5"); rec.Code != http.StatusOK || count.Load() != 1 {
		t.Errorf("status %d after %d requests", rec.Code, count.Load())
	}
}

func TestOldTiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "3", "4", "5.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(png+"old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusTooManyRequests)
	mux, count, _ := start(t, &status, Config{Dir: dir})
	// The old tile is better than none while the server refuses.
	if rec := get(mux, "/api/tiles/3/4/5"); rec.Code != http.StatusOK || rec.Body.String() != png+"old" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
	status.Store(0)
	if rec := get(mux, "/api/tiles/3/4/5"); rec.Body.String() != tilePNG {
		t.Errorf("not renewed: %q", rec.Body.String())
	}
	if got := count.Load(); got != 2 {
		t.Errorf("fetched %d times", got)
	}
}

func TestFailures(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	mux, count, _ := start(t, &status, Config{})
	if rec := get(mux, "/api/tiles/3/4/5"); rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
	for _, path := range []string{
		"/api/tiles/20/0/0", "/api/tiles/-1/0/0", "/api/tiles/3/8/0", "/api/tiles/3/0/8",
		"/api/tiles/3/-1/0", "/api/tiles/a/0/0",
	} {
		if rec := get(mux, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, rec.Code)
		}
	}
	if got := count.Load(); got != 1 {
		t.Errorf("fetched %d times", got)
	}
}

func TestNotAPNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>"))
	}))
	t.Cleanup(srv.Close)
	tiles, err := New(Config{URL: srv.URL, Dir: filepath.Join(t.TempDir(), "new")})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	tiles.Register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	if rec := get(mux, "/api/tiles/0/0/0"); rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
}
