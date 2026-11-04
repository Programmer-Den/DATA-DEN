package files

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Own albums: photos and videos put together by hand, without moving them.
// An album keeps the paths of its files; moving a file or folder takes them
// along. A file in the recycle bin keeps its place and is back in the album
// when restored.
//
//	GET  /api/albums                                  -> {"albums": [...]}
//	POST /api/albums        {"name"}                  -> 201, the album
//	POST /api/albums/rename {"id", "name"}
//	POST /api/albums/delete {"id"}                    (the files stay)
//	POST /api/albums/add    {"id", "paths": [...]}
//	POST /api/albums/remove {"id", "paths": [...]}
const (
	albumsFile = ".data-den-albums/albums.json"
	// maxAlbums and maxAlbumItems keep the file small enough to read at once.
	maxAlbums     = 1000
	maxAlbumItems = 100_000
	maxAlbumName  = 100
	// maxPathsBody is the largest add or remove request: a few thousand
	// paths.
	maxPathsBody = 1 << 20
)

// Album is an own album.
type Album struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	// Paths are the album's files from the top, such as "/Fotos/a.jpg", in
	// the order they were added.
	Paths []string `json:"paths"`
}

type albums struct {
	mu sync.Mutex
	// list is nil until it was read from albumsFile.
	list []*Album
}

func (f *Files) registerAlbums(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/albums", require(f.listAlbums))
	mux.HandleFunc("POST /api/albums", require(f.createAlbum))
	mux.HandleFunc("POST /api/albums/rename", require(f.renameAlbum))
	mux.HandleFunc("POST /api/albums/delete", require(f.deleteAlbum))
	mux.HandleFunc("POST /api/albums/add", require(f.addToAlbum))
	mux.HandleFunc("POST /api/albums/remove", require(f.removeFromAlbum))
}

