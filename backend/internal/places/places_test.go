package places

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// nominatim answers like Nominatim and notes the requests.
type nominatim struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
}

func (n *nominatim) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	n.requests = append(n.requests, r)
	status := n.status
	n.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch r.URL.Query().Get("lat") {
	case "52.52":
		_, _ = w.Write([]byte(`{"address": {"city": "Berlin", "state": "Berlin", "country": "Deutschland"}}`))
	case "47.92":
		_, _ = w.Write([]byte(`{"address": {"village": "Kirchzarten", "county": "Breisgau-Hochschwarzwald"}}`))
	default:
		_, _ = w.Write([]byte(`{"error": "Unable to geocode"}`))
	}
}

func (n *nominatim) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.requests)
}

func start(t *testing.T, cfg Config) (*Geocoder, *nominatim) {
	t.Helper()
	n := &nominatim{}
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	cfg.URL, cfg.UserAgent, cfg.Interval = srv.URL, "DATA-DEN test", time.Millisecond
	g := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return g, n
}

// eventually waits until name answers want.
func eventually(t *testing.T, g *Geocoder, lat, lon float64, want string) {
	t.Helper()
	for range 200 {
		if g.Name(lat, lon) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%v, %v: %q, want %q", lat, lon, g.Name(lat, lon), want)
}

func TestName(t *testing.T) {
	g, n := start(t, Config{})
	if got := g.Name(52.5163, 13.3777); got != "" {
		t.Errorf("before the answer: %q", got)
	}
	eventually(t, g, 52.5163, 13.3777, "Berlin")
	eventually(t, g, 47.9213, 7.9881, "Kirchzarten")

	// Nearby positions share the answer; the request was rounded.
	if got := g.Name(52.521, 13.381); got != "Berlin" {
		t.Errorf("nearby: %q", got)
	}
	r := n.requests[0]
	if q := r.URL.Query(); q.Get("lat") != "52.52" || q.Get("lon") != "13.38" || q.Get("accept-language") != "de" || r.Header.Get("User-Agent") != "DATA-DEN test" {
		t.Errorf("request %s, User-Agent %q", r.URL, r.Header.Get("User-Agent"))
	}

	// At sea: no name, and not asked for again.
	g.Name(0, -140)
	time.Sleep(50 * time.Millisecond)
	before := n.count()
	g.Name(0, -140)
	time.Sleep(50 * time.Millisecond)
	if g.Name(0, -140) != "" || n.count() != before {
		t.Errorf("at sea asked again: %d requests, were %d", n.count(), before)
	}
}

func TestNameRetriesLater(t *testing.T) {
	g, n := start(t, Config{})
	n.mu.Lock()
	n.status = http.StatusTooManyRequests
	n.mu.Unlock()
	g.Name(52.52, 13.40)
	time.Sleep(50 * time.Millisecond)
	g.Name(52.52, 13.40)
	time.Sleep(50 * time.Millisecond)
	if n.count() != 1 {
		t.Errorf("%d requests after a failure, want 1", n.count())
	}
}

func TestFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "places", "names.json")
	g, _ := start(t, Config{File: file})
	eventually(t, g, 52.52, 13.40, "Berlin")
	g.save()

	// A new Geocoder knows it without asking.
	again, n := start(t, Config{File: file})
	if got := again.Name(52.52, 13.40); got != "Berlin" || n.count() != 0 {
		t.Errorf("from the file: %q, %d requests", got, n.count())
	}

	// A broken file is started over.
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, _ := start(t, Config{File: file})
	if got := broken.Name(52.52, 13.40); got != "" {
		t.Errorf("broken file: %q", got)
	}
}
