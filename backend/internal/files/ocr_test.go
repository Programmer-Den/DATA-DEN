package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOCR answers with the text after "text:" in a file, and counts what it
// was asked.
type fakeOCR struct {
	mu    sync.Mutex
	asked []string
	down  bool
}

func (o *fakeOCR) Text(_ context.Context, name string, r io.Reader, _ int64) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.down {
		return "", ErrRecognizerDown
	}
	o.asked = append(o.asked, name)
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	_, text, ok := strings.Cut(string(b), "text:")
	if !ok {
		return "", errors.New("unreadable")
	}
	return text, nil
}

func (o *fakeOCR) names() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	names := slices.Clone(o.asked)
	slices.Sort(names)
	return names
}

func ocrData(t *testing.T, ocr Recognizer) (string, *Files, http.Handler) {
	t.Helper()
	data := t.TempDir()
	files := map[string][]byte{
		"Scans/rechnung.png":    []byte("png text:Rechnung vom Gärtner ~ | 129,95 Euro"),
		"Scans/vertrag.pdf":     append(minimalPDF(""), []byte("text:Mietvertrag Wohnung")...),
		"Steuer/bescheid.pdf":   minimalPDF("Einkommensteuerbescheid Finanzamt Hamburg " + strings.Repeat("Seite ", 20)),
		"Listen/budget.xlsx":    []byte("xlsx text:Haushaltsbuch"),
		"Fotos/kaputt.jpg":      []byte("no text here"),
		"Notizen/einkauf.txt":   []byte("Milch und Brot"),
		"Fotos/leer.png":        {},
		".data-den-trash/a.png": []byte("png text:im Papierkorb"),
	}
	for name, content := range files {
		full := filepath.Join(data, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, h := openOCR(t, data, ocr)
	return data, f, h
}

func openOCR(t *testing.T, data string, ocr Recognizer) (*Files, http.Handler) {
	t.Helper()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	f.ocr = ocr
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return f, mux
}

func recognizing(t *testing.T, h http.Handler) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=x", nil))
	var r struct {
		Recognizing int `json:"recognizing"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r.Recognizing
}

func TestRecognize(t *testing.T) {
	ocr := &fakeOCR{}
	data, f, h := ocrData(t, ocr)
	f.refreshIndex()

	// Before the recognition only the names are found.
	if got := paths(search(t, h, "gärtner")); len(got) != 0 {
		t.Errorf("before: %v", got)
	}
	if n := recognizing(t, h); n != 4 {
		t.Errorf("waiting: %d, want 4", n)
	}
	done, err := f.recognizeWaiting(t.Context())
	if err != nil || done != 4 {
		t.Fatalf("recognized %d, %v", done, err)
	}
	// Images, PDFs without text and office formats the search cannot read
	// itself; not a PDF with text, an empty file or the recycle bin.
	want := []string{"Fotos/kaputt.jpg", "Listen/budget.xlsx", "Scans/rechnung.png", "Scans/vertrag.pdf"}
	if got := ocr.names(); !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
	if n := recognizing(t, h); n != 0 {
		t.Errorf("still waiting: %d", n)
	}
	for q, want := range map[string]string{
		"GÄRTNER":       "/Scans/rechnung.png",
		"mietvertrag":   "/Scans/vertrag.pdf",
		"haushaltsbuch": "/Listen/budget.xlsx",
		"finanzamt":     "/Steuer/bescheid.pdf",
	} {
		r := search(t, h, q)
		if got := paths(r); len(got) != 1 || got[0] != want || r.Items[0].Snippet == "" {
			t.Errorf("%q: %+v, want %s", q, r.Items, want)
		}
	}
	// The noise of a photo is dropped.
	if r := search(t, h, "gärtner"); strings.Contains(r.Items[0].Snippet, "|") {
		t.Errorf("noise kept: %q", r.Items[0].Snippet)
	}

	// After a restart the kept texts are used: nothing is read again.
	ocr2 := &fakeOCR{}
	f2, h2 := openOCR(t, data, ocr2)
	f2.refreshIndex()
	if n := recognizing(t, h2); n != 0 || len(ocr2.names()) != 0 {
		t.Errorf("after a restart: %d waiting, asked %v", n, ocr2.names())
	}
	if got := paths(search(t, h2, "gärtner")); len(got) != 1 {
		t.Errorf("after a restart: %v", got)
	}

	// A changed file is read again and its old text removed.
	scan := filepath.Join(data, "Scans", "rechnung.png")
	if err := os.WriteFile(scan, []byte("png text:Quittung Bäckerei"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(scan, later, later); err != nil {
		t.Fatal(err)
	}
	f2.refreshIndex()
	if _, err := f2.recognizeWaiting(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := paths(search(t, h2, "bäckerei")); len(got) != 1 {
		t.Errorf("changed: %v", got)
	}
	if got := paths(search(t, h2, "gärtner")); len(got) != 0 {
		t.Errorf("old text: %v", got)
	}
	kept := 0
	_ = filepath.WalkDir(filepath.Join(data, ocrDir), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			kept++
		}
		return nil
	})
	if kept != 4 {
		t.Errorf("%d texts kept, want 4", kept)
	}
}

func TestRecognizeDown(t *testing.T) {
	ocr := &fakeOCR{down: true}
	_, f, h := ocrData(t, ocr)
	f.refreshIndex()
	if _, err := f.recognizeWaiting(t.Context()); !errors.Is(err, ErrRecognizerDown) {
		t.Fatalf("err %v", err)
	}
	if n := recognizing(t, h); n != 4 {
		t.Errorf("waiting: %d, want 4", n)
	}
	// They wait over the next refresh and are read when it is back.
	f.refreshIndex()
	ocr.mu.Lock()
	ocr.down = false
	ocr.mu.Unlock()
	if done, err := f.recognizeWaiting(t.Context()); err != nil || done != 4 {
		t.Errorf("recognized %d, %v", done, err)
	}
	if got := paths(search(t, h, "mietvertrag")); len(got) != 1 {
		t.Errorf("after: %v", got)
	}
}

func TestRecognizeRuns(t *testing.T) {
	ocr := &fakeOCR{}
	_, f, h := ocrData(t, ocr)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go f.Recognize(ctx)
	f.refreshIndex()
	deadline := time.Now().Add(5 * time.Second)
	for recognizing(t, h) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("not recognized in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := paths(search(t, h, "haushaltsbuch")); len(got) != 1 {
		t.Errorf("found %v", got)
	}
}

func TestWithoutRecognizer(t *testing.T) {
	_, f, h := ocrData(t, nil)
	f.refreshIndex()
	if n := recognizing(t, h); n != 0 {
		t.Errorf("waiting: %d", n)
	}
	if _, err := os.Stat(filepath.Join(f.root.Name(), ocrDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s made: %v", ocrDir, err)
	}
}

func TestTika(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/tika" || !strings.HasPrefix(r.Header.Get("Accept"), "text/plain") ||
			!strings.Contains(r.Header.Get("Content-Disposition"), `filename=scan.png`) {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("Text of " + string(body)))
	}))
	u, _ := url.Parse(srv.URL)
	tika := NewTika(u)
	text := func() (string, error) {
		return tika.Text(t.Context(), "Scans/scan.png", strings.NewReader("png"), 3)
	}

	status = http.StatusOK
	if got, err := text(); err != nil || got != "Text of png" {
		t.Errorf("ok: %q, %v", got, err)
	}
	status = http.StatusUnprocessableEntity
	if _, err := text(); err == nil || errors.Is(err, ErrRecognizerDown) {
		t.Errorf("422: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := text(); !errors.Is(err, ErrRecognizerDown) {
		t.Errorf("503: %v", err)
	}
	srv.Close()
	if _, err := text(); !errors.Is(err, ErrRecognizerDown) {
		t.Errorf("closed: %v", err)
	}
}

func TestDropNoise(t *testing.T) {
	if got := dropNoise("~ | Rechnung — 4711 a Nr. € 12"); got != "Rechnung 4711 Nr. 12" {
		t.Errorf("got %q", got)
	}
}
