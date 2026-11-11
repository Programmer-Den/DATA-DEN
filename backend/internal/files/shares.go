package files

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Share links: a file or folder for people without a login, until a set
// day and, if wanted, behind a password.
//
// Behind the login, with a JSON body:
//
//	GET  /api/shares                                     -> {"items": [...]}, newest first
//	POST /api/shares {"path": "/Fotos", "days": 7, "password": "", "upload": false} -> 201, the new share
//	POST /api/shares/delete {"id": "..."}                -> 200, the link stops working
//
// For everyone with the link /s/<id>:
//
//	GET  /api/s/{id}                       -> what is shared; 401 {"password": true} while locked
//	POST /api/s/{id}/unlock {"password"}   -> 200 and a cookie for this share
//	GET  /api/s/{id}/files?path=/2026      -> a folder within a shared folder
//	GET  /api/s/{id}/download?path=/a.jpg  -> a file; no path for a shared file
//	GET  /api/s/{id}/zip?path=/2026        -> a folder within a shared folder as one ZIP
//
// An upload link ("upload": true, only for a folder) works the other way:
// whoever has it can drop files into the folder, like an upload behind the
// login (see upload.go), but cannot see what is in it. A file whose name is
// taken gets the next free one, such as "a (1).jpg"; nothing is overwritten.
//
//	POST /api/s/{id}/uploads {"path": "/a.jpg", "size": 123} -> {"id": "...", "offset": 0}
//	PUT  /api/s/{id}/uploads/{upload}, header Upload-Offset  -> {"offset": n}, with "name" at the end
//	GET  /api/s/{id}/uploads/{upload}                        -> {"offset": n}
//
// Each share is sharesDir/<id>.json. The id is the secret part of the link.
// A link that expired or was revoked answers like one that never existed.
const (
	sharesDir = ".data-den-shares"
	// MaxShareDays is the longest a link can work.
	MaxShareDays = 365
	shareCookie  = "data_den_share"
	// unlockLifetime is how long a password, once entered, opens a share.
	unlockLifetime = 24 * time.Hour
	shareGone      = "Link ungültig oder abgelaufen"
)

// Share is a link to a file or folder.
type Share struct {
	ID string `json:"id"`
	// Path is what is shared, such as "/Fotos/2026".
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	// PasswordHash is the bcrypt hash of the password, empty for none. It
	// never leaves the server.
	PasswordHash string `json:"passwordHash,omitempty"`
	// Password is true in answers when the link asks for a password.
	Password bool `json:"password"`
	// Upload is true for an upload link: files go in, nothing comes out.
	Upload bool `json:"upload,omitempty"`
	// Missing is true in answers when the file or folder is gone, for
	// example moved to the recycle bin.
	Missing bool `json:"missing,omitempty"`
}

// answer is the share as the API shows it, without the hash.
func (s Share) answer() Share {
	s.Password = s.PasswordHash != ""
	s.PasswordHash = ""
	return s
}

// Guard limits password guesses on share links. The login's own limit does,
// so guessing share passwords also counts against logging in.
type Guard interface {
	// Wait is how long the client of r must wait before it may try again,
	// or 0.
	Wait(r *http.Request) time.Duration
	// Failed counts a wrong password from the client of r, for the share id.
	Failed(r *http.Request, id string)
}

// shares are the entered passwords: an unlock cookie opens one share for a
// while. They live in memory, so a restart asks for the password again.
type shares struct {
	// mu also orders the changes to the share files.
	mu       sync.Mutex
	unlocked map[string]unlock
	guard    Guard
}

type unlock struct {
	share   string
	expires time.Time
}

// RegisterShares adds the share links: /api/shares behind require (the login
// check), /api/s/ for everyone. guard limits the password guesses.
func (f *Files) RegisterShares(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc, guard Guard) {
	f.shares.guard = guard
	mux.HandleFunc("GET /api/shares", require(f.listShares))
	mux.HandleFunc("POST /api/shares", require(f.createShare))
	mux.HandleFunc("POST /api/shares/delete", require(f.deleteShare))
	mux.HandleFunc("GET /api/s/{id}", f.shareInfo)
	mux.HandleFunc("POST /api/s/{id}/unlock", f.unlockShare)
	mux.HandleFunc("GET /api/s/{id}/files", f.shareList)
	mux.HandleFunc("GET /api/s/{id}/download", f.shareDownload)
	mux.HandleFunc("GET /api/s/{id}/zip", f.shareZip)
	mux.HandleFunc("POST /api/s/{id}/uploads", f.shareStartUpload)
	mux.HandleFunc("PUT /api/s/{id}/uploads/{upload}", f.sharePutChunk)
	mux.HandleFunc("GET /api/s/{id}/uploads/{upload}", f.shareUploadState)
}

