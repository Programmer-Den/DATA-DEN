package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Uploads come in chunks, so a large video survives a broken connection:
//
//	POST /api/uploads {"path": "/Fotos/a.jpg", "size": 123}  -> {"id": "...", "offset": 0}
//	PUT  /api/uploads/{id}, header Upload-Offset: 0, body: the next chunk  -> {"offset": n}
//	GET  /api/uploads/{id}  -> {"offset": n}, to resume after an error
//
// The chunks go into a file in uploadDir. With the last chunk it is moved to
// its path, and the answer adds "duplicates": the other files with the same
// content. An existing file is only replaced when the start says
// "replace": true, and its old content is then kept as a version.
// The answer also has "mod", the file's time in nanoseconds. With
// "expect" {"size", "mod"} (see sync.go) a replace only happens while the
// file is still so.
const (
	uploadDir = ".data-den-uploads"
	// MaxChunk is the most one PUT may carry.
	MaxChunk = 32 << 20
)

type upload struct {
	mu     sync.Mutex
	path   string // within the root, such as "Fotos/a.jpg"
	size   int64
	offset int64
	// replace allows overwriting a file, which is kept as a version.
	replace bool
	// hash is the checksum of what arrived so far.
	hash hash.Hash
	// expect is what the file at path must still be to be replaced; nil
	// for no condition.
	expect *fileState
}

type uploads struct {
	mu   sync.Mutex
	byID map[string]*upload
}

func (f *Files) registerUploads(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	// Chunks of uploads that a restart cut off.
	if err := f.root.RemoveAll(uploadDir); err != nil {
		slog.Warn("removing unfinished uploads", "err", err)
	}
	mux.HandleFunc("POST /api/uploads", require(f.startUpload))
	mux.HandleFunc("PUT /api/uploads/{id}", require(f.putChunk))
	mux.HandleFunc("GET /api/uploads/{id}", require(f.uploadState))
}

