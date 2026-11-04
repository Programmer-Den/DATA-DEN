package files

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Shared folders: an account gives a folder of its own space to another
// account, to read or also to change. Behind the login, with a JSON body:
//
//	GET  /api/accounts                                  -> {"names": [...]}, the other accounts
//	GET  /api/grants                                    -> {"mine": [...], "withMe": [...]}
//	POST /api/grants {"path": "/Projekt", "to": "anna", "write": true} -> 201, the new grant
//	POST /api/grants/delete {"id": "..."}               -> 200; the owner revokes, the other account leaves
//
// The other account then uses the usual API with ?grant=<id>, and every path
// is within the shared folder: GET /api/files?grant=<id>&path=/ lists it.
// Reading takes /api/files, /api/download, /api/view, /api/media and
// /api/thumb; writing also uploads, new folders, moves, renames and
// deletes (into the owner's recycle bin). Everything else stays with the
// owner: the recycle bin, versions, search, links and the checks. What is
// stored counts against the owner's quota.
//
// The grants of all accounts are grantsFile at the top of the data
// directory.
const grantsFile = ".data-den-grants.json"

// Grant is a folder an account shares with another account.
type Grant struct {
	ID string `json:"id"`
	// Owner is the account whose folder it is.
	Owner string `json:"owner"`
	// Path is the folder within the owner's space, such as "/Projekt".
	Path string `json:"path"`
	Name string `json:"name"`
	// To is the account it is shared with.
	To string `json:"to"`
	// Write is true when To may also change the folder.
	Write   bool      `json:"write"`
	Created time.Time `json:"created"`
	// Team is true for a team folder, which Owner ("@<name>") names.
	Team bool `json:"team,omitempty"`
	// Missing is true in answers when the folder is gone, for example in
	// the recycle bin.
	Missing bool `json:"missing,omitempty"`
}

// grantReadRoutes are the routes a shared folder answers with ?grant=, and
// grantWriteRoutes those it also answers when it may be changed.
var (
	grantReadRoutes = []string{
		"GET /api/files",
		"GET /api/download",
		"GET /api/view",
		"GET /api/media",
		"GET /api/thumb",
	}
	grantWriteRoutes = []string{
		"POST /api/uploads",
		"PUT /api/uploads/{id}",
		"GET /api/uploads/{id}",
		"POST /api/folders",
		"POST /api/move",
		"POST /api/delete",
	}
)

// grantBodyKeys are the keys a JSON body to a shared folder may have.
var grantBodyKeys = []string{"path", "from", "to", "size", "replace", "id"}

// GrantRoutes are the API paths behind the login for shared folders.
var GrantRoutes = []string{
	"GET /api/accounts",
	"GET /api/grants",
	"POST /api/grants",
	"POST /api/grants/delete",
}

// acting is who works in a shared folder, for the audit log.
type acting struct{ user, owner string }

type actingKey struct{}

// readGrants reads all grants. The caller holds grantsMu.
func (s *Spaces) readGrants() []Grant {
	data, err := s.owner.root.ReadFile(grantsFile)
	if err != nil {
		return []Grant{}
	}
	var list []Grant
	if json.Unmarshal(data, &list) != nil || list == nil {
		slog.Warn("the shared folders cannot be read", "file", grantsFile)
		return []Grant{}
	}
	return list
}

// writeGrants replaces all grants. The caller holds grantsMu.
func (s *Spaces) writeGrants(list []Grant) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := grantsFile + ".tmp"
	if err := s.owner.root.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return s.owner.root.Rename(tmp, grantsFile)
}

// grant finds the grant id.
func (s *Spaces) grant(id string) (Grant, bool) {
	s.grantsMu.Lock()
	defer s.grantsMu.Unlock()
	for _, g := range s.readGrants() {
		if g.ID == id {
			return g, true
		}
	}
	return Grant{}, false
}

