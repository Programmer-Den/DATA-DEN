// Package auth handles the login: the owner's account from the settings and
// further accounts the admins create, passwords stored as bcrypt hashes,
// sessions in a cookie, app passwords for scripts and WebDAV, and a limit on
// failed logins per client.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// CookieName is the session cookie.
	CookieName = "data_den_session"
	// SessionLifetime is how long a login lasts.
	SessionLifetime = 7 * 24 * time.Hour
	// MaxFailures failed logins from one client within FailureWindow lock it
	// out until the window has passed.
	MaxFailures = 5
	// FailureWindow is how far back failed logins count.
	FailureWindow = 15 * time.Minute
	// MaxPasswordBytes is the most bcrypt uses; longer passwords are refused
	// instead of silently cut off.
	MaxPasswordBytes = 72
)

// Config is the accounts and where requests come from.
type Config struct {
	// Username and PasswordHash (bcrypt) of the owner's account, which is
	// always an admin. Without a hash, no one can log in.
	Username     string
	PasswordHash string
	// AccountsFile keeps the other accounts; empty keeps them in memory only.
	AccountsFile string
	// Usage is how many bytes an account stores, for the list of accounts;
	// nil leaves it out.
	Usage func(name string) (int64, error)
	// Removed is called before an account is deleted, to move its files
	// aside; nil does nothing.
	Removed func(name string) error
	// Audit notes logins and changes to accounts in the audit log; nil
	// notes nothing.
	Audit func(r *http.Request, user, action, target string)
	// TrustedProxy is the address of the reverse proxy (the Pi). Only for
	// requests from it is X-Forwarded-For used as the client's address.
	TrustedProxy string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Auth holds the accounts, the sessions and the failed logins. Sessions and
// failures live in memory, so a restart logs everyone out.
type Auth struct {
	cfg      Config
	dummy    []byte
	mu       sync.Mutex
	accounts map[string]Account
	// twoFactor holds the accounts with a second factor, the owner's too.
	twoFactor map[string]TwoFactor
	// pendingSecrets are secrets being set up, until the first code fits.
	pendingSecrets map[string]pendingSecret
	// tokens are the app passwords, by the hash of the token.
	tokens   map[string]Token
	sessions map[string]session
	failures map[string][]time.Time
}

type session struct {
	username string
	expires  time.Time
}

// HashPassword returns the bcrypt hash to put into DATA_DEN_PASSWORD_HASH.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("the password is empty")
	}
	if len(password) > MaxPasswordBytes {
		return "", errors.New("the password is longer than 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), hashCost)
	return string(hash), err
}

// New checks the config and returns the login handlers.
func New(cfg Config) (*Auth, error) {
	cost := bcrypt.MinCost
	if cfg.PasswordHash != "" {
		var err error
		if cost, err = bcrypt.Cost([]byte(cfg.PasswordHash)); err != nil {
			return nil, errors.New("DATA_DEN_PASSWORD_HASH is not a bcrypt hash; create one with: data-den hash-password")
		}
		if cfg.Username == "" {
			return nil, errors.New("DATA_DEN_USERNAME is empty")
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// Compared against when the username is wrong, so that answer takes as
	// long as a wrong password.
	dummy, err := bcrypt.GenerateFromPassword([]byte("not the password"), cost)
	if err != nil {
		return nil, err
	}
	stored, err := loadAccounts(cfg.AccountsFile, cfg.Username)
	if err != nil {
		return nil, err
	}
	return &Auth{
		cfg:            cfg,
		dummy:          dummy,
		accounts:       stored.accounts,
		twoFactor:      stored.twoFactor,
		pendingSecrets: map[string]pendingSecret{},
		tokens:         stored.tokens,
		sessions:       map[string]session{},
		failures:       map[string][]time.Time{},
	}, nil
}

// Register adds /api/login, /api/logout, /api/session, /api/account and
// /api/users to mux.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/session", a.Require(a.showSession))
	a.registerAccounts(mux)
	a.registerTwoFactor(mux)
	a.registerTokens(mux)
}

type userKey struct{}

// Username is the logged-in user of a request that passed Require.
func Username(r *http.Request) string {
	return user(r).Name
}

func user(r *http.Request) Account {
	account, _ := r.Context().Value(userKey{}).(Account)
	return account
}

// Owner is the account whose files are the data directory itself.
func (a *Auth) Owner() string {
	return a.cfg.Username
}

// Quota is how many bytes the account name may store, 0 for no limit.
func (a *Auth) Quota(name string) int64 {
	account, _ := a.account(name)
	return account.Quota
}

// Require only lets requests with a valid session or app password through.
// A read-only app password only gets GET and HEAD.
func (a *Auth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if wait := a.lockedFor(a.clientIP(r)); wait > 0 && r.Header.Get("Authorization") != "" {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte später erneut versuchen")
			return
		}
		account, readOnly, found := a.tokenUser(r)
		if found {
			if account.Name == "" {
				a.tokenFailed(w, r)
				return
			}
			if readOnly && r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeError(w, http.StatusForbidden, "Dieses App-Passwort darf nur lesen")
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, account)))
			return
		}
		a.requireSession(next)(w, r)
	}
}

