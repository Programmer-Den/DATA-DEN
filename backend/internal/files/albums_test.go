package files

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func albumOf(t *testing.T, body []byte) Album {
	t.Helper()
	var a Album
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return a
}

func albumList(t *testing.T, h http.Handler) []Album {
	t.Helper()
	rec := get(h, "/api/albums", "", nil)
	var list struct{ Albums []Album }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	return list.Albums
}

func TestAlbums(t *testing.T) {
	data, h := tree(t)
	if got := albumList(t, h); len(got) != 0 {
		t.Fatalf("at first: %+v", got)
	}

	rec := post(h, "/api/albums", `{"name": "  Hochzeit  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body)
	}
	a := albumOf(t, rec.Body.Bytes())
	if a.Name != "Hochzeit" || len(a.ID) != 32 || a.Paths == nil {
		t.Errorf("album %+v", a)
	}

	rec = post(h, "/api/albums/add", `{"id": "`+a.ID+`", "paths": ["/Fotos/2026/b.jpg", "/Fotos/a.jpg", "/Fotos/2026/b.jpg"]}`)
	if got := albumOf(t, rec.Body.Bytes()).Paths; !slices.Equal(got, []string{"/Fotos/2026/b.jpg", "/Fotos/a.jpg"}) {
		t.Errorf("after add: %v", got)
	}
	// Adding again keeps the order.
	post(h, "/api/albums/add", `{"id": "`+a.ID+`", "paths": ["Fotos/a.jpg"]}`)

	// Moving the folder takes the paths along, and they are kept on disk.
	if rec := post(h, "/api/move", `{"from": "/Fotos/2026", "to": "/2026"}`); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if got := albumList(t, h)[0].Paths; !slices.Equal(got, []string{"/2026/b.jpg", "/Fotos/a.jpg"}) {
		t.Errorf("after the move: %v", got)
	}
	again := handler(t, data)
	if got := albumList(t, again)[0].Paths; !slices.Equal(got, []string{"/2026/b.jpg", "/Fotos/a.jpg"}) {
		t.Errorf("read again: %v", got)
	}

	rec = post(h, "/api/albums/remove", `{"id": "`+a.ID+`", "paths": ["/Fotos/a.jpg"]}`)
	if got := albumOf(t, rec.Body.Bytes()).Paths; !slices.Equal(got, []string{"/2026/b.jpg"}) {
		t.Errorf("after remove: %v", got)
	}
	if !exists(data, "Fotos/a.jpg") {
		t.Error("removing from the album deleted the file")
	}

	rec = post(h, "/api/albums/rename", `{"id": "`+a.ID+`", "name": "Hochzeit 2026"}`)
	if albumOf(t, rec.Body.Bytes()).Name != "Hochzeit 2026" {
		t.Errorf("rename: %s", rec.Body)
	}
	if rec := post(h, "/api/albums/delete", `{"id": "`+a.ID+`"}`); rec.Code != http.StatusOK {
		t.Errorf("delete: %d", rec.Code)
	}
	if got := albumList(t, h); len(got) != 0 || !exists(data, "2026/b.jpg") {
		t.Errorf("after delete: %+v", got)
	}
}

func TestAlbumErrors(t *testing.T) {
	data, h := tree(t)
	a := albumOf(t, post(h, "/api/albums", `{"name": "Urlaub"}`).Body.Bytes())
	tests := []struct {
		endpoint, body string
		want           int
	}{
		{"/api/albums", `{"name": ""}`, http.StatusBadRequest},
		{"/api/albums", `{"name": "` + strings.Repeat("x", maxAlbumName+1) + `"}`, http.StatusBadRequest},
		{"/api/albums", `{"name": "a\nb"}`, http.StatusBadRequest},
		{"/api/albums/rename", `{"id": "nope", "name": "x"}`, http.StatusNotFound},
		{"/api/albums/delete", `{"id": "nope"}`, http.StatusNotFound},
		{"/api/albums/add", `{"id": "nope", "paths": ["/Fotos/a.jpg"]}`, http.StatusNotFound},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": []}`, http.StatusBadRequest},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": ["/notizen.txt"]}`, http.StatusBadRequest},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": ["/Fotos"]}`, http.StatusBadRequest},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": ["/fehlt.jpg"]}`, http.StatusNotFound},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": ["/../x.jpg"]}`, http.StatusBadRequest},
		{"/api/albums/add", `{"id": "` + a.ID + `", "paths": ["/` + albumsFile + `"]}`, http.StatusBadRequest},
		{"/api/albums/remove", `{"id": "nope", "paths": ["/Fotos/a.jpg"]}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := post(h, tt.endpoint, tt.body); rec.Code != tt.want {
			t.Errorf("%s %s: status %d, want %d (%s)", tt.endpoint, tt.body, rec.Code, tt.want, rec.Body)
		}
	}

	// The album file is never served or listed.
	if rec := get(h, "/api/download", "/"+albumsFile, nil); rec.Code != http.StatusNotFound {
		t.Errorf("download of the album file: %d", rec.Code)
	}
	// A broken file answers 500 and is not overwritten.
	if err := os.WriteFile(filepath.Join(data, filepath.FromSlash(albumsFile)), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := handler(t, data)
	if rec := post(broken, "/api/albums", `{"name": "x"}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("broken file: status %d", rec.Code)
	}
	if got, _ := os.ReadFile(filepath.Join(data, filepath.FromSlash(albumsFile))); string(got) != "{" { //nolint:gosec // the test's own directory
		t.Errorf("broken file overwritten: %q", got)
	}
}
