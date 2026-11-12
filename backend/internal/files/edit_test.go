package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readText(t *testing.T, h http.Handler, p string) (content, version string) {
	t.Helper()
	rec := get(h, "/api/text", p, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", p, rec.Code, rec.Body)
	}
	var body struct{ Content, Version string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Content, body.Version
}

func putText(h http.Handler, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/text", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// setupEdit is setup with the data directory's path.
func setupEdit(t *testing.T) (http.Handler, string) {
	h, data, _ := setupEditFiles(t)
	return h, data
}

// setupEditFiles is setupEdit with the Files.
func setupEditFiles(t *testing.T) (http.Handler, string, *Files) {
	t.Helper()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "notizen.md"), []byte("# Alt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return mux, data, f
}

func TestEditText(t *testing.T) {
	h, data := setupEdit(t)
	content, version := readText(t, h, "/notizen.md")
	if content != "# Alt\n" || version == "" {
		t.Fatalf("content %q, version %q", content, version)
	}
	rec := putText(h, map[string]any{"path": "/notizen.md", "content": "# Neu\n", "version": version})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Clean(filepath.Join(data, "notizen.md")))
	if err != nil || string(got) != "# Neu\n" {
		t.Fatalf("file %q, %v", got, err)
	}
	var saved struct{ Version string }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if _, now := readText(t, h, "/notizen.md"); now != saved.Version {
		t.Errorf("version after save %q, answer said %q", now, saved.Version)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, editDir)); len(entries) != 0 {
		t.Errorf("left in %s: %v", editDir, entries)
	}

	// The editor still holds the first version: refused, nothing lost.
	rec = putText(h, map[string]any{"path": "/notizen.md", "content": "# Anders\n", "version": version})
	if rec.Code != http.StatusConflict {
		t.Errorf("save from an old version: status %d", rec.Code)
	}
	if got, _ := os.ReadFile(filepath.Clean(filepath.Join(data, "notizen.md"))); string(got) != "# Neu\n" {
		t.Errorf("file after the refused save %q", got)
	}
}

func TestEditBackup(t *testing.T) {
	h, data, f := setupEditFiles(t)
	_, version := readText(t, h, "/notizen.md")
	rec := putText(h, map[string]any{"path": "/notizen.md", "content": "# Neu\n", "version": version, "backup": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}
	versions := f.versionsOf("notizen.md")
	if len(versions) != 1 {
		t.Fatalf("versions %+v", versions)
	}
	old, err := os.ReadFile(filepath.Clean(filepath.Join(data, versionFolder("notizen.md"), versions[0].ID)))
	if err != nil || string(old) != "# Alt\n" {
		t.Errorf("old version %q, %v", old, err)
	}
	if got, _ := os.ReadFile(filepath.Clean(filepath.Join(data, "notizen.md"))); string(got) != "# Neu\n" {
		t.Errorf("file %q", got)
	}
}

func TestEditBackupSkipsUnchanged(t *testing.T) {
	h, _, f := setupEditFiles(t)
	_, version := readText(t, h, "/notizen.md")
	if rec := putText(h, map[string]any{"path": "/notizen.md", "content": "# Neu\n", "version": version, "backup": true}); rec.Code != http.StatusOK {
		t.Fatalf("first save: status %d, body %s", rec.Code, rec.Body)
	}
	// Saved again without a change: the first save keeps its one version.
	_, version = readText(t, h, "/notizen.md")
	if rec := putText(h, map[string]any{"path": "/notizen.md", "content": "# Neu\n", "version": version, "backup": true}); rec.Code != http.StatusOK {
		t.Fatalf("second save: status %d, body %s", rec.Code, rec.Body)
	}
	if versions := f.versionsOf("notizen.md"); len(versions) != 1 {
		t.Errorf("versions %+v, want the one from the first save", versions)
	}
}

func TestEditCreate(t *testing.T) {
	h, data := setupEdit(t)
	if rec := putText(h, map[string]any{"path": "/neu.txt", "content": "Hallo"}); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(filepath.Clean(filepath.Join(data, "neu.txt"))); string(got) != "Hallo" {
		t.Errorf("new file %q", got)
	}
	// Never over an existing file.
	if rec := putText(h, map[string]any{"path": "/notizen.md", "content": "x"}); rec.Code != http.StatusConflict {
		t.Errorf("create over a file: status %d", rec.Code)
	}
	if rec := putText(h, map[string]any{"path": "/Fehlt/neu.txt", "content": "x"}); rec.Code != http.StatusNotFound {
		t.Errorf("create in a missing folder: status %d", rec.Code)
	}
}

func TestEditRefused(t *testing.T) {
	h, data := setupEdit(t)
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "latin1.txt"), []byte{'G', 'r', 0xfc, 0xdf, 'e'})
	write(filepath.Join(data, "bild.jpg"), []byte("jpeg"))
	write(filepath.Join(data, "gross.txt"), []byte(strings.Repeat("x", MaxText+1)))

	reads := map[string]int{
		"/latin1.txt":                     http.StatusUnsupportedMediaType,
		"/bild.jpg":                       http.StatusUnsupportedMediaType,
		"/gross.txt":                      http.StatusRequestEntityTooLarge,
		"/fehlt.txt":                      http.StatusNotFound,
		"/../notizen.md":                  http.StatusBadRequest,
		"/.data-den-trash/x/item.txt":     http.StatusNotFound,
		"/.data-den-users/anna/geheim.md": http.StatusNotFound,
	}
	for p, want := range reads {
		if rec := get(h, "/api/text", p, nil); rec.Code != want {
			t.Errorf("GET %s: status %d, want %d", p, rec.Code, want)
		}
	}

	_, version := readText(t, h, "/notizen.md")
	saves := []struct {
		body any
		want int
	}{
		{map[string]any{"path": "/bild.jpg", "content": "x", "version": version}, http.StatusBadRequest},
		{map[string]any{"path": "/.data-den-trash/a.txt", "content": "x"}, http.StatusBadRequest},
		{map[string]any{"path": "/../a.txt", "content": "x"}, http.StatusBadRequest},
		{map[string]any{"path": "/notizen.md", "content": strings.Repeat("x", MaxText+1), "version": version}, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range saves {
		if rec := putText(h, tt.body); rec.Code != tt.want {
			t.Errorf("PUT %v: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}

	// Only JSON: a form on another site cannot send it.
	req := httptest.NewRequest(http.MethodPut, "/api/text", strings.NewReader(`{"path":"/a.txt","content":"x"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain body: status %d", rec.Code)
	}
}

func TestEditQuota(t *testing.T) {
	data := t.TempDir()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	f.quota.Store(10)
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	if rec := putText(mux, map[string]any{"path": "/a.txt", "content": "12345678901"}); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("over the quota: status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(data, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("file written over the quota: %v", err)
	}
	if rec := putText(mux, map[string]any{"path": "/a.txt", "content": "1234567890"}); rec.Code != http.StatusOK {
		t.Errorf("within the quota: status %d", rec.Code)
	}
}

func TestListTextFlag(t *testing.T) {
	h := setup(t, true)
	l := listing(t, get(h, "/api/files", "/", nil))
	for _, e := range l.Entries {
		if want := e.Name == "notizen.txt"; e.Text != want {
			t.Errorf("%s: text %v, want %v", e.Name, e.Text, want)
		}
	}
}
