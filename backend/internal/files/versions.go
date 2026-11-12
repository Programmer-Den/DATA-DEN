package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
)

// File versions: an upload with "replace": true overwrites a file, and the
// old content is kept as a version that can be restored.
//
//	GET  /api/versions?path=/a.txt                      -> {"days": n, "versions": [...]}, newest first
//	GET  /api/versions/download?path=/a.txt&id=...      -> the version's content
//	POST /api/versions/restore {"path": "...", "id": "..."} -> 200, the version is the file again
//	POST /api/versions/delete  {"path": "...", "id": "..."} -> 200, the version is gone
//
// Restoring keeps the content it replaces as a version too, so nothing is
// lost by trying an older one. Each file keeps its MaxVersions newest
// versions, and a version is deleted VersionDays after it was replaced.
// The versions of a file live in versionsDir/<key>, key being a hash of its
// path: one file <id> and its info <id>.json per version. They follow the
// file when it is moved or renamed, and count towards the quota.
const (
	versionsDir = ".data-den-versions"
	// MaxVersions is how many older versions a file keeps.
	MaxVersions = 10
	// VersionDays is how long an older version is kept.
	VersionDays = 30
)

// Version is an older content of a file.
type Version struct {
	ID string `json:"id"`
	// Path is the file's path, such as "/Texte/brief.txt".
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Modified is when this content was written, Saved when it was replaced.
	Modified time.Time `json:"modified"`
	Saved    time.Time `json:"saved"`
}

func (f *Files) registerVersions(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	f.purgeVersions()
	mux.HandleFunc("GET /api/versions", require(f.listVersions))
	mux.HandleFunc("GET /api/versions/download", require(f.downloadVersion))
	mux.HandleFunc("POST /api/versions/restore", require(f.restoreVersion))
	mux.HandleFunc("POST /api/versions/delete", require(f.deleteVersion))
}

// versionFolder is where the versions of name, a file within the root, are.
func versionFolder(name string) string {
	sum := sha256.Sum256([]byte(name))
	return path.Join(versionsDir, hex.EncodeToString(sum[:16]))
}

// versionsOf reads the versions of name, newest first. Files without a
// readable info are left out.
func (f *Files) versionsOf(name string) []Version {
	return f.versionsIn(versionFolder(name), func(v Version) bool { return v.Path == "/"+name })
}

