package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListKinds(t *testing.T) {
	h := setup(t, true)
	l := listing(t, get(h, "/api/files", "/", nil))
	notes := l.Entries[1]
	if notes.Kind != "document" || !notes.Preview {
		t.Errorf("notizen.txt: %+v", notes)
	}
	if photos := l.Entries[0]; photos.Kind != "" || photos.Preview {
		t.Errorf("folder Fotos: %+v", photos)
	}
}

func TestView(t *testing.T) {
	h := setup(t, true)
	tests := []struct{ path, contentType, body string }{
		{"/Fotos/urlaub.jpg", "image/jpeg", "jpeg data"},
		{"/notizen.txt", "text/plain; charset=utf-8", "0123456789"},
	}
	for _, tt := range tests {
		rec := get(h, "/api/view", tt.path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != tt.body {
			t.Fatalf("%s: status %d, body %q", tt.path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Content-Type"); got != tt.contentType {
			t.Errorf("%s: Content-Type %q, want %q", tt.path, got, tt.contentType)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
			t.Errorf("%s: Content-Disposition %q", tt.path, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(got, "sandbox;") {
			t.Errorf("%s: Content-Security-Policy %q, want a sandbox", tt.path, got)
		}
	}
}

func TestViewRange(t *testing.T) {
	h := setup(t, true)
	// Seeking in a video asks for a range.
	rec := get(h, "/api/view", "/notizen.txt", http.Header{"Range": {"bytes=2-4"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body)
	}
}

func TestViewPDFInFrame(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "brief.pdf"), []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := handler(t, data)
	rec := get(h, "/api/view", "/brief.pdf", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options %q, want SAMEORIGIN", got)
	}
}

func TestViewErrors(t *testing.T) {
	data := t.TempDir()
	for name, content := range map[string]string{"seite.html": "<script>alert(1)</script>", "brief.docx": "PK"} {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := handler(t, data)
	tests := []struct {
		path string
		want int
	}{
		// Never served for the browser to run.
		{"/seite.html", http.StatusUnsupportedMediaType},
		// Downloaded, not shown.
		{"/brief.docx", http.StatusUnsupportedMediaType},
		{"/nothing.jpg", http.StatusNotFound},
		{"/../secret.jpg", http.StatusBadRequest},
		{"/" + uploadDir + "/x.jpg", http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := get(h, "/api/view", tt.path, nil); rec.Code != tt.want {
			t.Errorf("%q: status %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
}

func TestViewSymlinkOutside(t *testing.T) {
	h := setup(t, true)
	// setup's symlink is a .txt pointing out of the data directory.
	rec := get(h, "/api/view", "/secret.txt", nil)
	if rec.Code != http.StatusBadRequest || rec.Body.String() == "outside" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body)
	}
}

func TestMedia(t *testing.T) {
	data := t.TempDir()
	old := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	files := map[string]time.Time{
		"Fotos/2025/strand.JPG": old,
		"Fotos/2026/berg.png":   old.AddDate(1, 0, 0),
		"Fotos/film.mp4":        old,
		"Dokumente/brief.pdf":   old,
		uploadDir + "/x.jpg":    old,
	}
	for name, modified := range files {
		full := filepath.Join(data, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(data, "Fotos", "2026", "berg.png"), filepath.Join(data, "link.png")); err != nil {
		t.Fatal(err)
	}
	h := handler(t, data)

	rec := media(h, "photo")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var m Media
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	// Newest first; the symlink and unfinished uploads are left out.
	if len(m.Items) != 2 || m.Items[0].Path != "/Fotos/2026/berg.png" || m.Items[1].Path != "/Fotos/2025/strand.JPG" {
		t.Fatalf("items %+v", m.Items)
	}
	if it := m.Items[1]; it.Name != "strand.JPG" || it.Kind != "photo" || !it.Preview || it.Size != 1 {
		t.Errorf("item %+v", it)
	}

	rec = media(h, "document")
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 || m.Items[0].Path != "/Dokumente/brief.pdf" {
		t.Errorf("documents %+v", m.Items)
	}

	if rec := media(h, "secret"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d, want 400", rec.Code)
	}
}

func TestViewsNeedLogin(t *testing.T) {
	h := setup(t, false)
	for _, target := range []string{"/api/view?path=/notizen.txt", "/api/media?kind=photo"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", target, rec.Code)
		}
	}
}

// handler serves the data directory data, logged in.
func handler(t *testing.T, data string) http.Handler {
	t.Helper()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return mux
}

func media(h http.Handler, kind string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/media?kind="+kind, nil))
	return rec
}