// changeGrants applies change to the list of grants and saves it.
func (s *Spaces) changeGrants(change func([]Grant) []Grant) error {
	s.grantsMu.Lock()
	defer s.grantsMu.Unlock()
	return s.writeGrants(change(s.readGrants()))
}

// RegisterGrants adds GrantRoutes to mux behind require (the login check).
// Register must come too: it serves what is in a shared folder.
func (s *Spaces) RegisterGrants(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/accounts", require(s.listAccounts))
	mux.HandleFunc("GET /api/grants", require(s.listGrants))
	mux.HandleFunc("POST /api/grants", require(s.createGrant))
	mux.HandleFunc("POST /api/grants/delete", require(s.deleteGrant))
}

func (s *Spaces) listAccounts(w http.ResponseWriter, r *http.Request) {
	me := s.cfg.User(r)
	names := []string{}
	if s.cfg.Accounts != nil {
		for _, name := range s.cfg.Accounts() {
			if name != me {
				names = append(names, name)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}

func (s *Spaces) listGrants(w http.ResponseWriter, r *http.Request) {
	me := s.cfg.User(r)
	s.grantsMu.Lock()
	all := s.readGrants()
	s.grantsMu.Unlock()
	mine, withMe := []Grant{}, []Grant{}
	for _, g := range all {
		switch me {
		case g.Owner:
			g.Missing = !s.grantFolderThere(g)
			mine = append(mine, g)
		case g.To:
			g.Missing = !s.grantFolderThere(g)
			withMe = append(withMe, g)
		}
	}
	byName := func(a, b Grant) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }
	slices.SortFunc(mine, byName)
	slices.SortFunc(withMe, byName)
	writeJSON(w, http.StatusOK, map[string]any{"mine": mine, "withMe": withMe})
}

// grantFolderThere reports whether the folder of g is still a folder.
func (s *Spaces) grantFolderThere(g Grant) bool {
	f, err := s.space(g.Owner)
	if err != nil {
		return false
	}
	name := g.dir()
	info, err := f.root.Lstat(name)
	return err == nil && info.IsDir() && !f.symlinkIn(name)
}

// dir is the folder of g within the owner's space, such as "Projekt", or
// "." for a whole team folder.
func (g Grant) dir() string {
	if g.Path == "/" {
		return "."
	}
	return strings.TrimPrefix(g.Path, "/")
}

func (s *Spaces) createGrant(w http.ResponseWriter, r *http.Request) {
	me := s.cfg.User(r)
	var body struct {
		Path  string `json:"path"`
		To    string `json:"to"`
		Write bool   `json:"write"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if body.To == me || s.cfg.Accounts == nil || !slices.Contains(s.cfg.Accounts(), body.To) {
		writeError(w, http.StatusBadRequest, "Unbekanntes Konto")
		return
	}
	f, err := s.space(me)
	if err != nil {
		slog.Error("opening a space", "username", me, "err", err)
		writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
		return
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.IsDir() || f.symlinkIn(name) {
		writeError(w, http.StatusBadRequest, "Nur Ordner können geteilt werden")
		return
	}
	id, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Teilen fehlgeschlagen")
		return
	}
	g := Grant{ID: id, Owner: me, Path: "/" + name, Name: info.Name(), To: body.To, Write: body.Write, Created: time.Now().UTC()}
	status, isNew := http.StatusCreated, true
	err = s.changeGrants(func(list []Grant) []Grant {
		// Sharing the same folder with the same account again changes the
		// rights.
		for i, old := range list {
			if old.Owner == g.Owner && old.Path == g.Path && old.To == g.To {
				list[i].Write = g.Write
				g, status, isNew = list[i], http.StatusOK, false
				return list
			}
		}
		return append(list, g)
	})
	if err != nil {
		f.diskError(w, "sharing a folder", err)
		return
	}
	slog.Info("folder shared", "username", me, "path", g.Path, "to", g.To, "write", g.Write)
	f.note(r, "grant-create", g.Path+" → "+g.To+" ("+rights(g.Write)+")")
	if isNew && s.cfg.Shared != nil {
		s.cfg.Shared(g)
	}
	writeJSON(w, status, g)
}

func rights(write bool) string {
	if write {
		return "lesen und schreiben"
	}
	return "lesen"
}

func (s *Spaces) deleteGrant(w http.ResponseWriter, r *http.Request) {
	me := s.cfg.User(r)
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var gone Grant
	err := s.changeGrants(func(list []Grant) []Grant {
		return slices.DeleteFunc(list, func(g Grant) bool {
			if g.ID == body.ID && (g.Owner == me || g.To == me) {
				gone = g
				return true
			}
			return false
		})
	})
	if err != nil {
		s.owner.diskError(w, "revoking a shared folder", err)
		return
	}
	if gone.ID == "" {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	slog.Info("shared folder ended", "username", me, "owner", gone.Owner, "path", gone.Path, "to", gone.To)
	if f, err := s.space(me); err == nil {
		f.note(r, "grant-delete", gone.Owner+":"+gone.Path+" → "+gone.To)
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": gone.ID})
}

// grantsMoved keeps the grants of owner on from, and on what is inside it,
// after it was moved to to.
func (s *Spaces) grantsMoved(owner, from, to string) {
	err := s.changeGrants(func(list []Grant) []Grant {
		for i, g := range list {
			rest, ok := strings.CutPrefix(g.Path, "/"+from)
			if g.Owner != owner || !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
				continue
			}
			list[i].Path = "/" + to + rest
			if rest == "" {
				list[i].Name = path.Base(to)
			}
		}
		return list
	})
	if err != nil {
		slog.Warn("moving shared folders", "username", owner, "err", err)
	}
}

// retireGrants ends every grant from or to the deleted account name.
func (s *Spaces) retireGrants(name string) {
	err := s.changeGrants(func(list []Grant) []Grant {
		return slices.DeleteFunc(list, func(g Grant) bool { return g.Owner == name || g.To == name })
	})
	if err != nil {
		slog.Warn("ending the shared folders of a deleted account", "username", name, "err", err)
	}
}

// serveGrant answers a request of user to the shared folder id, from the
// owner's space, with every path within the folder.
func (s *Spaces) serveGrant(w http.ResponseWriter, r *http.Request, user, id string) {
	g, ok := s.grant(id)
	if !ok || g.To != user {
		writeError(w, http.StatusNotFound, "Geteilter Ordner nicht gefunden")
		return
	}
	switch {
	case slices.Contains(grantReadRoutes, r.Pattern):
	case slices.Contains(grantWriteRoutes, r.Pattern),
		g.Team && slices.Contains(teamWriteRoutes, r.Pattern):
		if !g.Write {
			writeError(w, http.StatusForbidden, "Nur Lesezugriff")
			return
		}
	default:
		writeError(w, http.StatusForbidden, "In geteilten Ordnern nicht möglich")
		return
	}
	f, err := s.space(g.Owner)
	if err != nil {
		slog.Error("opening a space", "username", g.Owner, "err", err)
		writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
		return
	}
	if !s.grantFolderThere(g) {
		writeError(w, http.StatusNotFound, "Nicht mehr vorhanden")
		return
	}
	if s.cfg.Quota != nil {
		f.quota.Store(s.cfg.Quota(g.Owner))
	}
	prefix := g.dir()
	scoped, ok := scopeRequest(w, r, f, prefix)
	if !ok {
		return
	}
	scoped = scoped.WithContext(context.WithValue(scoped.Context(), actingKey{}, acting{user: user, owner: g.Owner}))
	// The answer is the owner's view: paths in a JSON answer are turned into
	// ones within the folder, files and thumbnails pass as they are.
	sw := &scopedWriter{w: w}
	f.mux.ServeHTTP(sw, scoped)
	if !sw.json {
		return
	}
	var answer map[string]any
	if err := json.Unmarshal(sw.buf.Bytes(), &answer); err != nil {
		w.WriteHeader(sw.code)
		_, _ = sw.buf.WriteTo(w)
		return
	}
	if p, ok := answer["path"].(string); ok {
		answer["path"] = unscope(p, prefix)
	}
	// Where else the same content lies is the owner's to know, and so is the
	// recycle bin, which the id of a deleted item is for.
	delete(answer, "duplicates")
	if r.Pattern == "POST /api/delete" && !g.Team {
		delete(answer, "id")
	}
	w.Header().Del("Content-Length")
	writeJSON(w, sw.code, answer)
}

// scopedWriter holds back a JSON answer, so serveGrant can change it, and
// passes anything else through.
type scopedWriter struct {
	w       http.ResponseWriter
	decided bool
	json    bool
	code    int
	buf     bytes.Buffer
}

func (sw *scopedWriter) Header() http.Header { return sw.w.Header() }

func (sw *scopedWriter) WriteHeader(code int) {
	if sw.decided {
		return
	}
	sw.decided, sw.code = true, code
	sw.json = strings.HasPrefix(sw.w.Header().Get("Content-Type"), "application/json")
	if !sw.json {
		sw.w.WriteHeader(code)
	}
}

func (sw *scopedWriter) Write(b []byte) (int, error) {
	if !sw.decided {
		sw.WriteHeader(http.StatusOK)
	}
	if sw.json {
		return sw.buf.Write(b)
	}
	return sw.w.Write(b)
}

// unscope turns a path in the owner's space, such as "/Projekt/a", into one
// within the shared folder prefix ("Projekt"), such as "/a".
func unscope(p, prefix string) string {
	if prefix == "." {
		return p
	}
	rest, ok := strings.CutPrefix(p, "/"+prefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return "/"
	}
	if rest == "" {
		return "/"
	}
	return rest
}

// scopeRequest returns r with every path in it, in the query and in a JSON
// body, put within the folder prefix of f. It answers the request itself
// and returns false when a path is not acceptable: one with "..", or one
// through a symlink, which could lead out of the folder.
func scopeRequest(w http.ResponseWriter, r *http.Request, f *Files, prefix string) (*http.Request, bool) {
	r = r.Clone(r.Context())
	q := r.URL.Query()
	q.Del("grant")
	// Always set: without one, a handler would take the top of the owner's
	// space.
	name, ok := relative(w, q.Get("path"))
	if !ok {
		return nil, false
	}
	name = path.Join(prefix, name)
	if f.symlinkIn(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return nil, false
	}
	q.Set("path", "/"+name)
	r.URL.RawQuery = q.Encode()
	if r.Method != http.MethodPost {
		return r, true
	}
	// The handler checks the type again and refuses anything but JSON.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return nil, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return nil, false
	}
	// Only the keys the handlers know, spelled exactly so: the handlers
	// would also take "PATH" for "path", unscoped.
	for key := range body {
		if !slices.Contains(grantBodyKeys, key) {
			writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
			return nil, false
		}
	}
	for _, key := range []string{"path", "from", "to"} {
		raw, ok := body[key]
		if !ok {
			continue
		}
		var p string
		if json.Unmarshal(raw, &p) != nil {
			writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
			return nil, false
		}
		// Never the shared folder itself: it is the owner's to move or
		// delete.
		name, ok := uploadName(p)
		if !ok {
			writeError(w, http.StatusBadRequest, "Ungültiger Name")
			return nil, false
		}
		name = path.Join(prefix, name)
		if f.symlinkIn(name) {
			writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
			return nil, false
		}
		body[key], _ = json.Marshal("/" + name)
	}
	data, err = json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Length", strconv.Itoa(len(data)))
	return r, true
}
