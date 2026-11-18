package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys (WebAuthn) log in without a password and second factor: the
// browser or phone signs a challenge with a key that only works on the
// site's own domain, so a look-alike site gets nothing it could use.
//
//	GET  /api/login/options                 -> {"passkeys"}, whether passkeys are set up
//	POST /api/login/passkey/begin {}        -> {"id", "publicKey"}, the options for navigator.credentials.get
//	POST /api/login/passkey {"id", "credential"} -> the session, like /api/login
//	GET  /api/account/passkeys              -> {"available", "passkeys": [...]}
//	POST /api/account/passkeys/begin {"password", "code"} -> {"publicKey"}, the options for navigator.credentials.create
//	POST /api/account/passkeys {"name", "credential"} -> 201 {"id", ...}
//	POST /api/account/passkeys/{id}/delete {} -> 200
//
// The browser checks that the page's origin belongs to the passkey's domain
// (the relying party), and the server checks the origin the browser signed,
// so they only work at Config.Origin. A passkey always asks for the PIN,
// fingerprint or face on the device, so it counts as both factors. Adding
// one needs the password, and the code when 2FA is on, as it is as good as
// both. An admin can remove all passkeys of an account.

const (
	// MaxPasskeys is how many passkeys one account may have.
	MaxPasskeys = 20
	// passkeyTimeout is how long a started login or registration lasts.
	passkeyTimeout = 5 * time.Minute
	// maxPasskeyLogins is how many started logins are kept at once, as
	// anyone can start one.
	maxPasskeyLogins = 1000
	maxPasskeyName   = 64
	// maxPasskeyBody is larger than readJSON's limit: a registration carries
	// the public key and the authenticator's data.
	maxPasskeyBody = 64 << 10
)

// Passkey is one passkey of an account.
type Passkey struct {
	User string `json:"user"`
	Name string `json:"name"`
	// Handle is the account's user handle (base64url): random, the same
	// for all passkeys of an account, and sent back by the device on login.
	Handle     string              `json:"handle"`
	Created    time.Time           `json:"created"`
	LastUsed   time.Time           `json:"lastUsed,omitzero"`
	Credential webauthn.Credential `json:"credential"`
}

// id is the credential ID, base64url, which also names the passkey in the
// API.
func (p Passkey) id() string {
	return base64.RawURLEncoding.EncodeToString(p.Credential.ID)
}

// PasskeyInfo is a passkey in the list.
type PasskeyInfo struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed"`
}

func (p Passkey) info() PasskeyInfo {
	info := PasskeyInfo{ID: p.id(), Name: p.Name, Created: p.Created}
	if !p.LastUsed.IsZero() {
		info.LastUsed = &p.LastUsed
	}
	return info
}

// ceremony is a started login or registration, until the device answers.
type ceremony struct {
	data    webauthn.SessionData
	user    string
	expires time.Time
}

// passkeyUser is an account as the WebAuthn library sees it.
type passkeyUser struct {
	name        string
	handle      []byte
	credentials []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// newRelyingParty returns the WebAuthn settings for origin, such as
// https://nas.example.de, or nil when origin is empty.
func newRelyingParty(origin string) (*webauthn.WebAuthn, error) {
	if origin == "" {
		return nil, nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("DATA_DEN_ORIGIN must be the address DATA-DEN is opened at, such as https://nas.example.de, not %q", origin)
	}
	return webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: issuer,
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
		// No attestation: which device it is does not matter, and asking
		// would show an extra prompt.
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
			Registration: webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
		},
	})
}

func (a *Auth) registerPasskeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/login/options", a.loginOptions)
	mux.HandleFunc("POST /api/login/passkey/begin", a.beginPasskeyLogin)
	mux.HandleFunc("POST /api/login/passkey", a.finishPasskeyLogin)
	mux.HandleFunc("GET /api/account/passkeys", a.requireSession(a.listPasskeys))
	mux.HandleFunc("POST /api/account/passkeys/begin", a.requireSession(a.beginPasskey))
	mux.HandleFunc("POST /api/account/passkeys", a.requireSession(a.addPasskey))
	mux.HandleFunc("POST /api/account/passkeys/{id}/delete", a.requireSession(a.deletePasskey))
}

// passkeysOff answers the request itself and returns true when passkeys are
// not set up.
func (a *Auth) passkeysOff(w http.ResponseWriter) bool {
	if a.rp == nil {
		writeError(w, http.StatusNotFound, "Passkeys sind nicht eingerichtet (DATA_DEN_ORIGIN)")
		return true
	}
	return false
}

func (a *Auth) loginOptions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"passkeys": a.rp != nil && a.cfg.PasswordHash != ""})
}

