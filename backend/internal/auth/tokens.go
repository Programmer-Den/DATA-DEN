package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// App passwords (API tokens) for scripts, apps and WebDAV, so the account's
// own password and second factor stay in the browser:
//
//	GET  /api/account/tokens -> {"tokens": [...]}
//	POST /api/account/tokens {"name", "password", "readOnly", "expiresDays"} -> 201 {"token", ...}
//	POST /api/account/tokens/{id}/delete {} -> 200
//
// The token is shown once. A request sends it as "Authorization: Bearer
// dd_..." or with Basic auth as the account's username and the token. It
// works wherever a session does, except for the account's settings, the
// accounts of others and the app passwords themselves. A read-only token
// only gets GET and HEAD. An admin can revoke all tokens of an account.

const (
	// tokenPrefix marks DATA-DEN tokens, so secret scanners can find them.
	tokenPrefix = "dd_"
	// MaxTokens is how many app passwords one account may have.
	MaxTokens = 50
	// MaxTokenDays is the longest an app password can be set to last.
	MaxTokenDays = 3650
	maxTokenName = 64
)

// Token is an app password. Only the SHA-256 of the token is kept: it has
// 256 random bits, so a slow hash is not needed, and checking one on every
// WebDAV request stays cheap.
type Token struct {
	ID       string    `json:"id"`
	User     string    `json:"user"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	ReadOnly bool      `json:"readOnly,omitempty"`
	Created  time.Time `json:"created"`
	// Expires is zero for a token that lasts until it is revoked.
	Expires  time.Time `json:"expires,omitzero"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}

// TokenInfo is a token in the list, without its hash.
type TokenInfo struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	ReadOnly bool       `json:"readOnly"`
	Created  time.Time  `json:"created"`
	Expires  *time.Time `json:"expires"`
	LastUsed *time.Time `json:"lastUsed"`
}

func (t Token) info() TokenInfo {
	info := TokenInfo{ID: t.ID, Name: t.Name, ReadOnly: t.ReadOnly, Created: t.Created}
	if !t.Expires.IsZero() {
		info.Expires = &t.Expires
	}
	if !t.LastUsed.IsZero() {
		info.LastUsed = &t.LastUsed
	}
	return info
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *Auth) registerTokens(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/tokens", a.requireSession(a.listTokens))
	mux.HandleFunc("POST /api/account/tokens", a.requireSession(a.createToken))
	mux.HandleFunc("POST /api/account/tokens/{id}/delete", a.requireSession(a.deleteToken))
}

// tokenUser checks the Authorization header of r. found is false when r
// has none of the two kinds; then the session cookie decides.
func (a *Auth) tokenUser(r *http.Request) (account Account, readOnly, found bool) {
	header := r.Header.Get("Authorization")
	var username, token string
	switch {
	case strings.HasPrefix(header, "Bearer "):
		token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	case strings.HasPrefix(header, "Basic "):
		var ok bool
		if username, token, ok = r.BasicAuth(); !ok {
			return Account{}, false, true
		}
	default:
		return Account{}, false, false
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return Account{}, false, true
	}
	hash := hashToken(token)
	now := a.cfg.Now()
	a.mu.Lock()
	t, ok := a.tokens[hash]
	if ok && !t.Expires.IsZero() && !now.Before(t.Expires) {
		ok = false
	}
	if ok && username != "" && subtle.ConstantTimeCompare([]byte(username), []byte(t.User)) != 1 {
		ok = false
	}
	if ok {
		// Written to the file once a day, not on every request.
		day := t.LastUsed.Truncate(24 * time.Hour)
		t.LastUsed = now
		a.tokens[hash] = t
		if !day.Equal(now.Truncate(24 * time.Hour)) {
			if err := a.save(); err != nil {
				slog.Error("saving when a token was last used", "err", err)
			}
		}
	}
	a.mu.Unlock()
	if !ok {
		return Account{}, false, true
	}
	account, ok = a.account(t.User)
	return account, t.ReadOnly, ok
}