// versionsIn reads the versions in the folder dir that keep accepts, newest
// first.
func (f *Files) versionsIn(dir string, keep func(Version) bool) []Version {
	versions := []Version{}
	entries, err := fs.ReadDir(f.root.FS(), dir)
	if err != nil {
		return versions
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !trashID.MatchString(id) {
			continue
		}
		data, err := f.root.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var v Version
		if json.Unmarshal(data, &v) != nil || v.ID != id || !keep(v) {
			continue
		}
		versions = append(versions, v)
	}
	slices.SortFunc(versions, func(a, b Version) int {
		if c := b.Saved.Compare(a.Saved); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return versions
}

// keepVersion makes the file name, as it is now, a version of itself. The
// file stays where it is: the version is a hard link, ready for name to be
// replaced. The caller holds f.versionMu.
func (f *Files) keepVersion(name string, info fs.FileInfo) (Version, error) {
	id, err := newID()
	if err != nil {
		return Version{}, err
	}
	v := Version{ID: id, Path: "/" + name, Size: info.Size(), Modified: info.ModTime().UTC(), Saved: time.Now().UTC()}
	data, err := json.Marshal(v)
	if err != nil {
		return Version{}, err
	}
	dir := versionFolder(name)
	if err := f.root.MkdirAll(dir, 0o750); err != nil {
		return Version{}, err
	}
	content := path.Join(dir, id)
	if err := f.root.Link(name, content); err != nil {
		return Version{}, err
	}
	if err := f.root.WriteFile(content+".json", data, 0o640); err != nil {
		_ = f.root.Remove(content)
		return Version{}, err
	}
	// The checksum belongs to this content, which is now the version's.
	f.sumMoved(name, content)
	return v, nil
}

// pruneVersions deletes the versions of name beyond MaxVersions. The caller
// holds f.versionMu.
func (f *Files) pruneVersions(name string) {
	versions := f.versionsOf(name)
	if len(versions) <= MaxVersions {
		return
	}
	for _, v := range versions[MaxVersions:] {
		f.dropVersion(versionFolder(name), v)
	}
}

// dropVersion deletes the version v in the folder dir for good.
func (f *Files) dropVersion(dir string, v Version) {
	content := path.Join(dir, v.ID)
	if err := f.root.Remove(content); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("deleting a version", "path", v.Path, "err", err)
		return
	}
	_ = f.root.Remove(content + ".json")
	_ = f.root.Remove(dir)
	f.sumsDropped(content)
	f.forgetUsage()
}

// purgeVersions deletes the versions replaced more than VersionDays ago.
func (f *Files) purgeVersions() {
	f.versionMu.Lock()
	defer f.versionMu.Unlock()
	limit := time.Now().AddDate(0, 0, -VersionDays)
	folders, err := fs.ReadDir(f.root.FS(), versionsDir)
	if err != nil {
		return
	}
	for _, folder := range folders {
		dir := path.Join(versionsDir, folder.Name())
		old := f.versionsIn(dir, func(v Version) bool { return v.Saved.Before(limit) })
		for _, v := range old {
			f.dropVersion(dir, v)
		}
		if len(old) > 0 {
			slog.Info("older versions deleted after the waiting time", "count", len(old))
		}
	}
}

// versionsMoved follows a rename of a file or folder from one path to
// another: the versions of the files in it move along.
func (f *Files) versionsMoved(from, to string) {
	f.versionMu.Lock()
	defer f.versionMu.Unlock()
	folders, err := fs.ReadDir(f.root.FS(), versionsDir)
	if err != nil {
		return
	}
	for _, folder := range folders {
		dir := path.Join(versionsDir, folder.Name())
		moved := f.versionsIn(dir, func(v Version) bool {
			return v.Path == "/"+from || strings.HasPrefix(v.Path, "/"+from+"/")
		})
		for _, v := range moved {
			name := to + strings.TrimPrefix(v.Path, "/"+from)
			if err := f.moveVersion(dir, v, name); err != nil {
				slog.Warn("moving a version", "from", v.Path, "to", "/"+name, "err", err)
			}
		}
		_ = f.root.Remove(dir)
	}
}

// moveVersion moves the version v from the folder dir to the versions of
// name.
func (f *Files) moveVersion(dir string, v Version, name string) error {
	to := versionFolder(name)
	if err := f.root.MkdirAll(to, 0o750); err != nil {
		return err
	}
	v.Path = "/" + name
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := f.root.WriteFile(path.Join(to, v.ID+".json"), data, 0o640); err != nil {
		return err
	}
	if err := f.root.Rename(path.Join(dir, v.ID), path.Join(to, v.ID)); err != nil {
		_ = f.root.Remove(path.Join(to, v.ID+".json"))
		return err
	}
	_ = f.root.Remove(path.Join(dir, v.ID+".json"))
	f.sumMoved(path.Join(dir, v.ID), path.Join(to, v.ID))
	return nil
}

// replaceFile returns a link function that puts the finished upload chunks,
// whose SHA-256 is sum (hex), at name, keeping the file there as a version.
// A file that is gone meanwhile is simply created, and a file with the same
// content keeps no version: it would only take the space twice.
func (f *Files) replaceFile(sum string) func(chunks, name string) error {
	return func(chunks, name string) error {
		f.versionMu.Lock()
		defer f.versionMu.Unlock()
		info, err := f.root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return f.root.Link(chunks, name)
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fs.ErrExist
		}
		same := f.sameContent(name, info, sum)
		if !same {
			if _, err := f.keepVersion(name, info); err != nil {
				return err
			}
		}
		// A rename replaces the file in one step, so nobody sees half of it.
		if err := f.root.Rename(chunks, name); err != nil {
			return err
		}
		if same {
			// The caller counted the upload on top of the old file.
			f.forgetUsage()
			slog.Info("same content uploaded again, no version kept", "path", "/"+name)
			return nil
		}
		f.pruneVersions(name)
		return nil
	}
}

