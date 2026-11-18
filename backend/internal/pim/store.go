// Package pim serves each account's calendars over CalDAV and its contacts
// over CardDAV, so phones and desktop programs (DAVx5, iOS, macOS,
// Thunderbird and the like) keep them in sync with DATA-DEN.
package pim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-webdav"
)

// Each account keeps its calendars and address books in its own space, in
// dir: dir/calendars/<calendar>/<event>.ics and
// dir/contacts/<address book>/<contact>.vcf, each collection with its name
// in collectionFile. Like every .data-den- folder, dir never shows among
// the files, and it moves along when the account is deleted.
const (
	dir            = ".data-den-pim"
	collectionFile = ".collection.json"
	// deletedDir keeps deleted calendars and address books, so a wrong
	// click on a phone loses nothing.
	deletedDir = dir + "/.deleted"
	// keepDeleted is how long a deleted calendar or address book is kept.
	keepDeleted = 30 * 24 * time.Hour
	// deletedTime ends the name of a deleted collection.
	deletedTime = "20060102-150405.000"
	// defaultCollection is the calendar and the address book every account
	// starts with.
	defaultCollection = "default"
	// maxObject is the largest event or contact taken, in bytes.
	maxObject = 10 << 20
)

// kind is calendars or contacts.
type kind struct {
	// dir is the folder in dir and the URL segment of the home set.
	dir string
	// prefix is where its protocol is served.
	prefix string
	// defaultName names the collection every account starts with.
	defaultName string
}

var (
	calendars = kind{dir: "calendars", prefix: CalPrefix, defaultName: "Kalender"}
	contacts  = kind{dir: "contacts", prefix: CardPrefix, defaultName: "Kontakte"}
)

var (
	errNotFound  = errors.New("not found")
	errExists    = errors.New("already exists")
	errNoParent  = errors.New("the collection does not exist")
	errForbidden = errors.New("not allowed here")
	errQuota     = errors.New("the quota is full")
)