// tokenFailed answers a request with a wrong token and counts it like a
// failed login.
func (a *Auth) tokenFailed(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	a.fail(ip)
	// fail2ban on the Pi matches this line.
	slog.Warn("login failed", "ip", ip, "reason", "token")
	a.note(r, "", "token-failed", "")
	writeError(w, http.StatusUnauthorized, "App-Passwort ungültig")
}

func (a *Auth) listTokens(w http.ResponseWriter, r *http.Request) {
	name := Username(r)
	tokens := []TokenInfo{}
	a.mu.Lock()
	for _, t := range a.tokens {
		if t.User == name {
			tokens = append(tokens, t.info())
		}
	}
	a.mu.Unlock()
	slices.SortFunc(tokens, func(x, y TokenInfo) int { return x.Created.Compare(y.Created) })
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func validTokenName(name string) bool {
	if name == "" || len(name) > maxTokenName || !utf8.ValidString(name) {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

func (a *Auth) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Password    string `json:"password"`
		ReadOnly    bool   `json:"readOnly"`
		ExpiresDays int    `json:"expiresDays"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !validTokenName(body.Name) {
		writeError(w, http.StatusBadRequest, "Bitte einen Namen mit 1 bis 64 Zeichen angeben")
		return
	}
	if body.ExpiresDays < 0 || body.ExpiresDays > MaxTokenDays {
		writeError(w, http.StatusBadRequest, "Die Gültigkeit darf höchstens "+strconv.Itoa(MaxTokenDays)+" Tage sein")
		return
	}
	ip := a.clientIP(r)
	if wait := a.lockedFor(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte später erneut versuchen")
		return
	}
	name := Username(r)
	// A stolen session alone cannot leave a lasting way in.
	if _, ok := a.checkPassword(name, body.Password); !ok {
		a.fail(ip)
		// fail2ban on the Pi matches this line.
		slog.Warn("login failed", "ip", ip, "username", name, "reason", "token-password")
		writeError(w, http.StatusForbidden, "Das Passwort stimmt nicht")
		return
	}
	secret := make([]byte, 32)
	idBytes := make([]byte, 8)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, http.StatusInternalServerError, "Anlegen fehlgeschlagen")
		return
	}
	if _, err := rand.Read(idBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "Anlegen fehlgeschlagen")
		return
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	now := a.cfg.Now().UTC()
	t := Token{ID: hex.EncodeToString(idBytes), User: name, Name: body.Name, Hash: hashToken(token), ReadOnly: body.ReadOnly, Created: now}
	if body.ExpiresDays > 0 {
		t.Expires = now.AddDate(0, 0, body.ExpiresDays)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokenCount(name) >= MaxTokens {
		writeError(w, http.StatusConflict, "Höchstens "+strconv.Itoa(MaxTokens)+" App-Passwörter je Konto")
		return
	}
	a.tokens[t.Hash] = t
	if err := a.save(); err != nil {
		delete(a.tokens, t.Hash)
		a.saveError(w, err)
		return
	}
	slog.Info("app password created", "username", name, "id", t.ID, "readOnly", t.ReadOnly)
	a.note(r, name, "token-create", t.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "info": t.info()})
}

func (a *Auth) deleteToken(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	name, id := Username(r), r.PathValue("id")
	a.mu.Lock()
	defer a.mu.Unlock()
	for hash, t := range a.tokens {
		if t.User != name || t.ID != id {
			continue
		}
		delete(a.tokens, hash)
		if err := a.save(); err != nil {
			a.tokens[hash] = t
			a.saveError(w, err)
			return
		}
		slog.Info("app password revoked", "username", name, "id", id)
		a.note(r, name, "token-revoke", t.Name)
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	writeError(w, http.StatusNotFound, "App-Passwort nicht gefunden")
}

// dropTokens revokes all app passwords of the account name and returns
// them, to put back when saving fails. The caller holds mu.
func (a *Auth) dropTokens(name string) map[string]Token {
	dropped := map[string]Token{}
	for hash, t := range a.tokens {
		if t.User == name {
			dropped[hash] = t
			delete(a.tokens, hash)
		}
	}
	return dropped
}

// tokenCount is how many app passwords the account name has. The caller
// holds mu.
func (a *Auth) tokenCount(name string) int {
	count := 0
	for _, t := range a.tokens {
		if t.User == name {
			count++
		}
	}
	return count
}
