package files

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

// Managing files and folders, all with a JSON body:
//
//	POST /api/folders {"path": "/Fotos/2026"}                -> 201, a new folder
//	POST /api/move    {"from": "/a.jpg", "to": "/Fotos/b.jpg"} -> 200, rename or move
//	POST /api/delete  {"path": "/Fotos/a.jpg"}                -> 200, a file or a whole folder
//
// Nothing is ever overwritten, and the data directory itself and the
// server's own folders cannot be changed.
func (f *Files) registerManage(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/folders", require(f.createFolder))
	mux.HandleFunc("POST /api/move", require(f.move))
	mux.HandleFunc("POST /api/delete", require(f.remove))
}

// readJSON reads a small JSON body into v. It answers the request itself and
// returns false when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// A JSON body cannot be sent by a plain HTML form on another site.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return false
	}
	return true
}

func (f *Files) createFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if err := f.root.Mkdir(name, 0o750); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Existiert bereits")
			return
		}
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Übergeordneter Ordner nicht gefunden")
			return
		}
		f.diskError(w, "creating a folder", err)
		return
	}
	slog.Info("folder created", "path", "/"+name)
	writeJSON(w, http.StatusCreated, map[string]string{"path": "/" + name})
}

func (f *Files) move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	from, ok := uploadName(body.From)
	to, ok2 := uploadName(body.To)
	if !ok || !ok2 {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if from == to {
		writeJSON(w, http.StatusOK, map[string]string{"path": "/" + to})
		return
	}
	if strings.HasPrefix(to, from+"/") {
		writeError(w, http.StatusBadRequest, "Ein Ordner kann nicht in sich selbst verschoben werden")
		return
	}
	info, err := f.root.Lstat(from)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.Mode().IsDir() && !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return
	}
	if dir, err := f.root.Stat(path.Dir(to)); err != nil || !dir.IsDir() {
		writeError(w, http.StatusNotFound, "Zielordner nicht gefunden")
		return
	}
	if _, err := f.root.Lstat(to); err == nil {
		writeError(w, http.StatusConflict, "Existiert bereits")
		return
	}
	if err := f.rename(from, to, info.IsDir()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Existiert bereits")
			return
		}
		f.diskError(w, "moving", err)
		return
	}
	slog.Info("moved", "from", "/"+from, "to", "/"+to)
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + to})
}

// rename moves a file or folder. A file is hard-linked and the old name
// removed, since a link fails if the name was taken in the meantime, where
// a rename would overwrite it. A folder is renamed: that fails onto anything
// but an empty folder, and the check before rules that out.
func (f *Files) rename(from, to string, dir bool) error {
	if dir {
		return f.root.Rename(from, to)
	}
	if err := f.root.Link(from, to); err != nil {
		return err
	}
	return f.root.Remove(from)
}

func (f *Files) remove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if _, err := f.root.Lstat(name); err != nil {
		openError(w, err)
		return
	}
	if err := f.root.RemoveAll(name); err != nil {
		f.diskError(w, "deleting", err)
		return
	}
	slog.Info("deleted", "path", "/"+name)
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + name})
}