func (f *Files) startUpload(w http.ResponseWriter, r *http.Request) {
	// A JSON body cannot be sent by a plain HTML form on another site.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return
	}
	var body struct {
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		Replace bool   `json:"replace"`
		// Expect is what the file must still be to be replaced.
		Expect *fileState `json:"expect"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Size < 0 {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Dateiname")
		return
	}
	if body.Expect != nil {
		body.Replace = true
		if err := f.still(name, *body.Expect); err != nil {
			f.stateError(w, err)
			return
		}
	}
	if info, err := f.root.Lstat(name); err == nil && (!body.Replace || !info.Mode().IsRegular()) {
		writeError(w, http.StatusConflict, "Datei existiert bereits")
		return
	}
	if ok, err := f.fits(body.Size); err != nil {
		f.diskError(w, "counting the usage", err)
		return
	} else if !ok {
		writeError(w, http.StatusInsufficientStorage, "Dein Speicherplatz ist voll")
		return
	}

	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Upload nicht möglich")
		return
	}
	if err := f.root.MkdirAll(uploadDir, 0o750); err != nil {
		f.diskError(w, "creating the upload folder", err)
		return
	}
	chunks, err := f.root.OpenFile(path.Join(uploadDir, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		f.diskError(w, "creating an upload", err)
		return
	}
	_ = chunks.Close()

	u := &upload{path: name, size: body.Size, replace: body.Replace, expect: body.Expect, hash: sha256.New()}
	f.uploads.mu.Lock()
	f.uploads.byID[id] = u
	f.uploads.mu.Unlock()

	answer := map[string]any{"id": id, "offset": u.offset}
	if body.Size == 0 {
		_, mod, ok := f.finish(w, id, u)
		if !ok {
			return
		}
		f.noteUpload(r, u)
		answer["mod"] = mod
	}
	writeJSON(w, http.StatusCreated, answer)
}

func (f *Files) putChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := f.upload(id)
	if u == nil {
		writeError(w, http.StatusNotFound, "Upload nicht gefunden")
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Upload-Offset fehlt")
		return
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if offset != u.offset {
		// The client lost track, for example after a timeout; it asks for
		// the offset and goes on from there.
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Falscher Offset", "offset": u.offset})
		return
	}
	chunks, err := f.root.OpenFile(path.Join(uploadDir, id), os.O_WRONLY, 0)
	if err != nil {
		f.diskError(w, "opening an upload", err)
		return
	}
	defer func() { _ = chunks.Close() }()
	if _, err := chunks.Seek(u.offset, io.SeekStart); err != nil {
		f.diskError(w, "seeking in an upload", err)
		return
	}
	// Never more than the announced size, and never more than one chunk;
	// anything beyond is dropped, and the answer's offset says what counts.
	limit := min(u.size-u.offset, MaxChunk)
	written, err := io.Copy(hashingWriter{chunks, u.hash}, io.LimitReader(r.Body, limit))
	u.offset += written
	if err != nil {
		// What arrived stays; the client resumes from the new offset.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Übertragung abgebrochen", "offset": u.offset})
		return
	}
	if u.offset == u.size {
		if err := chunks.Close(); err != nil {
			f.diskError(w, "closing an upload", err)
			return
		}
		duplicates, mod, ok := f.finish(w, id, u)
		if !ok {
			return
		}
		f.noteUpload(r, u)
		writeJSON(w, http.StatusOK, map[string]any{"offset": u.offset, "mod": mod, "duplicates": duplicates})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offset": u.offset})
}

func (f *Files) uploadState(w http.ResponseWriter, r *http.Request) {
	u := f.upload(r.PathValue("id"))
	if u == nil {
		writeError(w, http.StatusNotFound, "Upload nicht gefunden")
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"offset": u.offset, "size": u.size})
}

// finish moves a complete upload to its path and returns the other files
// with the same content and the new file's time. It answers the request
// itself and returns false when that fails.
func (f *Files) finish(w http.ResponseWriter, id string, u *upload) ([]string, int64, bool) {
	f.uploads.mu.Lock()
	delete(f.uploads.byID, id)
	f.uploads.mu.Unlock()
	chunks := path.Join(uploadDir, id)
	defer func() { _ = f.root.Remove(chunks) }()

	if dir := path.Dir(u.path); dir != "." {
		if err := f.root.MkdirAll(dir, 0o750); err != nil {
			f.diskError(w, "creating a folder", err)
			return nil, 0, false
		}
	}
	// A hard link fails if the name is taken, so nothing is overwritten,
	// not even a file that appeared during the upload, unless asked to.
	link := f.root.Link
	if u.replace {
		link = f.replaceFile
	}
	if u.expect != nil {
		link = f.replaceIf(*u.expect)
	}
	if err := link(chunks, u.path); err != nil {
		if errors.Is(err, errChangedMeanwhile) {
			f.stateError(w, err)
			return nil, 0, false
		}
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Datei existiert bereits")
			return nil, 0, false
		}
		f.diskError(w, "finishing an upload", err)
		return nil, 0, false
	}
	f.addUsage(u.size)
	slog.Info("uploaded", "path", "/"+u.path, "size", u.size)
	info, err := f.root.Stat(u.path)
	if err != nil {
		// Uploaded; the next check makes its checksum.
		return []string{}, 0, true
	}
	return f.uploaded(u.path, info, hex.EncodeToString(u.hash.Sum(nil))), info.ModTime().UnixNano(), true
}

// noteUpload records a finished upload in the audit log.
func (f *Files) noteUpload(r *http.Request, u *upload) {
	action := "upload"
	if u.replace {
		action = "replace"
	}
	f.note(r, action, "/"+u.path)
}

func (f *Files) upload(id string) *upload {
	f.uploads.mu.Lock()
	defer f.uploads.mu.Unlock()
	return f.uploads.byID[id]
}

func (f *Files) diskError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, fs.ErrPermission) {
		writeError(w, http.StatusForbidden, "Kein Zugriff")
		return
	}
	if errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), "not a directory") {
		writeError(w, http.StatusConflict, "Ein Teil des Pfads ist eine Datei")
		return
	}
	slog.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
}

// uploadName checks the path of a new file, such as "/Fotos/2026/a.jpg", and
// returns it within the root. Each part must be a plain name, and none may
// be the upload folder.
func uploadName(p string) (string, bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 ||
			strings.ContainsAny(part, "\x00\\") || strings.HasPrefix(part, ".data-den-") {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
