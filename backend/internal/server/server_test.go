package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/tiles"
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
		// The page of a share link.
		{"/s/0123456789abcdef0123456789abcdef", "<app-root>"},
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

	cookies := rec.Result().Cookies()
	for _, endpoint := range []string{"/api/files?path=/", "/api/duplicates", "/api/integrity"} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without data directory: status %d, want 503", endpoint, rec.Code)
		}
	}
	// Share links answer without a login.
	if rec := get(t, h, "/api/s/0123456789abcdef0123456789abcdef"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/api/s/ without data directory: status %d, want 503", rec.Code)
	}
}

func TestStatusAPI(t *testing.T) {
	a, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Files: fstest.MapFS{}, Auth: a, Status: status.New(status.Config{DataDir: t.TempDir()})})
	if rec := get(t, h, "/api/status"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/status without login: status %d, want 401", rec.Code)
	}
}

func TestTilesNeedLogin(t *testing.T) {
	a, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	mapTiles, err := tiles.New(tiles.Config{URL: "http://127.0.0.1:1", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Files: fstest.MapFS{}, Auth: a, Tiles: mapTiles})
	if rec := get(t, h, "/api/tiles/0/0/0"); rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/tiles without login: status %d, want 401", rec.Code)
	}
}

func TestOfficeNeedsLogin(t *testing.T) {
	a, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := files.OpenSpaces(t.TempDir(), files.SpacesConfig{User: auth.Username})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	collabora, _ := url.Parse("http://127.0.0.1:1")
	office := data.Office(files.OfficeConfig{URL: collabora, WOPI: "http://127.0.0.1:9981"})
	h := New(Config{Files: fstest.MapFS{"index.html": {Data: []byte("app")}}, Auth: a, Data: data, Office: office})
	for _, target := range []string{"/api/office", "/browser/abc/cool.html", "/cool/ws", "/hosting/discovery"} {
		if rec := get(t, h, target); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without login: status %d, want 401", target, rec.Code)
		}
	}
	// Everything else is as without office.
	if rec := get(t, h, "/"); rec.Code != http.StatusOK || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("/: status %d, X-Frame-Options %q", rec.Code, rec.Header().Get("X-Frame-Options"))
	}
}

func TestSyncClientDownload(t *testing.T) {
	hash, err := auth.HashPassword("richtig")
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{Username: "chriszly", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	clients := fstest.MapFS{"data-den-sync-windows-amd64.exe": {Data: []byte("MZ")}, "geheim.txt": {Data: []byte("x")}}
	h := New(Config{Files: fstest.MapFS{}, Auth: a, Clients: clients})
	if rec := get(t, h, "/api/sync/client/data-den-sync-windows-amd64.exe"); rec.Code != http.StatusUnauthorized {
		t.Errorf("without login: status %d, want 401", rec.Code)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"chriszly","password":"richtig"}`))
	login.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, login)
	cookies := rec.Result().Cookies()
	for name, want := range map[string]int{"data-den-sync-windows-amd64.exe": http.StatusOK, "data-den-sync-windows-arm64.exe": http.StatusNotFound, "geheim.txt": http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/client/"+name, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
		if want == http.StatusOK && (rec.Body.String() != "MZ" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment")) {
			t.Errorf("%s: body %q, headers %v", name, rec.Body.String(), rec.Header())
		}
	}
}
