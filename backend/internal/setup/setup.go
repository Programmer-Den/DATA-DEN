// Package setup is the first run: as long as there is no owner's account,
// the server only serves the website, its health and the setup page, where
// the owner checks the installation and creates the account. A setup code,
// written to the log and into the data directory, keeps others out, as the
// page is already reachable from the internet.
package setup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// MaxFailures wrong codes replace the code with a new one.
const MaxFailures = 10

// codeAlphabet leaves out letters and digits that look alike.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Config is where the setup writes and what it checks.
type Config struct {
	// OwnerFile is where the owner's account is saved.
	OwnerFile string
	// CodeFile is where the setup code is written, for the VM's admin to
	// read; it is removed after the setup.
	CodeFile string
	// DataDir is the data directory, which must be writable.
	DataDir string
	// Status reports the disk and the RAID; nil leaves them out.
	Status *status.Status
	// HTTPS is whether the server serves HTTPS, so the way from the Pi is
	// encrypted.
	HTTPS bool
	// TrustedProxy is DATA_DEN_TRUSTED_PROXY, the Pi's address.
	TrustedProxy string
}

// Owner is the account created on the setup page.
type Owner struct {
	Name         string
	PasswordHash string
}

// Setup serves the setup page's API until the owner's account is created.
type Setup struct {
	cfg      Config
	mu       sync.Mutex
	code     string
	failures int
	created  bool
	done     chan Owner
}

// New makes a setup code, writes it to the log and to CodeFile.
func New(cfg Config) (*Setup, error) {
	s := &Setup{cfg: cfg, done: make(chan Owner, 1)}
	if err := s.newCode(); err != nil {
		return nil, err
	}
	return s, nil
}

// Done delivers the owner's account once it is created and saved.
func (s *Setup) Done() <-chan Owner {
	return s.done
}

// Register adds the setup page's API to mux:
//
//	POST /api/setup/check {"code"} -> the checks of the installation
//	POST /api/setup {"code", "username", "password"} -> 201, the owner's account is created
func (s *Setup) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/setup/check", s.check)
	mux.HandleFunc("POST /api/setup", s.create)
}

// newCode replaces the setup code. The caller holds mu, or nobody else
// has s yet.
func (s *Setup) newCode() error {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	var code strings.Builder
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			code.WriteByte('-')
		}
		code.WriteByte(codeAlphabet[int(v)%len(codeAlphabet)])
	}
	s.code, s.failures = code.String(), 0
	slog.Warn("DATA-DEN is not set up yet: open the website and enter the setup code", "code", s.code)
	if s.cfg.CodeFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.CodeFile), 0o700); err != nil {
		slog.Warn("the setup code cannot be written to the data directory, it is only in this log", "err", err)
		return nil
	}
	if err := os.WriteFile(s.cfg.CodeFile, []byte(s.code+"\n"), 0o600); err != nil {
		slog.Warn("the setup code cannot be written to the data directory, it is only in this log", "err", err)
	}
	return nil
}

// Checks is the answer of POST /api/setup/check.
type Checks struct {
	DataDir string `json:"dataDir"`
	// Writable is false when the server cannot write to DataDir, and
	// WriteError says why.
	Writable   bool   `json:"writable"`
	WriteError string `json:"writeError,omitempty"`
	// Disk and RAID are as on the status page.
	Disk      *status.Disk   `json:"disk"`
	DiskError string         `json:"diskError,omitempty"`
	RAID      []status.Array `json:"raid"`
	HTTPS     bool           `json:"https"`
	// TrustedProxy is the Pi's address, empty when it is not set.
	TrustedProxy string `json:"trustedProxy"`
}

func (s *Setup) checks() Checks {
	c := Checks{DataDir: s.cfg.DataDir, HTTPS: s.cfg.HTTPS, TrustedProxy: s.cfg.TrustedProxy}
	if err := writable(s.cfg.DataDir); err != nil {
		c.WriteError = err.Error()
	} else {
		c.Writable = true
	}
	if s.cfg.Status != nil {
		report := s.cfg.Status.Report()
		c.Disk, c.DiskError, c.RAID = report.Disk, report.DiskError, report.RAID
	}
	return c
}

// writable tries to create and remove a file in dir.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".data-den-setup-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func (s *Setup) check(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &body) || !s.checkCode(w, body.Code) {
		return
	}
	writeJSON(w, http.StatusOK, s.checks())
}

func (s *Setup) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) || !s.checkCode(w, body.Code) {
		return
	}
	if !auth.ValidName(body.Username) {
		writeError(w, http.StatusBadRequest, "Der Benutzername darf nur Kleinbuchstaben, Ziffern, Punkt, Binde- und Unterstrich enthalten, höchstens 32 Zeichen")
		return
	}
	if len(body.Password) < auth.MinPasswordBytes {
		writeError(w, http.StatusBadRequest, "Das Passwort braucht mindestens 10 Zeichen")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Das Passwort darf höchstens 72 Bytes lang sein")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created {
		writeError(w, http.StatusConflict, "DATA-DEN ist schon eingerichtet")
		return
	}
	if err := writable(s.cfg.DataDir); err != nil {
		writeError(w, http.StatusServiceUnavailable, "In den Datenordner kann nicht geschrieben werden: "+err.Error())
		return
	}
	if err := auth.SaveOwner(s.cfg.OwnerFile, body.Username, hash); err != nil {
		slog.Error("saving the owner's account", "err", err)
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	s.created = true
	if s.cfg.CodeFile != "" {
		_ = os.Remove(s.cfg.CodeFile)
	}
	slog.Info("owner's account created on the setup page", "username", body.Username)
	writeJSON(w, http.StatusCreated, map[string]string{"username": body.Username})
	s.done <- Owner{Name: body.Username, PasswordHash: hash}
}

// checkCode answers the request itself and returns false when code is not
// the setup code. Too many wrong codes replace it.
func (s *Setup) checkCode(w http.ResponseWriter, code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	s.mu.Lock()
	defer s.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.code)) == 1 {
		return true
	}
	s.failures++
	slog.Warn("wrong setup code")
	if s.failures >= MaxFailures {
		if err := s.newCode(); err != nil {
			slog.Error("making a new setup code", "err", err)
		}
	}
	writeError(w, http.StatusForbidden, "Der Einrichtungscode stimmt nicht")
	return false
}

// readJSON reads a small JSON body into v. It answers the request itself and
// returns false when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// A JSON body cannot be sent by a plain HTML form on another site.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