func (a *Auth) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !readJSON(w, r, &body) || a.passkeysOff(w) {
		return
	}
	if a.cfg.PasswordHash == "" {
		writeError(w, http.StatusServiceUnavailable, "Kein Passwort eingerichtet")
		return
	}
	if a.locked(w, r) {
		return
	}
	options, data, err := a.rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		slog.Error("starting a passkey login", "err", err)
		writeError(w, http.StatusInternalServerError, "Anmeldung fehlgeschlagen")
		return
	}
	id, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Anmeldung fehlgeschlagen")
		return
	}
	now := a.cfg.Now()
	a.mu.Lock()
	for other, c := range a.passkeyLogins {
		if !now.Before(c.expires) {
			delete(a.passkeyLogins, other)
		}
	}
	full := len(a.passkeyLogins) >= maxPasskeyLogins
	if !full {
		a.passkeyLogins[id] = ceremony{data: *data, expires: now.Add(passkeyTimeout)}
	}
	a.mu.Unlock()
	if full {
		writeError(w, http.StatusServiceUnavailable, "Zu viele Anmeldungen gleichzeitig, bitte gleich noch einmal versuchen")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "publicKey": options.Response})
}

// locked answers the request itself and returns true when its client is
// locked out after too many failed logins.
func (a *Auth) locked(w http.ResponseWriter, r *http.Request) bool {
	if wait := a.lockedFor(a.clientIP(r)); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte später erneut versuchen")
		return true
	}
	return false
}

// passkeyBody reads {"id"?, "name"?, "credential"} with the credential kept
// as it came, for the WebAuthn library. It answers the request itself and
// returns false when that fails.
func passkeyBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPasskeyBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return false
	}
	return true
}

func (a *Auth) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string          `json:"id"`
		Credential json.RawMessage `json:"credential"`
	}
	if !passkeyBody(w, r, &body) || a.passkeysOff(w) || a.locked(w, r) {
		return
	}
	ip := a.clientIP(r)
	now := a.cfg.Now()
	a.mu.Lock()
	c, ok := a.passkeyLogins[body.ID]
	// A challenge is only good for one try.
	delete(a.passkeyLogins, body.ID)
	a.mu.Unlock()
	if !ok || !now.Before(c.expires) {
		writeError(w, http.StatusBadRequest, "Die Anmeldung hat zu lange gedauert, bitte noch einmal")
		return
	}

	var found Passkey
	user, credential, err := a.validatePasskey(c.data, body.Credential, &found)
	if err != nil {
		a.fail(ip)
		// fail2ban on the Pi matches this line.
		slog.Warn("login failed", "ip", ip, "username", found.User, "reason", "passkey", "err", err)
		a.note(r, found.User, "login-failed", "Passkey")
		writeError(w, http.StatusUnauthorized, "Der Passkey wurde nicht erkannt")
		return
	}
	account, ok := a.account(user)
	if !ok {
		a.fail(ip)
		writeError(w, http.StatusUnauthorized, "Der Passkey wurde nicht erkannt")
		return
	}
	a.mu.Lock()
	if p, ok := a.passkeys[found.id()]; ok {
		p.Credential.Authenticator = credential.Authenticator
		p.Credential.Flags = credential.Flags
		p.LastUsed = now
		a.passkeys[found.id()] = p
		if err := a.save(); err != nil {
			slog.Error("saving when a passkey was last used", "err", err)
		}
	}
	a.mu.Unlock()
	a.clearFailures(ip)
	a.startSession(w, r, account, "passkey")
}

// validatePasskey checks a login's answer and returns the account and the
// updated credential. found is the passkey it names, if any, for the log.
func (a *Auth) validatePasskey(data webauthn.SessionData, raw []byte, found *Passkey) (string, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return "", nil, err
	}
	lookup := func(rawID, handle []byte) (webauthn.User, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		p, ok := a.passkeys[base64.RawURLEncoding.EncodeToString(rawID)]
		if !ok {
			return nil, errors.New("unknown passkey")
		}
		*found = p
		if p.Handle != base64.RawURLEncoding.EncodeToString(handle) {
			return nil, errors.New("the user handle does not fit the passkey")
		}
		return a.passkeyUser(p.User, p.Handle), nil
	}
	user, credential, err := a.rp.ValidatePasskeyLogin(lookup, data, parsed)
	if err != nil {
		return "", nil, err
	}
	if credential.Authenticator.CloneWarning {
		return "", nil, errors.New("the signature counter went back, the passkey may be cloned")
	}
	return user.WebAuthnName(), credential, nil
}

// passkeyUser is the account name with its passkeys. The caller holds mu.
func (a *Auth) passkeyUser(name, handle string) passkeyUser {
	u := passkeyUser{name: name}
	u.handle, _ = base64.RawURLEncoding.DecodeString(handle)
	for _, p := range a.sortedPasskeys(name) {
		u.credentials = append(u.credentials, p.Credential)
	}
	return u
}