// collection is what collectionFile holds.
type collection struct {
	ID          string `json:"-"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// object is an event or a contact as stored.
type object struct {
	name string
	data []byte
	mod  time.Time
}

// etag is the entity tag of data.
func etag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// segment is true for what a collection or an object may be called: no
// slashes and nothing hidden.
func segment(s string) bool {
	return s != "" && len(s) <= 255 && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "/\\\x00")
}

// account is the logged-in account of a request and the root of its space.
type account struct {
	name string
	root *os.Root
	// fits reports whether n more bytes fit into the account's quota, and
	// added counts them once stored; both may be nil.
	fits  func(n int64) (bool, error)
	added func(n int64)
}

// fit refuses n more bytes when they do not fit into a's quota.
func (a account) fit(n int64) error {
	if n <= 0 || a.fits == nil {
		return nil
	}
	if ok, err := a.fits(n); err != nil {
		return err
	} else if !ok {
		return statusError{http.StatusInsufficientStorage, errQuota}
	}
	return nil
}

// home is the URL of k's home set of a.
func (a account) home(k kind) string {
	return k.prefix + "/" + a.name + "/" + k.dir + "/"
}

// parse splits the URL path p of k into a collection and an object, both
// empty where p stops before them. Paths of other accounts are not found.
func (a account) parse(k kind, p string) (coll, obj string, err error) {
	rest, ok := strings.CutPrefix(path.Clean("/"+p)+"/", a.home(k))
	if !ok {
		return "", "", errNotFound
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", "", nil
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 {
		return "", "", errNotFound
	}
	for _, part := range parts {
		if !segment(part) {
			return "", "", errNotFound
		}
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return parts[0], "", nil
}

// store keeps the calendars and address books; mu orders all changes.
type store struct {
	mu sync.Mutex
}

// ensure creates the default collection of k the first time a uses it.
func (s *store) ensure(a account, k kind) error {
	if _, err := a.root.Stat(path.Join(dir, k.dir)); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The default collection is a few bytes and comes with every request,
	// so it is made even when the quota is full: else a full account could
	// not even list its calendars.
	a.fits = nil
	err := s.create(a, k, collection{ID: defaultCollection, Name: k.defaultName})
	if errors.Is(err, errExists) {
		return nil
	}
	return err
}

// collections lists a's collections of k.
func (s *store) collections(a account, k kind) ([]collection, error) {
	entries, err := fs.ReadDir(a.root.FS(), path.Join(dir, k.dir))
	if err != nil {
		return nil, err
	}
	var list []collection
	for _, e := range entries {
		if !e.IsDir() || !segment(e.Name()) {
			continue
		}
		c, err := s.collection(a, k, e.Name())
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, nil
}

// collection reads the collection id of k.
func (s *store) collection(a account, k kind, id string) (collection, error) {
	folder := path.Join(dir, k.dir, id)
	if info, err := a.root.Stat(folder); errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return collection{}, errNotFound
	} else if err != nil {
		return collection{}, err
	}
	c := collection{ID: id, Name: id}
	data, err := a.root.ReadFile(path.Join(folder, collectionFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return collection{}, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &c); err != nil {
			slog.Warn("reading a collection", "account", a.name, "collection", folder, "err", err)
		}
	}
	return c, nil
}

// create adds the collection c of k.
func (s *store) create(a account, k kind, c collection) error {
	if !segment(c.ID) {
		return errForbidden
	}
	if c.Name == "" {
		c.Name = c.ID
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := a.fit(int64(len(data))); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := path.Join(dir, k.dir)
	if err := a.root.MkdirAll(base, 0o750); err != nil {
		return err
	}
	folder := path.Join(base, c.ID)
	if err := a.root.Mkdir(folder, 0o750); errors.Is(err, fs.ErrExist) {
		return errExists
	} else if err != nil {
		return err
	}
	if err := writeFile(a.root, path.Join(folder, collectionFile), data); err != nil {
		return err
	}
	if a.added != nil {
		a.added(int64(len(data)))
	}
	return nil
}

// remove moves the collection id of k to deletedDir.
func (s *store) remove(a account, k kind, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.collection(a, k, id); err != nil {
		return err
	}
	if err := a.root.MkdirAll(deletedDir, 0o750); err != nil {
		return err
	}
	s.purgeDeleted(a, time.Now().Add(-keepDeleted))
	to := path.Join(deletedDir, fmt.Sprintf("%s-%s-%s", k.dir, id, time.Now().UTC().Format(deletedTime)))
	return a.root.Rename(path.Join(dir, k.dir, id), to)
}

// purgeDeleted removes the collections deleted before cutoff for good, so
// they stop counting against the quota. The caller holds mu.
func (s *store) purgeDeleted(a account, cutoff time.Time) {
	entries, err := fs.ReadDir(a.root.FS(), deletedDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		// The name ends with the time of the deletion; the folder's own
		// time is that of its last change.
		name := e.Name()
		if len(name) < len(deletedTime) {
			continue
		}
		at, err := time.Parse(deletedTime, name[len(name)-len(deletedTime):])
		if err != nil || !at.Before(cutoff) {
			continue
		}
		var size int64
		if a.added != nil {
			size = dirSize(a.root, path.Join(deletedDir, e.Name()))
		}
		if err := a.root.RemoveAll(path.Join(deletedDir, e.Name())); err != nil {
			slog.Warn("removing a deleted collection", "account", a.name, "name", e.Name(), "err", err)
			if a.added != nil {
				// Some of it may be gone.
				size -= dirSize(a.root, path.Join(deletedDir, e.Name()))
			}
		}
		if a.added != nil {
			a.added(-size)
		}
	}
}

// dirSize is how many bytes the files in the folder name hold.
func dirSize(root *os.Root, name string) int64 {
	var total int64
	_ = fs.WalkDir(root.FS(), name, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// objects lists the objects in the collection id of k.
func (s *store) objects(a account, k kind, id string) ([]object, error) {
	if _, err := s.collection(a, k, id); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(a.root.FS(), path.Join(dir, k.dir, id))
	if err != nil {
		return nil, err
	}
	var list []object
	for _, e := range entries {
		if !e.Type().IsRegular() || !segment(e.Name()) {
			continue
		}
		o, err := s.object(a, k, id, e.Name())
		if errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	return list, nil
}

// object reads the object name in the collection id of k.
func (s *store) object(a account, k kind, id, name string) (object, error) {
	file := path.Join(dir, k.dir, id, name)
	info, err := a.root.Stat(file)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return object{}, errNotFound
	} else if err != nil {
		return object{}, err
	}
	data, err := a.root.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return object{}, errNotFound
	} else if err != nil {
		return object{}, err
	}
	return object{name: name, data: data, mod: info.ModTime()}, nil
}

// put stores data as the object name in the collection id of k, if the
// conditions of the request hold.
func (s *store) put(a account, k kind, id, name string, data []byte, ifMatch, ifNoneMatch webdav.ConditionalMatch) (object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.collection(a, k, id); errors.Is(err, errNotFound) {
		return object{}, errNoParent
	} else if err != nil {
		return object{}, err
	}
	old, err := s.object(a, k, id, name)
	if err != nil && !errors.Is(err, errNotFound) {
		return object{}, err
	}
	current := ""
	if err == nil {
		current = etag(old.data)
	}
	if err := conditions(current, ifMatch, ifNoneMatch); err != nil {
		return object{}, err
	}
	// Calendars and contacts count against the quota like files.
	more := int64(len(data) - len(old.data))
	if err := a.fit(more); err != nil {
		return object{}, err
	}
	file := path.Join(dir, k.dir, id, name)
	if err := writeFile(a.root, file, data); err != nil {
		return object{}, err
	}
	if a.added != nil {
		a.added(more)
	}
	return s.object(a, k, id, name)
}

// delete removes the object name in the collection id of k.
func (s *store) delete(a account, k kind, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.object(a, k, id, name)
	if err != nil {
		return err
	}
	if err := a.root.Remove(path.Join(dir, k.dir, id, name)); err != nil {
		return err
	}
	// The room is free again at once, not only when the usage is counted
	// again.
	if a.added != nil {
		a.added(-int64(len(old.data)))
	}
	return nil
}

// conditions checks If-Match and If-None-Match against the entity tag of
// the stored object, "" when there is none.
func conditions(current string, ifMatch, ifNoneMatch webdav.ConditionalMatch) error {
	if ifMatch.IsSet() {
		ok, err := ifMatch.MatchETag(current)
		if err != nil {
			return statusError{http.StatusBadRequest, err}
		}
		if !ok || current == "" {
			return statusError{http.StatusPreconditionFailed, errors.New("If-Match failed")}
		}
	}
	if ifNoneMatch.IsSet() && current != "" {
		ok, err := ifNoneMatch.MatchETag(current)
		if err != nil {
			return statusError{http.StatusBadRequest, err}
		}
		if ok {
			return statusError{http.StatusPreconditionFailed, errors.New("If-None-Match failed")}
		}
	}
	return nil
}

// writeFile writes data to name through a temporary file, so readers never
// see half of it.
func writeFile(root *os.Root, name string, data []byte) error {
	tmp := path.Join(path.Dir(name), fmt.Sprintf(".tmp-%d", time.Now().UnixNano()))
	if err := root.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// statusError is an answer with its own status code, such as a failed
// condition.
type statusError struct {
	code int
	err  error
}

func (e statusError) Error() string { return e.err.Error() }

// status is the status code and the error a client sees for err, without
// the server's paths.
func status(err error) (int, error) {
	var se statusError
	switch {
	case errors.As(err, &se):
		return se.code, se.err
	case errors.Is(err, errNotFound):
		return http.StatusNotFound, err
	case errors.Is(err, errExists):
		return http.StatusMethodNotAllowed, err
	case errors.Is(err, errNoParent):
		return http.StatusConflict, err
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, err
	}
	slog.Error("calendars and contacts", "err", err)
	return http.StatusInternalServerError, errors.New("storage error")
}

// davError is err for the CalDAV and CardDAV handlers.
func davError(err error) error {
	if err == nil {
		return nil
	}
	return webdav.NewHTTPError(status(err))
}
