package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// uploadSetup is like setup and also returns the data directory.
func uploadSetup(t *testing.T) (http.Handler, string) {
	t.Helper()
	data := t.TempDir()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return mux, data
}

func do(h http.Handler, method, target, contentType, body string, header http.Header) (int, map[string]any) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var answer map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	return rec.Code, answer
}

func start(t *testing.T, h http.Handler, path string, size int) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"path": path, "size": size})
	code, answer := do(h, http.MethodPost, "/api/uploads", "application/json", string(body), nil)
	id, _ := answer["id"].(string)
	return code, id
}

func chunk(h http.Handler, id string, offset int, data string) (int, map[string]any) {
	return do(h, http.MethodPut, "/api/uploads/"+id, "application/octet-stream", data,
		http.Header{"Upload-Offset": {strconv.Itoa(offset)}})
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUploadInChunks(t *testing.T) {
	h, data := uploadSetup(t)
	code, id := start(t, h, "/Fotos/2026/urlaub.jpg", 10)
	if code != http.StatusCreated || id == "" {
		t.Fatalf("start: status %d", code)
	}
	if code, answer := chunk(h, id, 0, "01234"); code != http.StatusOK || answer["offset"] != 5.0 {
		t.Fatalf("first chunk: status %d, %v", code, answer)
	}
	if _, err := os.Stat(filepath.Join(data, "Fotos")); err == nil {
		t.Error("the folder exists before the upload is complete")
	}
	if code, answer := do(h, http.MethodGet, "/api/uploads/"+id, "", "", nil); code != http.StatusOK || answer["offset"] != 5.0 {
		t.Errorf("state: status %d, %v", code, answer)
	}
	if code, answer := chunk(h, id, 5, "56789"); code != http.StatusOK || answer["offset"] != 10.0 {
		t.Fatalf("last chunk: status %d, %v", code, answer)
	}
	if got := read(t, filepath.Join(data, "Fotos", "2026", "urlaub.jpg")); got != "0123456789" {
		t.Errorf("file %q", got)
	}
	// Done: the upload is gone, and so are its chunks.
	if code, _ := chunk(h, id, 10, "x"); code != http.StatusNotFound {
		t.Errorf("chunk after the end: status %d, want 404", code)
	}
	if left, _ := os.ReadDir(filepath.Join(data, uploadDir)); len(left) != 0 {
		t.Errorf("chunks left behind: %v", left)
	}
}

func TestUploadWrongOffset(t *testing.T) {
	h, _ := uploadSetup(t)
	_, id := start(t, h, "/a.txt", 4)
	chunk(h, id, 0, "ab")
	code, answer := chunk(h, id, 0, "ab")
	if code != http.StatusConflict || answer["offset"] != 2.0 {
		t.Errorf("repeated chunk: status %d, %v", code, answer)
	}
}

func TestUploadIgnoresExtraData(t *testing.T) {
	h, data := uploadSetup(t)
	_, id := start(t, h, "/a.txt", 3)
	if code, answer := chunk(h, id, 0, "abcdef"); code != http.StatusOK || answer["offset"] != 3.0 {
		t.Fatalf("status %d, %v", code, answer)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "abc" {
		t.Errorf("file %q, want abc", got)
	}
}

func TestUploadEmptyFile(t *testing.T) {
	h, data := uploadSetup(t)
	if code, _ := start(t, h, "/leer.txt", 0); code != http.StatusCreated {
		t.Fatalf("status %d", code)
	}
	if got := read(t, filepath.Join(data, "leer.txt")); got != "" {
		t.Errorf("file %q", got)
	}
}

func TestUploadNeverOverwrites(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "da.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := start(t, h, "/da.txt", 3); code != http.StatusConflict {
		t.Errorf("existing file: status %d, want 409", code)
	}

	// A file that appears while the upload runs is not overwritten either.
	_, id := start(t, h, "/spaet.txt", 3)
	if err := os.WriteFile(filepath.Join(data, "spaet.txt"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := chunk(h, id, 0, "new"); code != http.StatusConflict {
		t.Errorf("file appeared meanwhile: status %d, want 409", code)
	}
	if got := read(t, filepath.Join(data, "spaet.txt")); got != "first" {
		t.Errorf("file %q, want first", got)
	}
}

func TestUploadBadNames(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "datei"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want int
	}{
		{"", http.StatusBadRequest},
		{"/", http.StatusBadRequest},
		{"/../x", http.StatusBadRequest},
		{"/a/../../x", http.StatusBadRequest},
		{"/a//b", http.StatusBadRequest},
		{"/a\\b", http.StatusBadRequest},
		{"/" + uploadDir + "/x", http.StatusBadRequest},
		{"/" + strings.Repeat("x", 256), http.StatusBadRequest},
	}
	for _, tt := range tests {
		if code, _ := start(t, h, tt.path, 1); code != tt.want {
			t.Errorf("%q: status %d, want %d", tt.path, code, tt.want)
		}
	}
	// A folder in the path that is a file fails at the end.
	_, id := start(t, h, "/datei/x.txt", 1)
	if code, _ := chunk(h, id, 0, "x"); code != http.StatusConflict {
		t.Errorf("file as folder: status %d, want 409", code)
	}
}

func TestUploadNeedsJSON(t *testing.T) {
	h, _ := uploadSetup(t)
	if code, _ := do(h, http.MethodPost, "/api/uploads", "text/plain", `{"path":"/a","size":1}`, nil); code != http.StatusUnsupportedMediaType {
		t.Errorf("status %d, want 415", code)
	}
}

func TestUploadFolderHidden(t *testing.T) {
	h, _ := uploadSetup(t)
	_, id := start(t, h, "/a.txt", 5)
	chunk(h, id, 0, "ab")
	l := listing(t, get(h, "/api/files", "/", nil))
	if len(l.Entries) != 0 {
		t.Errorf("listing shows %+v", l.Entries)
	}
	if rec := get(h, "/api/download", "/"+uploadDir+"/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("download of a chunk file: status %d, want 404", rec.Code)
	}
}
