// Package files lists the folders of the data directory and serves its files
// for download. Every access goes through an os.Root, so no path, ".." or
// symlink reaches anything outside the data directory.
package files

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

// Entry is one file or folder in a listing.
type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Listing is the content of one folder.
type Listing struct {
	// Path is the folder, "/" for the data directory itself.
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

// Files serves the data directory.
type Files struct {
	root    *os.Root
	uploads uploads
}

// Open opens the data directory dir.
func Open(dir string) (*Files, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Files{root: root, uploads: uploads{byID: map[string]*upload{}}}, nil
}

// Close releases the data directory.
func (f *Files) Close() error {
	return f.root.Close()
}

// Register adds /api/files, /api/download and /api/uploads to mux, behind
// require (the login check).
func (f *Files) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/files", require(f.list))
	mux.HandleFunc("GET /api/download", require(f.download))
	f.registerUploads(mux, require)
}

func (f *Files) list(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	dir, err := f.root.Open(name)
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = dir.Close() }()
	infos, err := dir.Readdir(-1)
	if err != nil {
		// Readdir on a file fails; anything else is a fault of the disk.
		if info, statErr := dir.Stat(); statErr == nil && !info.IsDir() {
			writeError(w, http.StatusBadRequest, "Kein Ordner")
			return
		}
		slog.Error("listing a folder", "path", name, "err", err)
		writeError(w, http.StatusInternalServerError, "Ordner nicht lesbar")
		return
	}

	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		if !info.Mode().IsDir() && !info.Mode().IsRegular() {
			// Symlinks, devices and the like are not offered.
			continue
		}
		if name == "." && info.Name() == uploadDir {
			continue
		}
		entry := Entry{Name: info.Name(), Dir: info.IsDir(), Modified: info.ModTime().UTC()}
		if !entry.Dir {
			entry.Size = info.Size()
		}
		entries = append(entries, entry)
	}
	// Folders first, then by name.
	slices.SortFunc(entries, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	listed := "/"
	if name != "." {
		listed += name
	}
	writeJSON(w, http.StatusOK, Listing{Path: listed, Entries: entries})
}

func (f *Files) download(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if name == uploadDir || strings.HasPrefix(name, uploadDir+"/") {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	file, err := f.root.Open(name)
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	// ServeContent answers Range requests, so a broken download of a large
	// video can continue where it stopped.
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// relative turns the path of a request, such as "/Fotos/2026", into a name
// within the root, such as "Fotos/2026" ("." for the root). It answers the
// request itself when the path is not acceptable.
func relative(w http.ResponseWriter, p string) (string, bool) {
	if strings.ContainsRune(p, 0) || strings.Contains(p, "\\") {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
			return "", false
		}
	}
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		name = "."
	}
	return name, true
}

func openError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	if errors.Is(err, fs.ErrPermission) {
		writeError(w, http.StatusForbidden, "Kein Zugriff")
		return
	}
	// os.Root refuses paths that leave the data directory, such as symlinks
	// pointing outside it.
	writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