func shareFile(id string) string {
	return path.Join(sharesDir, id+".json")
}

// readShare reads the share id. One that expired is removed, and like an
// unknown one gives false.
func (f *Files) readShare(id string) (Share, bool) {
	if !trashID.MatchString(id) {
		return Share{}, false
	}
	data, err := f.root.ReadFile(shareFile(id))
	if err != nil {
		return Share{}, false
	}
	var s Share
	if json.Unmarshal(data, &s) != nil || s.ID != id {
		return Share{}, false
	}
	if !time.Now().Before(s.Expires) {
		_ = f.root.Remove(shareFile(id))
		slog.Info("share link expired", "path", s.Path)
		return Share{}, false
	}
	return s, true
}

// allShares reads every share that has not expired, newest first.
func (f *Files) allShares() []Share {
	list := []Share{}
	entries, err := fs.ReadDir(f.root.FS(), sharesDir)
	if err != nil {
		return list
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		if s, ok := f.readShare(id); ok {
			list = append(list, s)
		}
	}
	slices.SortFunc(list, func(a, b Share) int { return b.Created.Compare(a.Created) })
	return list
}

func (f *Files) writeShare(s Share) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := f.root.MkdirAll(sharesDir, 0o750); err != nil {
		return err
	}
	return f.root.WriteFile(shareFile(s.ID), data, 0o640)
}

func (f *Files) listShares(w http.ResponseWriter, _ *http.Request) {
	items := []Share{}
	for _, s := range f.allShares() {
		s = s.answer()
		if name, ok := uploadName(s.Path); !ok || f.symlinkIn(name) {
			s.Missing = true
		} else if info, err := f.root.Lstat(name); err != nil || info.IsDir() != s.Dir {
			s.Missing = true
		}
		items = append(items, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"maxDays": MaxShareDays, "items": items})
}

