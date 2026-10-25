package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
)

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHealth(t *testing.T) {
	h := New(Config{Files: fstest.MapFS{}, Version: "1.2.3"})
	rec := get(t, h, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["version"] != "1.2.3" {
		t.Errorf("body %v", body)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options %q, want nosniff", got)
	}
}

func TestUnknownAPIPath(t *testing.T) {
	h := New(Config{Files: fstest.MapFS{"index.html": {Data: []byte("app")}}})
	if rec := get(t, h, "/api/nothing"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestSite(t *testing.T) {
	files := fstest.MapFS{
		"index.html": {Data: []byte("<app-root></app-root>")},
		"main.js":    {Data: []byte("console.log(1)")},
	}
	h := New(Config{Files: files})
	tests := []struct{ path, want string }{
		{"/", "<app-root>"},
		{"/main.js", "console.log"},
		// A route of the Angular app, not a file: gets index.html.
		{"/files/holiday", "<app-root>"},
	}
	for _, tt := range tests {
		rec := get(t, h, tt.path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: status %d, body %q", tt.path, rec.Code, rec.Body.String())
		}
	}
}

func TestSiteNotBuilt(t *testing.T) {
	h := New(Config{Files: fstest.MapFS{}})
	if rec := get(t, h, "/"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

func TestLoginAPI(t *testing.T) {
	a, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Files: fstest.MapFS{}, Auth: a})
	if rec := get(t, h, "/api/session"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/session without login: status %d, want 401", rec.Code)
	}
}

func TestFilesWithoutDataDirectory(t *testing.T) {
	hash, err := auth.HashPassword("richtig")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{Username: "chriszly", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Files: fstest.MapFS{}, Auth: a})

	login := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"chriszly","password":"richtig"}`))
	login.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, login)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/files?path=/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/api/files without data directory: status %d, want 503", rec.Code)
	}
}
