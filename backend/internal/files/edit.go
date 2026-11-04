package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Editing text and Markdown files in the browser:
//
//	GET /api/text?path=/notizen.md  -> {"path": "...", "content": "...", "version": "..."}
//	PUT /api/text {"path": "/notizen.md", "content": "...", "version": "...", "backup": true} -> {"version": "..."}
//
// version is what GET answered; the save is refused with 409 when the file
// changed since, so two open editors do not overwrite each other. An empty
// version creates a new file and is refused when the name is taken. With
// backup, the file as it was is kept as a version (/api/versions) first;
// the editor asks for it on its first save only.
const (
	// MaxText is the largest text file that can be edited.
	MaxText = 1 << 20
	// editDir holds a file being saved until it replaces the old one.
	editDir = ".data-den-edits"
)

// editFormats are the extensions that can be edited as text.
var editFormats = map[string]bool{".txt": true, ".md": true, ".csv": true, ".log": true}

// editable is true for a text file the editor can open.
func editable(name string) bool {
	return editFormats[lowerExt(name)]
}

func (f *Files) registerEdit(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	// Saves that a restart cut off.
	if err := f.root.RemoveAll(editDir); err != nil {
		slog.Warn("removing unfinished saves", "err", err)
	}
	mux.HandleFunc("GET /api/text", require(f.readText))
	mux.HandleFunc("PUT /api/text", require(f.writeText))
}

// versionOf names the state of a file, so a save can tell that it changed.
func versionOf(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

func (f *Files) readText(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if hidden(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	if !editable(name) {
		writeError(w, http.StatusUnsupportedMediaType, "Nicht als Text bearbeitbar")
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
	if info.Size() > MaxText {
		writeError(w, http.StatusRequestEntityTooLarge, "Die Datei ist zu groß zum Bearbeiten")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxText+1))
	if err != nil {
		f.diskError(w, "reading a text file", err)
		return
	}
	// A file that is not UTF-8 would be changed by the round trip through
	// JSON, so it is not offered.
	if len(data) > MaxText || !utf8.Valid(data) {
		writeError(w, http.StatusUnsupportedMediaType, "Keine UTF-8-Textdatei")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"path": "/" + name, "content": string(data), "version": versionOf(info),
	})
}

func (f *Files) writeText(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Version string `json:"version"`
		Backup  bool   `json:"backup"`
	}
	// Escaped in JSON, a character takes at most six bytes.
	r.Body = http.MaxBytesReader(w, r.Body, 6*MaxText+8192)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	name, ok := uploadName(body.Path)
	if !ok || !editable(name) {
		writeError(w, http.StatusBadRequest, "Ungültiger Dateiname")
		return
	}
	if len(body.Content) > MaxText {
		writeError(w, http.StatusRequestEntityTooLarge, "Der Text ist zu groß")
		return
	}
	version, ok := f.save(w, name, body.Version, body.Backup, strings.NewReader(body.Content))
	if !ok {
		return
	}
	action := "edit"
	if body.Version == "" {
		action = "create"
	}
	f.note(r, action, "/"+name)
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + name, "version": version})
}

// save writes content to name and returns its new version. expected is the
// version the writer started from, "" for a new file; backup keeps the old
// file as a version first. It answers the request itself and
// returns false when that fails.
func (f *Files) save(w http.ResponseWriter, name, expected string, backup bool, content io.Reader) (string, bool) {
	f.versionMu.Lock()
	defer f.versionMu.Unlock()

	var oldSize int64
	if expected != "" {
		info, err := f.root.Lstat(name)
		if err != nil {
			openError(w, err)
			return "", false
		}
		if !info.Mode().IsRegular() {
			writeError(w, http.StatusBadRequest, "Keine Datei")
			return "", false
		}
		if versionOf(info) != expected {
			writeError(w, http.StatusConflict, "Die Datei wurde inzwischen geändert")
			return "", false
		}
		oldSize = info.Size()
	} else if _, err := f.root.Lstat(name); err == nil {
		writeError(w, http.StatusConflict, "Datei existiert bereits")
		return "", false
	}
	if dir, err := f.root.Stat(path.Dir(name)); err != nil || !dir.IsDir() {
		writeError(w, http.StatusNotFound, "Ordner nicht gefunden")
		return "", false
	}

	if err := f.root.MkdirAll(editDir, 0o750); err != nil {
		f.diskError(w, "creating the folder for saving", err)
		return "", false
	}
	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return "", false
	}
	temp := path.Join(editDir, id)
	defer func() { _ = f.root.Remove(temp) }()
	file, err := f.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		f.diskError(w, "creating a file to save", err)
		return "", false
	}
	sum := sha256.New()
	size, err := io.Copy(hashingWriter{file, sum}, content)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		f.diskError(w, "saving a file", err)
		return "", false
	}

	// What is added: the whole file when the old one is kept as a version,
	// otherwise only the difference.
	added := size - oldSize
	if backup || expected == "" {
		added = size
	}
	if ok, err := f.fits(added); err != nil {
		f.diskError(w, "counting the usage", err)
		return "", false
	} else if !ok {
		writeError(w, http.StatusInsufficientStorage, "Dein Speicherplatz ist voll")
		return "", false
	}

	if expected != "" && backup {
		info, err := f.root.Lstat(name)
		if err == nil {
			_, err = f.keepVersion(name, info)
		}
		if err != nil {
			f.diskError(w, "keeping the old version", err)
			return "", false
		}
		slog.Info("old content kept as a version", "path", "/"+name)
	}
	if expected != "" {
		// Replaces the old file in one step: a reader sees either it or
		// the new one.
		err = f.root.Rename(temp, name)
	} else {
		// A hard link fails if the name was taken meanwhile, so nothing
		// is overwritten.
		err = f.root.Link(temp, name)
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Datei existiert bereits")
			return "", false
		}
		f.diskError(w, "saving a file", err)
		return "", false
	}
	if backup {
		f.pruneVersions(name)
	}
	f.addUsage(added)
	info, err := f.root.Stat(name)
	if err != nil {
		f.diskError(w, "reading a saved file", err)
		return "", false
	}
	f.uploaded(name, info, hex.EncodeToString(sum.Sum(nil)))
	slog.Info("saved", "path", "/"+name, "size", size)
	return versionOf(info), true
}
