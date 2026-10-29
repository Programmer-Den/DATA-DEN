package files

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// post sends a JSON body to endpoint.
func post(h http.Handler, endpoint, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tree makes a data directory with Fotos/a.jpg, Fotos/2026/b.jpg and
// notizen.txt.
func tree(t *testing.T) (string, http.Handler) {
	t.Helper()
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "Fotos", "2026"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Fotos/a.jpg", "Fotos/2026/b.jpg", "notizen.txt"} {
		if err := os.WriteFile(filepath.Join(data, filepath.FromSlash(name)), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data, handler(t, data)
}

func exists(data, name string) bool {
	_, err := os.Lstat(filepath.Join(data, filepath.FromSlash(name)))
	return err == nil
}

func TestCreateFolder(t *testing.T) {
	data, h := tree(t)
	if rec := post(h, "/api/folders", `{"path":"/Fotos/Neu"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if info, err := os.Stat(filepath.Join(data, "Fotos", "Neu")); err != nil || !info.IsDir() {
		t.Errorf("folder not made: %v", err)
	}
	tests := []struct {
		body string
		want int
	}{
		{`{"path":"/Fotos/Neu"}`, http.StatusConflict},
		{`{"path":"/notizen.txt"}`, http.StatusConflict},
		{`{"path":"/fehlt/Neu"}`, http.StatusNotFound},
		{`{"path":"/"}`, http.StatusBadRequest},
		{`{"path":"/../raus"}`, http.StatusBadRequest},
		{`{"path":"/.data-den-thumbs"}`, http.StatusBadRequest},
		{`kein json`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/folders", tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
}

func TestMove(t *testing.T) {
	data, h := tree(t)
	// Rename a file.
	if rec := post(h, "/api/move", `{"from":"/notizen.txt","to":"/Notizen 2026.txt"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: status %d, body %s", rec.Code, rec.Body)
	}
	if exists(data, "notizen.txt") || !exists(data, "Notizen 2026.txt") {
		t.Error("file not renamed")
	}
	// Move a folder with its content.
	if rec := post(h, "/api/move", `{"from":"/Fotos/2026","to":"/2026"}`); rec.Code != http.StatusOK {
		t.Fatalf("move folder: status %d, body %s", rec.Code, rec.Body)
	}
	if !exists(data, "2026/b.jpg") || exists(data, "Fotos/2026") {
		t.Error("folder not moved")
	}
}

func TestMoveRefuses(t *testing.T) {
	data, h := tree(t)
	tests := []struct {
		body string
		want int
	}{
		// Never overwrites.
		{`{"from":"/notizen.txt","to":"/Fotos/a.jpg"}`, http.StatusConflict},
		{`{"from":"/Fotos/2026","to":"/Fotos"}`, http.StatusConflict},
		{`{"from":"/Fotos","to":"/Fotos/2026/Fotos"}`, http.StatusBadRequest},
		{`{"from":"/fehlt.txt","to":"/da.txt"}`, http.StatusNotFound},
		{`{"from":"/notizen.txt","to":"/fehlt/notizen.txt"}`, http.StatusNotFound},
		{`{"from":"/notizen.txt","to":"/notizen.txt/x"}`, http.StatusBadRequest},
		{`{"from":"/notizen.txt","to":"/../notizen.txt"}`, http.StatusBadRequest},
		{`{"from":"/notizen.txt","to":"/.data-den-uploads/x"}`, http.StatusBadRequest},
		{`{"from":"/","to":"/x"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/move", tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
	got, err := os.ReadFile(filepath.Clean(filepath.Join(data, "Fotos", "a.jpg")))
	if err != nil || string(got) != "Fotos/a.jpg" {
		t.Errorf("a.jpg changed: %q, %v", got, err)
	}
	if !exists(data, "notizen.txt") {
		t.Error("notizen.txt lost")
	}
}

func TestDelete(t *testing.T) {
	data, h := tree(t)
	if rec := post(h, "/api/delete", `{"path":"/notizen.txt"}`); rec.Code != http.StatusOK {
		t.Fatalf("file: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/delete", `{"path":"/Fotos"}`); rec.Code != http.StatusOK {
		t.Fatalf("folder: status %d, body %s", rec.Code, rec.Body)
	}
	if exists(data, "notizen.txt") || exists(data, "Fotos") {
		t.Error("not deleted")
	}
	tests := []struct {
		body string
		want int
	}{
		{`{"path":"/"}`, http.StatusBadRequest},
		{`{"path":""}`, http.StatusBadRequest},
		{`{"path":"/.."}`, http.StatusBadRequest},
		{`{"path":"/.data-den-thumbs"}`, http.StatusBadRequest},
		{`{"path":"/fehlt"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/delete", tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
	if !exists(data, ".") {
		t.Error("the data directory is gone")
	}
}

func TestDeleteSymlinkKeepsTarget(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.Mkdir(data, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "draussen")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "wichtig.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}
	h := handler(t, data)
	post(h, "/api/delete", `{"path":"/link"}`)
	post(h, "/api/delete", `{"path":"/link/wichtig.txt"}`)
	if _, err := os.Stat(filepath.Join(outside, "wichtig.txt")); err != nil {
		t.Errorf("a file outside the data directory was deleted: %v", err)
	}
}

func TestManageNeedsJSONAndLogin(t *testing.T) {
	_, h := tree(t)
	for _, endpoint := range []string{"/api/folders", "/api/move", "/api/delete"} {
		req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`path=/notizen.txt`))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s as a form: status %d, want 415", endpoint, rec.Code)
		}
	}
	loggedOut := setup(t, false)
	for _, endpoint := range []string{"/api/folders", "/api/move", "/api/delete"} {
		if rec := post(loggedOut, endpoint, `{"path":"/notizen.txt"}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without login: status %d, want 401", endpoint, rec.Code)
		}
	}
}