// requireSession only lets requests with a valid session through, not app
// passwords: for the account's settings and the accounts of others.
func (a *Auth) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			writeError(w, http.StatusForbidden, "Nur nach der Anmeldung im Browser, nicht mit einem App-Passwort")
			return
		}
		account, ok := a.sessionUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "Nicht angemeldet")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, account)))
	}
}

// RequireAdmin only lets admins with a session through.
func (a *Auth) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return a.requireSession(func(w http.ResponseWriter, r *http.Request) {
		if !user(r).Admin {
			writeError(w, http.StatusForbidden, "Nur für Admins")
			return
		}
		next(w, r)
	})
}

func (a *Auth) showSession(w http.ResponseWriter, r *http.Request) {
	account := user(r)
	writeJSON(w, http.StatusOK, a.sessionInfo(account))
}

// sessionInfo is the answer of /api/session and of a login.
type sessionInfo struct {
	Username  string `json:"username"`
	Admin     bool   `json:"admin"`
	Owner     bool   `json:"owner"`
	TwoFactor bool   `json:"twoFactor"`
}

func (a *Auth) sessionInfo(account Account) sessionInfo {
	return sessionInfo{
		Username:  account.Name,
		Admin:     account.Admin,
		Owner:     account.Name == a.cfg.Username,
		TwoFactor: a.hasSecondFactor(account.Name),
	}
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	// A JSON body cannot be sent by a plain HTML form on another site.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return
	}
	if a.cfg.PasswordHash == "" {
		writeError(w, http.StatusServiceUnavailable, "Kein Passwort eingerichtet")
		return
	}
	ip := a.clientIP(r)
	if wait := a.lockedFor(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte später erneut versuchen")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		// Code is the second factor, for accounts that have one.
		Code string `json:"code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}

	account, ok := a.checkPassword(body.Username, body.Password)
	if !ok {
		a.fail(ip)
		// fail2ban on the Pi matches this line.
		slog.Warn("login failed", "ip", ip, "username", body.Username)
		a.note(r, body.Username, "login-failed", "")
		writeError(w, http.StatusUnauthorized, "Benutzername oder Passwort falsch")
		return
	}
	if a.hasSecondFactor(account.Name) {
		if body.Code == "" {
			// Not a failure: the page asks for the code and sends all again.
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Bitte den Code aus der App eingeben", "twoFactor": true})
			return
		}
		if !a.checkSecondFactor(account.Name, body.Code) {
			a.fail(ip)
			// fail2ban on the Pi matches this line.
			slog.Warn("login failed", "ip", ip, "username", body.Username, "reason", "code")
			a.note(r, account.Name, "login-failed", "Code")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Der Code stimmt nicht", "twoFactor": true})
			return
		}
	}
	a.clearFailures(ip)

	token, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Anmeldung fehlgeschlagen")
		return
	}
	expires := a.cfg.Now().Add(SessionLifetime)
	a.mu.Lock()
	a.dropExpired()
	a.sessions[token] = session{username: account.Name, expires: expires}
	a.mu.Unlock()

	slog.Info("login", "ip", ip, "username", account.Name)
	a.note(r, account.Name, "login", "")
	http.SetCookie(w, cookie(token, expires))
	writeJSON(w, http.StatusOK, a.sessionInfo(account))
}

// note records in the audit log that user did action to target.
func (a *Auth) note(r *http.Request, user, action, target string) {
	if a.cfg.Audit != nil {
		a.cfg.Audit(r, user, action, target)
	}
}

// ClientIP is the address r came from, see clientIP.
func (a *Auth) ClientIP(r *http.Request) string {
	return a.clientIP(r)
}

// IsAdmin reports whether the user of a request that passed Require is an
// admin.
func IsAdmin(r *http.Request) bool {
	return user(r).Admin
}

// Wait is how long the client of r is still locked out after too many
// wrong passwords, or 0. Share links use it for their passwords.
func (a *Auth) Wait(r *http.Request) time.Duration {
	return a.lockedFor(a.clientIP(r))
}

// Failed counts a wrong share password from the client of r like a failed
// login, so guessing one also locks the client out of logging in.
func (a *Auth) Failed(r *http.Request, share string) {
	ip := a.clientIP(r)
	a.fail(ip)
	// fail2ban on the Pi matches this line.
	slog.Warn("login failed", "ip", ip, "share", share)
	a.note(r, "", "share-password-failed", share)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		s, ok := a.sessions[c.Value]
		delete(a.sessions, c.Value)
		a.mu.Unlock()
		if ok {
			a.note(r, s.username, "logout", "")
		}
	}
	http.SetCookie(w, cookie("", time.Unix(0, 0)))
	w.WriteHeader(http.StatusNoContent)
}

// checkPassword returns the account if the password is right. A wrong
// username takes as long as a wrong password.
func (a *Auth) checkPassword(username, password string) (Account, bool) {
	if len(password) > MaxPasswordBytes {
		return Account{}, false
	}
	account, userOK := a.account(username)
	hash := []byte(account.PasswordHash)
	if !userOK || len(hash) == 0 {
		hash = a.dummy
		userOK = false
	}
	ok := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil && userOK
	return account, ok
}

func (a *Auth) sessionUser(r *http.Request) (Account, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Account{}, false
	}
	a.mu.Lock()
	s, ok := a.sessions[c.Value]
	if ok && !a.cfg.Now().Before(s.expires) {
		delete(a.sessions, c.Value)
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		return Account{}, false
	}
	// Gone when the account was deleted since.
	return a.account(s.username)
}

// account returns the account name, the owner's included.
func (a *Auth) account(name string) (Account, bool) {
	if name == "" {
		return Account{}, false
	}
	if subtle.ConstantTimeCompare([]byte(name), []byte(a.cfg.Username)) == 1 {
		return Account{Name: a.cfg.Username, PasswordHash: a.cfg.PasswordHash, Admin: true}, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account, ok := a.accounts[name]
	return account, ok
}

// dropSessions logs the account name out everywhere but in the session
// keep. The caller holds mu.
func (a *Auth) dropSessions(name, keep string) {
	for token, s := range a.sessions {
		if s.username == name && token != keep {
			delete(a.sessions, token)
		}
	}
}

// lockedFor is how long ip still has to wait, or 0.
func (a *Auth) lockedFor(ip string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	recent := a.recentFailures(ip)
	if len(recent) < MaxFailures {
		return 0
	}
	return recent[0].Add(FailureWindow).Sub(a.cfg.Now())
}

func (a *Auth) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Keeps the map small when many addresses try their luck.
	if len(a.failures) > 10000 {
		for other := range a.failures {
			a.recentFailures(other)
		}
	}
	a.failures[ip] = append(a.recentFailures(ip), a.cfg.Now())
}

func (a *Auth) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, ip)
}

// recentFailures drops the failures of ip older than FailureWindow. The
// caller holds mu.
func (a *Auth) recentFailures(ip string) []time.Time {
	cutoff := a.cfg.Now().Add(-FailureWindow)
	var recent []time.Time
	for _, t := range a.failures[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(a.failures, ip)
	} else {
		a.failures[ip] = recent
	}
	return recent
}

// dropExpired removes expired sessions. The caller holds mu.
func (a *Auth) dropExpired() {
	now := a.cfg.Now()
	for token, s := range a.sessions {
		if !now.Before(s.expires) {
			delete(a.sessions, token)
		}
	}
}

// clientIP is the address the request came from: from X-Forwarded-For when
// it came through the trusted proxy, else from the connection.
func (a *Auth) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a.cfg.TrustedProxy == "" || host != a.cfg.TrustedProxy {
		return host
	}
	// The proxy appends the address it saw, so the last entry is the one to
	// trust; earlier ones the client may have made up.
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return host
	}
	parts := strings.Split(forwarded[len(forwarded)-1], ",")
	if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
		return ip.String()
	}
	return host
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// cookie is only sent over HTTPS (browsers treat localhost as secure too),
// never to scripts and never with requests from other sites.
func cookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
