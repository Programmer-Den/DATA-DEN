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
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/webdav"
)

// Entry is one file or folder in a listing.
type Entry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Kind is the view a file belongs to (photo, video, audio, document),
	// empty for folders and other files.
	Kind string `json:"kind,omitempty"`
	// Preview is true when /api/view can show the file in the browser.
	Preview bool `json:"preview,omitempty"`
	// Thumb is true when /api/thumb can make a thumbnail of the photo.
	Thumb bool `json:"thumb,omitempty"`
	// Text is true when /api/text can open the file in the editor.
	Text bool `json:"text,omitempty"`
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
	shares  shares
	// thumbSlots limits how many thumbnails are made at the same time.
	thumbSlots chan struct{}
	// trashDays is how long a deleted item can be restored.
	trashDays int
	index     index
	// quota is how many bytes may be stored, 0 for no limit.
	quota atomic.Int64
	// used caches what usage found.
	used usedCache
	// mux serves the API of this space alone, for Spaces.
	mux  *http.ServeMux
	sums sums
	// photos caches the EXIF data of the photos, for the timeline.
	photos photoCache
	albums albums
	// places names the towns of the photos; nil leaves them unnamed.
	places Namer
	// account is whose space this is, for the audit log.
	account string
	// audit notes changes in the audit log; nil notes nothing.
	audit Recorder
	// moved is told that from was moved to to, so shared folders follow;
	// nil for none.
	moved func(from, to string)
	// versionMu keeps two changes of the versions, or of a file and its
	// versions, from running at the same time.
	versionMu sync.Mutex
	// dav serves WebDAV, made on first use.
	dav     *webdav.Handler
	davOnce sync.Once
	// damaged is told about the files a check found damaged; nil tells no one.
	damaged func(account string, paths []string)
}

// Recorder notes in the audit log that account did action to target, in the
// request r.
type Recorder func(r *http.Request, account, action, target string)

// note records action on target in the audit log.
// In a shared folder, it is the account working there that did it, in the
// owner's space.
func (f *Files) note(r *http.Request, action, target string) {
	if f.audit == nil {
		return
	}
	if a, ok := r.Context().Value(actingKey{}).(acting); ok {
		f.audit(r, a.user, action, a.owner+":"+target)
		return
	}
	f.audit(r, f.account, action, target)
}

// noteBy records that user did action to target, such as an admin to a team
// folder.
func (f *Files) noteBy(r *http.Request, user, action, target string) {
	if f.audit != nil {
		f.audit(r, user, action, target)
	}
}

// Open opens the data directory dir.
func Open(dir string) (*Files, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return newFiles(root), nil
}

func newFiles(root *os.Root) *Files {
	f := &Files{
		root:       root,
		uploads:    uploads{byID: map[string]*upload{}},
		shares:     shares{unlocked: map[string]unlock{}},
		thumbSlots: make(chan struct{}, thumbWorkers),
		trashDays:  DefaultTrashDays,
	}
	f.loadSums()
	return f
}

// serve sets up mux with the API of this space; the login was checked
// before.
func (f *Files) serve() {
	f.mux = http.NewServeMux()
	pass := func(h http.HandlerFunc) http.HandlerFunc { return h }
	f.Register(f.mux, pass)
	f.RegisterShares(f.mux, pass, f.shares.guard)
}

// Close saves the checksums and releases the data directory.
func (f *Files) Close() error {
	f.sums.checker.Wait()
	f.saveSums()
	return f.root.Close()
}

// Register adds /api/files, /api/download, /api/view, /api/media,
// /api/thumb, /api/usage, /api/uploads, /api/folders, /api/move,
// /api/delete, /api/trash, /api/search, /api/duplicates, /api/integrity,
// /api/versions, /api/albums, /api/text and /api/sync/tree (all of Routes)
// to mux, behind require (the login check). The share links are added by
// RegisterShares.
func (f *Files) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/files", require(f.list))
	mux.HandleFunc("GET /api/download", require(f.download))
	mux.HandleFunc("GET /api/usage", require(f.showUsage))
	f.registerViews(mux, require)
	f.registerManage(mux, require)
	f.registerTrash(mux, require)
	f.registerSearch(mux, require)
	f.registerUploads(mux, require)
	f.registerSums(mux, require)
	f.registerVersions(mux, require)
	f.registerAlbums(mux, require)
	f.registerEdit(mux, require)
	f.registerSync(mux, require)
}

func (f *Files) list(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	listed := "/"
	if name != "." {
		listed += name
	}
	f.writeListing(w, name, listed)
}

// writeListing answers with the folder name, shown as listed.
func (f *Files) writeListing(w http.ResponseWriter, name, listed string) {
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
		if name == "." && hidden(info.Name()) {
			continue
		}
		entries = append(entries, entryOf(info))
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
	writeJSON(w, http.StatusOK, Listing{Path: listed, Entries: entries})
}

func (f *Files) download(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if hidden(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	f.serveDownload(w, r, name)
}

// serveDownload answers with the file name to save.
func (f *Files) serveDownload(w http.ResponseWriter, r *http.Request, name string) {
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

// hidden is true for the server's own folders at the top of the data
// directory, such as unfinished uploads and thumbnails: never listed or served.
func hidden(name string) bool {
	top, _, _ := strings.Cut(name, "/")
	return strings.HasPrefix(top, ".data-den-")
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
