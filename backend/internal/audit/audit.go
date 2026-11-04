// Package audit keeps the audit log: who logged in, changed an account or
// changed files, when and from where. It is a file of JSON lines in the data
// directory; when it grows past MaxBytes it moves to a .1 file, so the log
// keeps the latest between MaxBytes and twice as much.
//
//	GET /api/audit?user=anna&limit=200 -> {"entries": [...]}, newest first
//
// Admins see everyone's entries and may filter by user; everyone else sees
// only their own.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	// MaxBytes is the size at which the log starts a new file.
	MaxBytes = 5 << 20
	// DefaultLimit is how many entries /api/audit answers without a limit.
	DefaultLimit = 200
	// MaxLimit is the most entries /api/audit answers.
	MaxLimit = 2000
	// maxField keeps a made-up username or path from bloating the log.
	maxField = 256
)

// Entry is one thing that happened.
type Entry struct {
	Time time.Time `json:"time"`
	// User is the account, or the name typed into a failed login.
	User string `json:"user"`
	IP   string `json:"ip,omitempty"`
	// Action is what happened, such as "login" or "upload".
	Action string `json:"action"`
	// Target is what it happened to, such as a path or an account.
	Target string `json:"target,omitempty"`
}

// Config is where the log goes and how a request is read.
type Config struct {
	// File is the log; empty keeps no log.
	File string
	// IP is the client address of a request.
	IP func(*http.Request) string
	// User and Admin tell who is logged in, for /api/audit.
	User  func(*http.Request) string
	Admin func(*http.Request) bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Log writes and reads the audit log.
type Log struct {
	cfg Config
	mu  sync.Mutex
}

// New returns the log.
func New(cfg Config) *Log {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Log{cfg: cfg}
}

// Record notes that user did action to target, in the request r.
func (l *Log) Record(r *http.Request, user, action, target string) {
	if l == nil || l.cfg.File == "" {
		return
	}
	e := Entry{Time: l.cfg.Now().UTC(), User: cut(user), Action: action, Target: cut(target)}
	if r != nil && l.cfg.IP != nil {
		e.IP = l.cfg.IP(r)
	}
	if err := l.write(e); err != nil {
		slog.Error("writing the audit log", "err", err)
	}
}

func cut(s string) string {
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

func (l *Log) write(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.cfg.File), 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(l.cfg.File); err == nil && info.Size() >= MaxBytes {
		if err := os.Rename(l.cfg.File, l.cfg.File+".1"); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(l.cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Entries returns the newest entries of user (all for ""), at most limit.
func (l *Log) Entries(user string, limit int) ([]Entry, error) {
	entries := []Entry{}
	if l == nil || l.cfg.File == "" {
		return entries, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, name := range []string{l.cfg.File + ".1", l.cfg.File} {
		read, err := readFile(name, user)
		if err != nil {
			return nil, err
		}
		entries = append(entries, read...)
	}
	slices.Reverse(entries)
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func readFile(name, user string) ([]Entry, error) {
	file, err := os.Open(filepath.Clean(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var entries []Entry
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		var e Entry
		// A line cut off by a crash, or anything else that is not an
		// entry, is skipped.
		if len(line) > 0 && json.Unmarshal(line, &e) == nil && e.Action != "" && (user == "" || e.User == user) {
			entries = append(entries, e)
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Register adds /api/audit to mux, behind require (the login check).
func (l *Log) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/audit", require(l.list))
}

func (l *Log) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	user := l.cfg.User(r)
	if l.cfg.Admin(r) {
		user = query.Get("user")
	}
	limit := DefaultLimit
	if value := query.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Ungültige Anzahl"})
			return
		}
		limit = min(n, MaxLimit)
	}
	entries, err := l.Entries(user, limit)
	if err != nil {
		slog.Error("reading the audit log", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Protokoll nicht lesbar"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
