package files

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Every account has its own space. The owner's (the account from
// DATA_DEN_USERNAME) is the data directory itself, as before there were
// several accounts; each other account gets usersDir/<name>, which the owner
// never sees. When an account is deleted, its folder is moved to
// usersDir/.deleted/<name>-<time>, so nothing is lost by a wrong click.
const usersDir = ".data-den-users"

// accountName is what Spaces accepts as a folder name; the auth package
// only creates accounts that match it.
var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// Routes are the API paths of a space, for registering them on a mux.
var Routes = []string{
	"GET /api/files",
	"GET /api/download",
	"GET /api/view",
	"GET /api/media",
	"GET /api/thumb",
	"GET /api/usage",
	"POST /api/uploads",
	"PUT /api/uploads/{id}",
	"GET /api/uploads/{id}",
	"POST /api/folders",
	"POST /api/move",
	"POST /api/delete",
	"GET /api/trash",
	"POST /api/trash/restore",
	"POST /api/trash/delete",
	"POST /api/trash/empty",
	"GET /api/search",
	"GET /api/duplicates",
	"GET /api/integrity",
	"POST /api/integrity/check",
	"POST /api/integrity/accept",
	"GET /api/versions",
	"GET /api/versions/download",
	"POST /api/versions/restore",
	"POST /api/versions/delete",
}

// ShareRoutes are the API paths behind the login for share links.
var ShareRoutes = []string{
	"GET /api/shares",
	"POST /api/shares",
	"POST /api/shares/delete",
}

// publicShareRoutes are the API paths of a share link, for everyone with it.
var publicShareRoutes = []string{
	"GET /api/s/{id}",
	"POST /api/s/{id}/unlock",
	"GET /api/s/{id}/files",
	"GET /api/s/{id}/download",
	"GET /api/s/{id}/zip",
}

// SpacesConfig says whose space a request is for.
type SpacesConfig struct {
	// Owner is the account whose space is the data directory itself.
	Owner string
	// User is the logged-in account of a request that passed the login check.
	User func(*http.Request) string
	// Quota is how many bytes an account may store; 0 means no limit.
	Quota func(name string) int64
	// Audit notes changes to files in the audit log; nil notes nothing.
	Audit Recorder
}

// Spaces serves each account its own space in the data directory.
type Spaces struct {
	cfg       SpacesConfig
	owner     *Files
	mu        sync.Mutex
	byName    map[string]*Files
	trashDays int
	guard     Guard
	// ctx is what Run got: spaces opened later start their background work
	// with it. Nil before Run.
	ctx context.Context
	// stop ends the background work of each space but the owner's.
	stop map[string]context.CancelFunc
}

// OpenSpaces opens the data directory dir.
func OpenSpaces(dir string, cfg SpacesConfig) (*Spaces, error) {
	owner, err := Open(dir)
	if err != nil {
		return nil, err
	}
	owner.account, owner.audit = cfg.Owner, cfg.Audit
	owner.serve()
	return &Spaces{
		cfg: cfg, owner: owner, byName: map[string]*Files{},
		trashDays: DefaultTrashDays, stop: map[string]context.CancelFunc{},
	}, nil
}

// SetTrashDays sets the recycle bin's waiting time of every space; see
// Files.SetTrashDays.
func (s *Spaces) SetTrashDays(days int) error {
	if err := s.owner.SetTrashDays(days); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trashDays = days
	for _, f := range s.byName {
		_ = f.SetTrashDays(days)
	}
	return nil
}

// TrashDays is how many days a deleted item can be restored.
func (s *Spaces) TrashDays() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trashDays
}

// Run starts the background work of every space, the search index,
// emptying the recycle bin and the checks, until ctx is done; a space opened later starts
// its own then.
func (s *Spaces) Run(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	s.owner.background(ctx)
	for name, f := range s.byName {
		s.startLocked(name, f)
	}
}

// startLocked starts the background work of the space name. The caller
// holds mu.
func (s *Spaces) startLocked(name string, f *Files) {
	if s.ctx == nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.stop[name] = cancel
	f.background(ctx)
}

// background keeps the search index fresh, empties the recycle bin on time
// and runs the checks, until ctx is done.
func (f *Files) background(ctx context.Context) {
	go f.Index(ctx.Done())
	go f.PurgeTrashEvery(ctx, time.Hour)
	f.StartChecks(ctx)
}

// Close releases the data directory and every space.
func (s *Spaces) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, f := range s.byName {
		if stop, ok := s.stop[name]; ok {
			stop()
		}
		_ = f.Close()
		delete(s.byName, name)
	}
	return s.owner.Close()
}