// loadAlbumsLocked reads the albums on first use. The caller holds mu.
func (f *Files) loadAlbumsLocked() error {
	if f.albums.list != nil {
		return nil
	}
	data, err := f.root.ReadFile(albumsFile)
	if errors.Is(err, fs.ErrNotExist) {
		f.albums.list = []*Album{}
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Album
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	f.albums.list = list
	return nil
}

// saveAlbumsLocked writes the albums through a temporary file. The caller
// holds mu.
func (f *Files) saveAlbumsLocked() error {
	data, err := json.Marshal(f.albums.list)
	if err != nil {
		return err
	}
	if err := f.root.MkdirAll(path.Dir(albumsFile), 0o750); err != nil {
		return err
	}
	tmp := albumsFile + ".tmp"
	if err := f.root.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return f.root.Rename(tmp, albumsFile)
}

// withAlbums runs change on the albums and saves them when it returns true.
// It answers the request itself when reading or saving fails.
func (f *Files) withAlbums(w http.ResponseWriter, change func() (status int, v any, save bool)) {
	f.albums.mu.Lock()
	defer f.albums.mu.Unlock()
	if err := f.loadAlbumsLocked(); err != nil {
		slog.Error("reading the albums", "err", err)
		writeError(w, http.StatusInternalServerError, "Alben nicht lesbar")
		return
	}
	status, v, save := change()
	if save {
		if err := f.saveAlbumsLocked(); err != nil {
			// What is in memory is what the file holds.
			f.albums.list = nil
			f.diskError(w, "saving the albums", err)
			return
		}
	}
	writeJSON(w, status, v)
}

func (f *Files) albumLocked(id string) *Album {
	for _, a := range f.albums.list {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// albumName checks and trims a name; "" means it is not acceptable.
func albumName(name string) string {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxAlbumName || strings.ContainsAny(name, "\x00\n\r") {
		return ""
	}
	return name
}

func (f *Files) listAlbums(w http.ResponseWriter, _ *http.Request) {
	f.withAlbums(w, func() (int, any, bool) {
		return http.StatusOK, map[string][]*Album{"albums": f.albums.list}, false
	})
}

func (f *Files) createAlbum(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := albumName(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Album nicht möglich")
		return
	}
	f.withAlbums(w, func() (int, any, bool) {
		if len(f.albums.list) >= maxAlbums {
			return http.StatusConflict, map[string]string{"error": "Zu viele Alben"}, false
		}
		a := &Album{ID: id, Name: name, Created: time.Now().UTC(), Paths: []string{}}
		f.albums.list = append(f.albums.list, a)
		return http.StatusCreated, a, true
	})
}

func (f *Files) renameAlbum(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := albumName(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	f.withAlbums(w, func() (int, any, bool) {
		a := f.albumLocked(body.ID)
		if a == nil {
			return http.StatusNotFound, map[string]string{"error": "Album nicht gefunden"}, false
		}
		a.Name = name
		return http.StatusOK, a, true
	})
}

func (f *Files) deleteAlbum(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	f.withAlbums(w, func() (int, any, bool) {
		i := slices.IndexFunc(f.albums.list, func(a *Album) bool { return a.ID == body.ID })
		if i < 0 {
			return http.StatusNotFound, map[string]string{"error": "Album nicht gefunden"}, false
		}
		f.albums.list = slices.Delete(f.albums.list, i, i+1)
		return http.StatusOK, map[string]string{"id": body.ID}, true
	})
}

// albumPaths reads an add or remove request: the album and the files, as
// paths from the top. It answers the request itself when they are not
// acceptable.
func albumPaths(w http.ResponseWriter, r *http.Request) (string, []string, bool) {
	var body struct {
		ID    string   `json:"id"`
		Paths []string `json:"paths"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return "", nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPathsBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return "", nil, false
	}
	paths := make([]string, 0, len(body.Paths))
	for _, p := range body.Paths {
		name, ok := uploadName(p)
		if !ok {
			writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
			return "", nil, false
		}
		paths = append(paths, "/"+name)
	}
	return body.ID, paths, true
}

// addToAlbum adds photos and videos; files already in it stay where they are.
func (f *Files) addToAlbum(w http.ResponseWriter, r *http.Request) {
	id, paths, ok := albumPaths(w, r)
	if !ok {
		return
	}
	for _, p := range paths {
		info, err := f.root.Stat(strings.TrimPrefix(p, "/"))
		if err != nil {
			openError(w, err)
			return
		}
		if kind := formatOf(p).kind; !info.Mode().IsRegular() || (kind != "photo" && kind != "video") {
			writeError(w, http.StatusBadRequest, "Nur Fotos und Videos")
			return
		}
	}
	f.withAlbums(w, func() (int, any, bool) {
		a := f.albumLocked(id)
		if a == nil {
			return http.StatusNotFound, map[string]string{"error": "Album nicht gefunden"}, false
		}
		for _, p := range paths {
			if !slices.Contains(a.Paths, p) {
				if len(a.Paths) >= maxAlbumItems {
					return http.StatusConflict, map[string]string{"error": "Album ist voll"}, false
				}
				a.Paths = append(a.Paths, p)
			}
		}
		return http.StatusOK, a, true
	})
}

// removeFromAlbum takes files out of an album; the files themselves stay.
func (f *Files) removeFromAlbum(w http.ResponseWriter, r *http.Request) {
	id, paths, ok := albumPaths(w, r)
	if !ok {
		return
	}
	f.withAlbums(w, func() (int, any, bool) {
		a := f.albumLocked(id)
		if a == nil {
			return http.StatusNotFound, map[string]string{"error": "Album nicht gefunden"}, false
		}
		a.Paths = slices.DeleteFunc(a.Paths, func(p string) bool { return slices.Contains(paths, p) })
		return http.StatusOK, a, true
	})
}

// albumsMoved follows a move of a file or folder from one path to another,
// both within the root.
func (f *Files) albumsMoved(from, to string) {
	f.albums.mu.Lock()
	defer f.albums.mu.Unlock()
	if err := f.loadAlbumsLocked(); err != nil {
		slog.Error("reading the albums", "err", err)
		return
	}
	changed := false
	for _, a := range f.albums.list {
		for i, p := range a.Paths {
			if p == "/"+from {
				a.Paths[i], changed = "/"+to, true
			} else if rest, ok := strings.CutPrefix(p, "/"+from+"/"); ok {
				a.Paths[i], changed = "/"+to+"/"+rest, true
			}
		}
	}
	if changed {
		if err := f.saveAlbumsLocked(); err != nil {
			f.albums.list = nil
			slog.Error("saving the albums", "err", err)
		}
	}
}