func (f *Files) createShare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path     string `json:"path"`
		Days     int    `json:"days"`
		Password string `json:"password"`
		Upload   bool   `json:"upload"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if body.Days < 1 || body.Days > MaxShareDays {
		writeError(w, http.StatusBadRequest, "Gültigkeit zwischen 1 und "+strconv.Itoa(MaxShareDays)+" Tagen")
		return
	}
	if len(body.Password) > 72 {
		writeError(w, http.StatusBadRequest, "Das Passwort ist zu lang (höchstens 72 Bytes)")
		return
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		openError(w, err)
		return
	}
	if (!info.IsDir() && !info.Mode().IsRegular()) || f.symlinkIn(name) {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return
	}
	if body.Upload && !info.IsDir() {
		writeError(w, http.StatusBadRequest, "Hochladen geht nur in einen Ordner")
		return
	}
	id, err := newID()
	if err != nil {
		f.diskError(w, "creating a share", err)
		return
	}
	now := time.Now().UTC()
	s := Share{
		ID:      id,
		Path:    "/" + name,
		Name:    info.Name(),
		Dir:     info.IsDir(),
		Created: now,
		Expires: now.AddDate(0, 0, body.Days),
		Upload:  body.Upload,
	}
	if body.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
		if err != nil {
			f.diskError(w, "hashing a share password", err)
			return
		}
		s.PasswordHash = string(hash)
	}
	f.shares.mu.Lock()
	err = f.writeShare(s)
	f.shares.mu.Unlock()
	if err != nil {
		f.diskError(w, "creating a share", err)
		return
	}
	slog.Info("share link created", "path", s.Path, "expires", s.Expires, "password", s.PasswordHash != "", "upload", s.Upload)
	f.note(r, "share-create", s.Path)
	writeJSON(w, http.StatusCreated, s.answer())
}

func (f *Files) deleteShare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s, ok := f.readShare(body.ID)
	if !ok {
		writeError(w, http.StatusNotFound, shareGone)
		return
	}
	f.shares.mu.Lock()
	err := f.root.Remove(shareFile(s.ID))
	f.shares.mu.Unlock()
	if err != nil {
		f.diskError(w, "revoking a share", err)
		return
	}
	slog.Info("share link revoked", "path", s.Path)
	f.note(r, "share-delete", s.Path)
	writeJSON(w, http.StatusOK, map[string]string{"id": s.ID})
}

// moveShares keeps the links to from, and to what is inside it, working
// after it was moved to to.
func (f *Files) moveShares(from, to string) {
	f.shares.mu.Lock()
	defer f.shares.mu.Unlock()
	for _, s := range f.allShares() {
		rest, ok := strings.CutPrefix(s.Path, "/"+from)
		if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
			continue
		}
		s.Path = "/" + to + rest
		if rest == "" {
			s.Name = path.Base(to)
		}
		if err := f.writeShare(s); err != nil {
			slog.Warn("moving a share link", "path", s.Path, "err", err)
		}
	}
}

// symlinkIn is true when name or a folder on the way to it is a symlink.
// os.Root keeps symlinks within the data directory, but a link inside a
// shared folder could still lead out of what is shared.
func (f *Files) symlinkIn(name string) bool {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := f.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// shared reads the share of a request to /api/s/{id} and checks that it is
// unlocked. It answers the request itself and returns false when not.
func (f *Files) shared(w http.ResponseWriter, r *http.Request) (Share, bool) {
	s, ok := f.readShare(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, shareGone)
		return Share{}, false
	}
	if s.PasswordHash != "" && !f.isUnlocked(r, s.ID) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Passwort nötig", "password": true})
		return Share{}, false
	}
	return s, true
}

// sharedName turns the path of a request within a share, such as "/2026"
// in a shared "/Fotos", into a name within the root, such as "Fotos/2026".
// It also returns the path within the share, such as "2026" ("." for the
// shared folder itself). It answers the request itself and returns false
// when the path is not acceptable, and always for an upload link.
func (f *Files) sharedName(w http.ResponseWriter, s Share, p string) (name, rel string, ok bool) {
	if s.Upload {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return "", "", false
	}
	rel, ok = relative(w, p)
	if !ok {
		return "", "", false
	}
	if !s.Dir && rel != "." {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return "", "", false
	}
	name = strings.TrimPrefix(path.Join(s.Path, rel), "/")
	if f.symlinkIn(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return "", "", false
	}
	return name, rel, true
}

func (f *Files) isUnlocked(r *http.Request, id string) bool {
	c, err := r.Cookie(shareCookie)
	if err != nil {
		return false
	}
	f.shares.mu.Lock()
	defer f.shares.mu.Unlock()
	u, ok := f.shares.unlocked[c.Value]
	if !ok || subtle.ConstantTimeCompare([]byte(u.share), []byte(id)) != 1 {
		return false
	}
	if !time.Now().Before(u.expires) {
		delete(f.shares.unlocked, c.Value)
		return false
	}
	return true
}

func (f *Files) shareInfo(w http.ResponseWriter, r *http.Request) {
	s, ok := f.shared(w, r)
	if !ok {
		return
	}
	f.writeShareInfo(w, s)
}

// writeShareInfo answers with what a link shares, without where it lies.
func (f *Files) writeShareInfo(w http.ResponseWriter, s Share) {
	name := strings.TrimPrefix(s.Path, "/")
	info, err := f.root.Lstat(name)
	if err != nil || info.IsDir() != s.Dir || f.symlinkIn(name) {
		writeError(w, http.StatusNotFound, "Nicht mehr vorhanden")
		return
	}
	answer := map[string]any{"name": s.Name, "dir": s.Dir, "expires": s.Expires, "upload": s.Upload}
	if !s.Dir {
		answer["size"] = info.Size()
	}
	writeJSON(w, http.StatusOK, answer)
}

func (f *Files) unlockShare(w http.ResponseWriter, r *http.Request) {
	s, ok := f.readShare(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, shareGone)
		return
	}
	if wait := f.shares.guard.Wait(r); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte später erneut versuchen")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.PasswordHash == "" {
		f.writeShareInfo(w, s)
		return
	}
	if len(body.Password) > 72 || bcrypt.CompareHashAndPassword([]byte(s.PasswordHash), []byte(body.Password)) != nil {
		f.shares.guard.Failed(r, s.ID)
		writeError(w, http.StatusUnauthorized, "Passwort falsch")
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeError(w, http.StatusInternalServerError, "Entsperren fehlgeschlagen")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expires := time.Now().Add(unlockLifetime)
	if s.Expires.Before(expires) {
		expires = s.Expires
	}
	f.shares.mu.Lock()
	now := time.Now()
	for t, u := range f.shares.unlocked {
		if !now.Before(u.expires) {
			delete(f.shares.unlocked, t)
		}
	}
	f.shares.unlocked[token] = unlock{share: s.ID, expires: expires}
	f.shares.mu.Unlock()
	// Only sent to this share's API, over HTTPS, never to scripts and never
	// with requests from other sites.
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookie,
		Value:    token,
		Path:     "/api/s/" + s.ID,
		Expires:  expires,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	f.writeShareInfo(w, s)
}

func (f *Files) shareList(w http.ResponseWriter, r *http.Request) {
	s, ok := f.shared(w, r)
	if !ok {
		return
	}
	if !s.Dir {
		writeError(w, http.StatusBadRequest, "Kein Ordner")
		return
	}
	name, rel, ok := f.sharedName(w, s, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	listed := "/"
	if rel != "." {
		listed += rel
	}
	f.writeListing(w, name, listed, false)
}

func (f *Files) shareDownload(w http.ResponseWriter, r *http.Request) {
	s, ok := f.shared(w, r)
	if !ok {
		return
	}
	name, _, ok := f.sharedName(w, s, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	// A video is fetched in many ranges; only the start counts.
	if firstPiece(r) {
		f.note(r, "share-download", "/"+name)
	}
	f.serveDownload(w, r, name)
}

// shareZip sends a folder within a shared folder as one ZIP, made while it
// is sent. Photos and videos are compressed already, so the files are only
// stored, which keeps it fast.
func (f *Files) shareZip(w http.ResponseWriter, r *http.Request) {
	s, ok := f.shared(w, r)
	if !ok {
		return
	}
	if !s.Dir {
		writeError(w, http.StatusBadRequest, "Kein Ordner")
		return
	}
	name, _, ok := f.sharedName(w, s, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	f.note(r, "share-download", "/"+name+" (ZIP)")
	info, err := f.root.Stat(name)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "Kein Ordner")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name() + ".zip"}))
	archive := zip.NewWriter(w)
	err = fs.WalkDir(f.root.FS(), name, func(p string, d fs.DirEntry, err error) error {
		// Symlinks and the like are left out; WalkDir does not follow them.
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		return f.addToZip(archive, p, strings.TrimPrefix(p, name+"/"))
	})
	if err == nil {
		err = archive.Close()
	}
	if err != nil {
		// The answer has begun; the browser sees a broken download.
		slog.Warn("sending a ZIP of a share", "path", "/"+name, "err", err)
	}
}

func (f *Files) addToZip(archive *zip.Writer, name, inZip string) error {
	file, err := f.root.Open(name)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = inZip
	header.Method = zip.Store
	out, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, file)
	return err
}

// uploadShare reads the upload link of a request to /api/s/{id}/uploads and
// checks that it is unlocked and its folder still there. It answers the
// request itself and returns false when not.
func (f *Files) uploadShare(w http.ResponseWriter, r *http.Request) (Share, bool) {
	s, ok := f.shared(w, r)
	if !ok {
		return Share{}, false
	}
	if !s.Upload {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return Share{}, false
	}
	name := strings.TrimPrefix(s.Path, "/")
	if info, err := f.root.Lstat(name); err != nil || !info.IsDir() || f.symlinkIn(name) {
		writeError(w, http.StatusNotFound, "Nicht mehr vorhanden")
		return Share{}, false
	}
	return s, true
}

func (f *Files) shareStartUpload(w http.ResponseWriter, r *http.Request) {
	s, ok := f.uploadShare(w, r)
	if !ok {
		return
	}
	body, ok := readUploadStart(w, r)
	if !ok {
		return
	}
	// Only a file name: the folders inside stay unknown and untouched, and
	// nothing is replaced.
	file, ok := uploadName(body.Path)
	if !ok || strings.Contains(file, "/") {
		writeError(w, http.StatusBadRequest, "Ungültiger Dateiname")
		return
	}
	name, ok := f.freeName(path.Join(strings.TrimPrefix(s.Path, "/"), file))
	if !ok {
		writeError(w, http.StatusConflict, "Datei existiert bereits")
		return
	}
	f.beginUpload(w, r, &upload{path: name, size: body.Size, hash: sha256.New(), share: s.ID})
}

func (f *Files) sharePutChunk(w http.ResponseWriter, r *http.Request) {
	s, ok := f.uploadShare(w, r)
	if !ok {
		return
	}
	f.receiveChunk(w, r, r.PathValue("upload"), s.ID)
}

func (f *Files) shareUploadState(w http.ResponseWriter, r *http.Request) {
	s, ok := f.uploadShare(w, r)
	if !ok {
		return
	}
	f.writeUploadState(w, r.PathValue("upload"), s.ID)
}