// sameContent reports whether the file name, as info describes it, has the
// checksum sum. Only a checksum taken of this very file counts (same size
// and time, not found damaged); without one it is not the same, so a
// version is kept rather than lost.
func (f *Files) sameContent(name string, info fs.FileInfo, sum string) bool {
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	s := f.sums.byPath[name]
	return s != nil && sum != "" && s.SHA256 == sum && s.Bad == "" &&
		s.Size == info.Size() && s.Mod == info.ModTime().UnixNano()
}

func (f *Files) listVersions(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if name == "." || hidden(name) {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": VersionDays, "versions": f.versionsOf(name)})
}

// version finds the version that path and id name. It answers the request
// itself and returns false when that fails.
func (f *Files) version(w http.ResponseWriter, p, id string) (string, Version, bool) {
	name, ok := uploadName(p)
	if !ok || !trashID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return "", Version{}, false
	}
	for _, v := range f.versionsOf(name) {
		if v.ID == id {
			return name, v, true
		}
	}
	writeError(w, http.StatusNotFound, "Version nicht gefunden")
	return "", Version{}, false
}

func (f *Files) downloadVersion(w http.ResponseWriter, r *http.Request) {
	name, v, ok := f.version(w, r.URL.Query().Get("path"), r.URL.Query().Get("id"))
	if !ok {
		return
	}
	file, err := f.root.Open(path.Join(versionFolder(name), v.ID))
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = file.Close() }()
	base := path.Base(name)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": base}))
	http.ServeContent(w, r, base, v.Modified, file)
}

// versionBody reads the path and id of a version from a JSON body.
func (f *Files) versionBody(w http.ResponseWriter, r *http.Request) (string, Version, bool) {
	var body struct {
		Path string `json:"path"`
		ID   string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return "", Version{}, false
	}
	return f.version(w, body.Path, body.ID)
}

func (f *Files) restoreVersion(w http.ResponseWriter, r *http.Request) {
	f.versionMu.Lock()
	defer f.versionMu.Unlock()
	name, v, ok := f.versionBody(w, r)
	if !ok {
		return
	}
	info, err := f.root.Lstat(name)
	switch {
	case err == nil && !info.Mode().IsRegular():
		writeError(w, http.StatusConflict, "Dort ist jetzt ein Ordner")
		return
	case err == nil:
		if _, err := f.keepVersion(name, info); err != nil {
			f.diskError(w, "keeping a version", err)
			return
		}
	case errors.Is(err, fs.ErrNotExist):
		// The file was deleted or moved; the version brings it back.
		if dir := path.Dir(name); dir != "." {
			if err := f.root.MkdirAll(dir, 0o750); err != nil {
				f.diskError(w, "restoring a version", err)
				return
			}
		}
	default:
		f.diskError(w, "restoring a version", err)
		return
	}
	content := path.Join(versionFolder(name), v.ID)
	if err := f.root.Rename(content, name); err != nil {
		f.diskError(w, "restoring a version", err)
		return
	}
	_ = f.root.Remove(content + ".json")
	f.sumMoved(content, name)
	f.pruneVersions(name)
	slog.Info("version restored", "path", v.Path, "saved", v.Saved)
	f.note(r, "version-restore", v.Path)
	writeJSON(w, http.StatusOK, map[string]string{"path": v.Path})
}

func (f *Files) deleteVersion(w http.ResponseWriter, r *http.Request) {
	f.versionMu.Lock()
	defer f.versionMu.Unlock()
	name, v, ok := f.versionBody(w, r)
	if !ok {
		return
	}
	f.dropVersion(versionFolder(name), v)
	slog.Info("version deleted", "path", v.Path, "saved", v.Saved)
	f.note(r, "version-delete", v.Path)
	writeJSON(w, http.StatusOK, map[string]string{"path": v.Path})
}