// Register adds Routes to mux, behind require (the login check).
func (s *Spaces) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	handler := require(func(w http.ResponseWriter, r *http.Request) {
		name := s.cfg.User(r)
		f, err := s.space(name)
		if err != nil {
			slog.Error("opening a space", "username", name, "err", err)
			writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
			return
		}
		if s.cfg.Quota != nil {
			f.quota.Store(s.cfg.Quota(name))
		}
		f.mux.ServeHTTP(w, r)
	})
	for _, route := range Routes {
		mux.HandleFunc(route, handler)
	}
}

// RegisterDAV adds WebDAV under DavPrefix to mux, behind require (a login
// check that takes app passwords), each account in its own space.
func (s *Spaces) RegisterDAV(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	handler := require(func(w http.ResponseWriter, r *http.Request) {
		name := s.cfg.User(r)
		f, err := s.space(name)
		if err != nil {
			slog.Error("opening a space", "username", name, "err", err)
			http.Error(w, "Speicherbereich nicht verfügbar", http.StatusInternalServerError)
			return
		}
		if s.cfg.Quota != nil {
			f.quota.Store(s.cfg.Quota(name))
		}
		f.ServeDAV(w, r)
	})
	mux.HandleFunc(DavPrefix, handler)
	mux.HandleFunc(DavPrefix+"/", handler)
}

// RegisterShares adds ShareRoutes to mux behind require, each account's
// links kept in its own space, and the links themselves for everyone.
// guard limits the password guesses.
func (s *Spaces) RegisterShares(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc, guard Guard) {
	s.mu.Lock()
	s.guard = guard
	s.owner.shares.guard = guard
	for _, f := range s.byName {
		f.shares.guard = guard
	}
	s.mu.Unlock()
	handler := require(func(w http.ResponseWriter, r *http.Request) {
		name := s.cfg.User(r)
		f, err := s.space(name)
		if err != nil {
			slog.Error("opening a space", "username", name, "err", err)
			writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
			return
		}
		f.mux.ServeHTTP(w, r)
	})
	for _, route := range ShareRoutes {
		mux.HandleFunc(route, handler)
	}
	for _, route := range publicShareRoutes {
		mux.HandleFunc(route, s.serveShare)
	}
}

// serveShare answers a share link from the space that holds it.
func (s *Spaces) serveShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f := s.owner
	if trashID.MatchString(id) {
		if _, err := s.owner.root.Lstat(shareFile(id)); err != nil {
			f = s.shareSpace(id)
		}
	}
	// A space without the share answers like an unknown link.
	f.mux.ServeHTTP(w, r)
}

// shareSpace is the space of another account that holds the share id, or
// the owner's when none does.
func (s *Spaces) shareSpace(id string) *Files {
	entries, err := fs.ReadDir(s.owner.root.FS(), usersDir)
	if err != nil {
		return s.owner
	}
	for _, e := range entries {
		if !e.IsDir() || !accountName.MatchString(e.Name()) {
			continue
		}
		if _, err := s.owner.root.Lstat(path.Join(usersDir, e.Name(), shareFile(id))); err == nil {
			if f, err := s.space(e.Name()); err == nil {
				return f
			}
		}
	}
	return s.owner
}

// Usage is how many bytes the account name stores, recycle bin included.
func (s *Spaces) Usage(name string) (int64, error) {
	f, err := s.space(name)
	if err != nil {
		return 0, err
	}
	return f.usage()
}

// Retire moves the space of a deleted account aside, so a new account of
// the same name starts empty.
func (s *Spaces) Retire(name string) error {
	if name == s.cfg.Owner || !accountName.MatchString(name) {
		return errors.New("not a space that can be retired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.byName[name]; ok {
		if stop, ok := s.stop[name]; ok {
			stop()
			delete(s.stop, name)
		}
		_ = f.Close()
		delete(s.byName, name)
	}
	from := path.Join(usersDir, name)
	if _, err := s.owner.root.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	deleted := path.Join(usersDir, ".deleted")
	if err := s.owner.root.MkdirAll(deleted, 0o750); err != nil {
		return err
	}
	to := path.Join(deleted, name+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	slog.Info("space of a deleted account moved aside", "username", name, "to", to)
	return s.owner.root.Rename(from, to)
}

// space returns the space of the account name, opening it on first use.
func (s *Spaces) space(name string) (*Files, error) {
	if name == s.cfg.Owner {
		return s.owner, nil
	}
	if !accountName.MatchString(name) {
		return nil, errors.New("invalid account name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.byName[name]; ok {
		return f, nil
	}
	dir := path.Join(usersDir, name)
	if err := s.owner.root.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := s.owner.root.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f := newFiles(root)
	// Shares the limit on thumbnails made at once with all other spaces.
	f.thumbSlots = s.owner.thumbSlots
	f.trashDays = s.trashDays
	f.shares.guard = s.guard
	f.account, f.audit = name, s.cfg.Audit
	f.serve()
	s.byName[name] = f
	s.startLocked(name, f)
	return f, nil
}
