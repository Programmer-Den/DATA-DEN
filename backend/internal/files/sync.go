package files

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
)

// The desktop sync client (cmd/data-den-sync) keeps a folder on a PC and a
// folder here the same, both ways:
//
//	GET /api/sync/tree?path=/Dokumente -> {"path": "/Dokumente", "entries": [...]}, everything below it
//
// An upload with "expect" {"size": n, "mod": ns} replaces the file at its
// path, and a delete with "size" and "mod" deletes it, only while the file
// is still as the client saw it; otherwise the answer is 412, so a change
// made meanwhile on another device is never overwritten. The replaced
// content is kept as a version, a deleted file goes into the recycle bin.

// SyncEntry is one file or folder in the tree.
type SyncEntry struct {
	// Path is relative to the tree's folder, such as "2026/a.jpg".
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
	// Mod is the modification time in nanoseconds since 1970, as the
	// conditions of an upload or delete expect it.
	Mod int64 `json:"mod"`
	// SHA256 is the checksum, when the server has one for this content.
	SHA256 string `json:"sha256,omitempty"`
}

// fileState is what a file must still be for a replace or delete.
type fileState struct {
	Size int64 `json:"size"`
	Mod  int64 `json:"mod"`
}

// errChangedMeanwhile is the answer to a condition that no longer holds.
var errChangedMeanwhile = errors.New("changed meanwhile")

func (f *Files) registerSync(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/sync/tree", require(f.syncTree))
}

func (f *Files) syncTree(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if hidden(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	if info, err := f.root.Stat(name); err != nil {
		openError(w, err)
		return
	} else if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "Kein Ordner")
		return
	}
	f.sums.mu.Lock()
	known := make(map[string]fileSum, len(f.sums.byPath))
	for p, s := range f.sums.byPath {
		known[p] = *s
	}
	f.sums.mu.Unlock()

	entries := []SyncEntry{}
	err := fs.WalkDir(f.root.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == name {
			return nil
		}
		if name == "." && hidden(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsDir() && !d.Type().IsRegular() {
			// Symlinks, devices and the like are not offered.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			// Deleted while walking.
			return nil
		}
		rel := p
		if name != "." {
			rel = p[len(name)+1:]
		}
		entry := SyncEntry{Path: rel, Dir: d.IsDir(), Mod: info.ModTime().UnixNano()}
		if !entry.Dir {
			entry.Size = info.Size()
			if s, ok := known[p]; ok && s.Size == entry.Size && s.Mod == entry.Mod && s.Bad == "" {
				entry.SHA256 = s.SHA256
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		slog.Error("listing a folder for the sync", "path", name, "err", err)
		writeError(w, http.StatusInternalServerError, "Ordner nicht lesbar")
		return
	}
	slices.SortFunc(entries, func(a, b SyncEntry) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		}
		return 0
	})
	listed := "/"
	if name != "." {
		listed += name
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": listed, "entries": entries})
}

// still checks that name is a file as want describes it.
func (f *Files) still(name string, want fileState) error {
	info, err := f.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return errChangedMeanwhile
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != want.Size || info.ModTime().UnixNano() != want.Mod {
		return errChangedMeanwhile
	}
	return nil
}

// stateError answers a request whose condition failed or could not be
// checked.
func (f *Files) stateError(w http.ResponseWriter, err error) {
	if errors.Is(err, errChangedMeanwhile) {
		writeError(w, http.StatusPreconditionFailed, "Die Datei wurde inzwischen geändert")
		return
	}
	f.diskError(w, "checking a file", err)
}

// replaceIf replaces a file with a finished upload like replaceFile, if it
// is still as want describes it.
func (f *Files) replaceIf(want fileState) func(chunks, name string) error {
	return func(chunks, name string) error {
		if err := f.still(name, want); err != nil {
			return err
		}
		return f.replaceFile(chunks, name)
	}
}
