package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// setup makes a data directory with Fotos/urlaub.jpg, notizen.txt, a symlink
// to a secret outside it, and returns the handler. loggedIn decides what the
// login check says.
func setup(t *testing.T, loggedIn bool) http.Handler {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "data")
	for _, dir := range []string{data, filepath.Join(data, "Fotos")} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, content string) {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "Fotos", "urlaub.jpg"), "jpeg data")
	write(filepath.Join(data, "notizen.txt"), "0123456789")
	write(filepath.Join(base, "secret.txt"), "outside")
	if err := os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(data, "secret.txt")); err != nil {
		t.Fatal(err)
	}

	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	require := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !loggedIn {
				http.Error(w, "not logged in", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	mux := http.NewServeMux()
	f.Register(mux, require)
	return mux
}

func get(h http.Handler, endpoint, p string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, endpoint+"?path="+url.QueryEscape(p), nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func listing(t *testing.T, rec *httptest.ResponseRecorder) Listing {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var l Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestListRoot(t *testing.T) {
	h := setup(t, true)
	for _, p := range []string{"", "/", "."} {
		l := listing(t, get(h, "/api/files", p, nil))
		if l.Path != "/" {
			t.Errorf("%q: path %q, want /", p, l.Path)
		}
		// Folders first; the symlink is left out.
		if len(l.Entries) != 2 || l.Entries[0].Name != "Fotos" || !l.Entries[0].Dir ||
			l.Entries[1].Name != "notizen.txt" || l.Entries[1].Size != 10 {
			t.Errorf("%q: entries %+v", p, l.Entries)
		}
	}
}

func TestListFolder(t *testing.T) {
	h := setup(t, true)
	l := listing(t, get(h, "/api/files", "/Fotos", nil))
	if l.Path != "/Fotos" || len(l.Entries) != 1 || l.Entries[0].Name != "urlaub.jpg" {
		t.Errorf("listing %+v", l)
	}
}

func TestListErrors(t *testing.T) {
	h := setup(t, true)
	tests := []struct {
		path string
		want int
	}{
		{"/nothing", http.StatusNotFound},
		{"/notizen.txt", http.StatusBadRequest},
		{"/../", http.StatusBadRequest},
		{"/Fotos/../..", http.StatusBadRequest},
		{"..\\data", http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := get(h, "/api/files", tt.path, nil); rec.Code != tt.want {
			t.Errorf("%q: status %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
}

func TestDownload(t *testing.T) {
	h := setup(t, true)
	rec := get(h, "/api/download", "/Fotos/urlaub.jpg", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg data" {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename=urlaub.jpg" {
		t.Errorf("Content-Disposition %q", got)
	}
}

func TestDownloadRange(t *testing.T) {
	h := setup(t, true)
	rec := get(h, "/api/download", "/notizen.txt", http.Header{"Range": {"bytes=4-"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "456789" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body)
	}
}

func TestDownloadErrors(t *testing.T) {
	h := setup(t, true)
	tests := []struct {
		path string
		want int
	}{
		{"/Fotos", http.StatusBadRequest},
		{"/nothing.txt", http.StatusNotFound},
		// A symlink pointing out of the data directory.
		{"/secret.txt", http.StatusBadRequest},
		{"/../secret.txt", http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := get(h, "/api/download", tt.path, nil)
		if rec.Code != tt.want {
			t.Errorf("%q: status %d, want %d", tt.path, rec.Code, tt.want)
		}
		if rec.Body.String() == "outside" {
			t.Errorf("%q: served the file outside the data directory", tt.path)
		}
	}
}

func TestNeedsLogin(t *testing.T) {
	h := setup(t, false)
	for _, endpoint := range []string{"/api/files", "/api/download"} {
		if rec := get(h, endpoint, "/notizen.txt", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", endpoint, rec.Code)
		}
	}
}
