package pim

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// The URLs, with the account's name after the prefix:
//
//	/caldav/<name>/                        the principal
//	/caldav/<name>/calendars/              the calendars
//	/caldav/<name>/calendars/<id>/         a calendar
//	/caldav/<name>/calendars/<id>/<event>  an event or a task
//
// and the same for /carddav/<name>/contacts/. Clients find them through
// /.well-known/caldav and /.well-known/carddav.
const (
	// CalPrefix is where CalDAV is served.
	CalPrefix = "/caldav"
	// CardPrefix is where CardDAV is served.
	CardPrefix = "/carddav"
)

// Routes are the paths to register, for answering them while the data
// directory is missing.
var Routes = []string{CalPrefix + "/", CardPrefix + "/", "/.well-known/caldav", "/.well-known/carddav"}

// Config says whose calendars and contacts a request is for.
type Config struct {
	// User is the logged-in account of a request that passed the login
	// check.
	User func(*http.Request) string
	// Space opens the root of an account's space.
	Space func(name string) (*os.Root, error)
	// Fits reports whether n more bytes fit into the quota of the account
	// name; nil sets no limit.
	Fits func(name string, n int64) (bool, error)
	// Added counts n bytes stored in the space of the account name; nil
	// counts nothing.
	Added func(name string, n int64)
}

// PIM serves the calendars and contacts of every account.
type PIM struct {
	cfg   Config
	store store
}

// New returns the calendars and contacts of the accounts in cfg.
func New(cfg Config) *PIM {
	return &PIM{cfg: cfg}
}

type accountKey struct{}

// from is the account of a request.
func from(ctx context.Context) (account, error) {
	if a, ok := ctx.Value(accountKey{}).(account); ok {
		return a, nil
	}
	return account{}, errors.New("no account")
}

// Register adds CalDAV under CalPrefix and CardDAV under CardPrefix to mux,
// with their well-known addresses, behind require (a login check that
// takes app passwords).
func (p *PIM) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	cal := require(p.serve(&caldav.Handler{Backend: calBackend{p}, Prefix: CalPrefix}))
	card := require(p.serve(&carddav.Handler{Backend: cardBackend{p}, Prefix: CardPrefix}))
	for _, route := range []string{CalPrefix, CalPrefix + "/", "/.well-known/caldav"} {
		mux.HandleFunc(route, cal)
	}
	for _, route := range []string{CardPrefix, CardPrefix + "/", "/.well-known/carddav"} {
		mux.HandleFunc(route, card)
	}
}

// serve answers a request with h for the logged-in account.
func (p *PIM) serve(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := p.cfg.User(r)
		root, err := p.cfg.Space(name)
		if err != nil {
			slog.Error("opening a space", "username", name, "err", err)
			http.Error(w, "Speicherbereich nicht verfügbar", http.StatusInternalServerError)
			return
		}
		a := account{name: name, root: root}
		if p.cfg.Fits != nil {
			a.fits = func(n int64) (bool, error) { return p.cfg.Fits(name, n) }
		}
		if p.cfg.Added != nil {
			a.added = func(n int64) { p.cfg.Added(name, n) }
		}
		for _, k := range []kind{calendars, contacts} {
			if err := p.store.ensure(a, k); err != nil {
				code, err := status(err)
				http.Error(w, err.Error(), code)
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), accountKey{}, a))
		r.Body = http.MaxBytesReader(w, r.Body, maxObject)
		if r.Method == "MKCALENDAR" {
			p.mkcalendar(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}
}

// mkcalendarRequest is the body of MKCALENDAR (RFC 4791 5.3.1), which the
// CalDAV handler does not know: Apple's Calendar and Thunderbird create
// calendars with it.
type mkcalendarRequest struct {
	Set struct {
		Prop struct {
			DisplayName string `xml:"DAV: displayname"`
			Description string `xml:"urn:ietf:params:xml:ns:caldav calendar-description"`
		} `xml:"DAV: prop"`
	} `xml:"DAV: set"`
}

// mkcalendar creates the calendar at the request's path.
func (p *PIM) mkcalendar(w http.ResponseWriter, r *http.Request) {
	a, err := from(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, obj, err := a.parse(calendars, r.URL.Path)
	if err == nil && (id == "" || obj != "") {
		err = errForbidden
	}
	var req mkcalendarRequest
	if err == nil {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			err = statusError{http.StatusRequestEntityTooLarge, readErr}
		} else if len(bytes.TrimSpace(body)) > 0 {
			if xmlErr := xml.Unmarshal(body, &req); xmlErr != nil {
				err = statusError{http.StatusBadRequest, xmlErr}
			}
		}
	}
	if err == nil {
		err = p.store.create(a, calendars, collection{ID: id, Name: req.Set.Prop.DisplayName, Description: req.Set.Prop.Description})
	}
	if err != nil {
		code, err := status(err)
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
