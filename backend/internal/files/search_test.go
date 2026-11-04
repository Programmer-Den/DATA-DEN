package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// minimalPDF is a one-page PDF showing text.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 712 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

// zipWith is a zip holding one file, like a .docx or .odt.
func zipWith(t *testing.T, name, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type searchResult struct {
	Items   []Hit `json:"items"`
	Total   int   `json:"total"`
	Indexed int   `json:"indexed"`
	Ready   bool  `json:"ready"`
}

func search(t *testing.T, h http.Handler, q string) searchResult {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q="+url.QueryEscape(q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%q: status %d, body %s", q, rec.Code, rec.Body)
	}
	var r searchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func paths(r searchResult) []string {
	var p []string
	for _, h := range r.Items {
		p = append(p, h.Path)
	}
	return p
}

func searchData(t *testing.T) (*Files, http.Handler) {
	t.Helper()
	data := t.TempDir()
	files := map[string][]byte{
		"Steuer/2026/bescheid.pdf":   minimalPDF("Einkommensteuerbescheid Finanzamt Hamburg"),
		"Steuer/2026/notiz.txt":      []byte("Rückfrage beim Finanzamt wegen der Rechnung"),
		"Briefe/kuendigung.docx":     zipWith(t, "word/document.xml", `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Kündigung</w:t></w:r></w:p><w:p><w:r><w:t>Fitnessstudio</w:t></w:r></w:p></w:body></w:document>`),
		"Briefe/einladung.odt":       zipWith(t, "content.xml", `<office:document-content xmlns:office="o" xmlns:text="t"><text:p>Einladung zum Grillfest</text:p></office:document-content>`),
		"Alt/latin1.txt":             {'S', 'c', 'h', 0xF6, 'n', 'e', 's', ' ', 'W', 'e', 't', 't', 'e', 'r'},
		"Fotos/rechnung-foto.jpg":    []byte("jpeg"),
		"Fotos/kaputt.pdf":           []byte("%PDF-1.4 broken"),
		".data-den-trash/x/item.txt": []byte("Finanzamt im Papierkorb"),
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
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return f, mux
}

func TestSearchBeforeIndex(t *testing.T) {
	_, h := searchData(t)
	if r := search(t, h, "finanzamt"); r.Ready || len(r.Items) != 0 {
		t.Errorf("before the index: %+v", r)
	}
}

func TestSearch(t *testing.T) {
	f, h := searchData(t)
	f.refreshIndex()

	tests := []struct {
		q    string
		want []string
	}{
		// In a PDF and a text file, not in the recycle bin.
		{"finanzamt", []string{"/Steuer/2026/bescheid.pdf", "/Steuer/2026/notiz.txt"}},
		// Word and OpenDocument, with umlauts and any case.
		{"KÜNDIGUNG", []string{"/Briefe/kuendigung.docx"}},
		{"grillfest", []string{"/Briefe/einladung.odt"}},
		// A text file that is not UTF-8.
		{"schönes", []string{"/Alt/latin1.txt"}},
		// All words must match, in the path or the text.
		{"steuer hamburg", []string{"/Steuer/2026/bescheid.pdf"}},
		// Folders are found by name.
		{"briefe", []string{"/Briefe", "/Briefe/einladung.odt", "/Briefe/kuendigung.docx"}},
		{"nichtda", nil},
	}
	for _, tt := range tests {
		got := paths(search(t, h, tt.q))
		slices.Sort(got)
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%q: %v, want %v", tt.q, got, tt.want)
		}
	}

	// A name match ranks above a text match.
	r := search(t, h, "rechnung")
	if len(r.Items) != 2 || r.Items[0].Path != "/Fotos/rechnung-foto.jpg" || r.Items[1].Snippet == "" {
		t.Errorf("rechnung: %+v", r.Items)
	}
	if !strings.Contains(r.Items[1].Snippet, "Rechnung") {
		t.Errorf("snippet %q", r.Items[1].Snippet)
	}
	if !r.Ready || r.Indexed == 0 {
		t.Errorf("ready %v, indexed %d", r.Ready, r.Indexed)
	}
}

func TestSearchSeesChanges(t *testing.T) {
	f, h := searchData(t)
	f.refreshIndex()
	if err := os.WriteFile(filepath.Join(f.root.Name(), "neu.txt"), []byte("Zahnarzt Termin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root.Name(), "Steuer", "2026", "notiz.txt")); err != nil {
		t.Fatal(err)
	}
	f.refreshIndex()
	if got := paths(search(t, h, "zahnarzt")); len(got) != 1 {
		t.Errorf("new file: %v", got)
	}
	if got := paths(search(t, h, "rückfrage")); len(got) != 0 {
		t.Errorf("removed file still found: %v", got)
	}
}

func TestSearchNeedsWords(t *testing.T) {
	_, h := searchData(t)
	for _, q := range []string{"", "   ", strings.Repeat("a ", 11)} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q="+url.QueryEscape(q), nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, rec.Code)
		}
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("x", 100) + " Größe " + strings.Repeat("y", 100)
	lower := strings.ToLower(text)
	s := snippet(text, lower, strings.Index(lower, "größe"), len("größe"))
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "Größe") {
		t.Errorf("snippet %q", s)
	}
	if short := snippet("Größe", "größe", 0, len("größe")); short != "Größe" {
		t.Errorf("short snippet %q", short)
	}
}