// sortedPasskeys are the passkeys of name, oldest first. The caller holds
// mu.
func (a *Auth) sortedPasskeys(name string) []Passkey {
	var list []Passkey
	for _, p := range a.passkeys {
		if p.User == name {
			list = append(list, p)
		}
	}
	slices.SortFunc(list, func(x, y Passkey) int { return x.Created.Compare(y.Created) })
	return list
}

func (a *Auth) listPasskeys(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	list := []PasskeyInfo{}
	for _, p := range a.sortedPasskeys(Username(r)) {
		list = append(list, p.info())
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"available": a.rp != nil, "passkeys": list})
}

func (a *Auth) beginPasskey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &body) || a.passkeysOff(w) || a.locked(w, r) {
		return
	}
	name := Username(r)
	if _, ok := a.checkPassword(name, body.Password); !ok {
		a.fail(a.clientIP(r))
		writeError(w, http.StatusForbidden, "Das Passwort stimmt nicht")
		return
	}
	if a.hasSecondFactor(name) && !a.checkSecondFactor(name, body.Code) {
		a.fail(a.clientIP(r))
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Der Code stimmt nicht", "twoFactor": true})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	existing := a.sortedPasskeys(name)
	if len(existing) >= MaxPasskeys {
		writeError(w, http.StatusConflict, fmt.Sprintf("Höchstens %d Passkeys je Konto", MaxPasskeys))
		return
	}
	handle := ""
	if len(existing) > 0 {
		handle = existing[0].Handle
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			writeError(w, http.StatusInternalServerError, "Einrichten fehlgeschlagen")
			return
		}
		handle = base64.RawURLEncoding.EncodeToString(b)
	}
	user := a.passkeyUser(name, handle)
	options, data, err := a.rp.BeginRegistration(user,
		webauthn.WithExclusions(webauthn.Credentials(user.credentials).CredentialDescriptors()),
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()),
	)
	if err != nil {
		slog.Error("starting a passkey registration", "err", err)
		writeError(w, http.StatusInternalServerError, "Einrichten fehlgeschlagen")
		return
	}
	a.pendingPasskeys[name] = ceremony{data: *data, user: handle, expires: a.cfg.Now().Add(passkeyTimeout)}
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": options.Response})
}

func (a *Auth) addPasskey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if !passkeyBody(w, r, &body) || a.passkeysOff(w) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || utf8.RuneCountInString(body.Name) > maxPasskeyName || strings.IndexFunc(body.Name, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "Der Name braucht 1 bis 64 Zeichen")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Antwort des Geräts")
		return
	}
	name := Username(r)
	now := a.cfg.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.pendingPasskeys[name]
	delete(a.pendingPasskeys, name)
	if !ok || !now.Before(c.expires) {
		writeError(w, http.StatusBadRequest, "Bitte das Einrichten neu beginnen")
		return
	}
	credential, err := a.rp.CreateCredential(a.passkeyUser(name, c.user), c.data, parsed)
	if err != nil {
		slog.Warn("passkey not added", "username", name, "err", err)
		writeError(w, http.StatusBadRequest, "Das Gerät hat den Passkey nicht bestätigt")
		return
	}
	p := Passkey{User: name, Name: body.Name, Handle: c.user, Created: now, Credential: *credential}
	if _, taken := a.passkeys[p.id()]; taken {
		writeError(w, http.StatusConflict, "Diesen Passkey gibt es schon")
		return
	}
	a.passkeys[p.id()] = p
	if err := a.save(); err != nil {
		delete(a.passkeys, p.id())
		a.saveError(w, err)
		return
	}
	slog.Info("passkey added", "username", name, "name", p.Name)
	a.note(r, name, "passkey-add", p.Name)
	writeJSON(w, http.StatusCreated, p.info())
}

func (a *Auth) deletePasskey(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	name := Username(r)
	id := r.PathValue("id")
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.passkeys[id]
	if !ok || p.User != name {
		writeError(w, http.StatusNotFound, "Passkey nicht gefunden")
		return
	}
	delete(a.passkeys, id)
	if err := a.save(); err != nil {
		a.passkeys[id] = p
		a.saveError(w, err)
		return
	}
	slog.Info("passkey removed", "username", name, "name", p.Name)
	a.note(r, name, "passkey-remove", p.Name)
	writeJSON(w, http.StatusOK, map[string]string{})
}

// passkeyCount is how many passkeys the account name has. The caller holds
// mu.
func (a *Auth) passkeyCount(name string) int {
	n := 0
	for _, p := range a.passkeys {
		if p.User == name {
			n++
		}
	}
	return n
}

// dropPasskeys removes all passkeys of name and returns them. The caller
// holds mu.
func (a *Auth) dropPasskeys(name string) map[string]Passkey {
	dropped := map[string]Passkey{}
	for id, p := range a.passkeys {
		if p.User == name {
			dropped[id] = p
			delete(a.passkeys, id)
		}
	}
	return dropped
}
