package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Accounts besides the owner's, managed by admins:
//
//	GET  /api/users                 -> {"users": [...]}, admins only
//	POST /api/users {"name", "password", "admin", "quota"} -> 201, admins only
//	POST /api/users/{name} {"password"?, "admin"?, "quota"?, "twoFactor": false?} -> 200, admins only
//	POST /api/users/{name}/delete {} -> 200, admins only; the files are moved aside
//	POST /api/account/password {"current", "password"} -> 200, one's own password
//
// The owner's account comes from the settings and cannot be changed here.

// MinPasswordBytes is the shortest password a new or changed account may
// have.
const MinPasswordBytes = 10

// hashCost is bcrypt's cost for new hashes; tests lower it.
var hashCost = 12

// accountName is a name for a new account. It is also the name of the
// account's folder, so it is kept plain.
var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// Account is one login.
type Account struct {
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
	Admin        bool   `json:"admin,omitempty"`
	// Quota is how many bytes the account may store, 0 for no limit.
	Quota int64 `json:"quota,omitempty"`
}

// UserInfo is an account in the list for admins.
type UserInfo struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	Owner bool   `json:"owner"`
	Quota int64  `json:"quota"`
	// Used is how many bytes it stores, -1 when unknown.
	Used int64 `json:"used"`
	// TwoFactor is true when the login needs a code from an app.
	TwoFactor bool `json:"twoFactor"`
}

type accountsFile struct {
	Accounts  []Account            `json:"accounts"`
	TwoFactor map[string]TwoFactor `json:"twoFactor,omitempty"`
}

type storedAccounts struct {
	accounts  map[string]Account
	twoFactor map[string]TwoFactor
}

// loadAccounts reads file, if there is one. An account named like the owner
// is left out: the settings decide about the owner.
func loadAccounts(file, owner string) (storedAccounts, error) {
	stored := storedAccounts{accounts: map[string]Account{}, twoFactor: map[string]TwoFactor{}}
	if file == "" {
		return stored, nil
	}
	data, err := os.ReadFile(filepath.Clean(file))
	if errors.Is(err, fs.ErrNotExist) {
		return stored, nil
	}
	if err != nil {
		return stored, err
	}
	var parsed accountsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return stored, errors.New(file + " is not valid JSON: " + err.Error())
	}
	for _, account := range parsed.Accounts {
		if account.Name == owner || !accountName.MatchString(account.Name) {
			slog.Warn("account left out", "username", account.Name)
			continue
		}
		stored.accounts[account.Name] = account
	}
	for name, tf := range parsed.TwoFactor {
		if _, ok := stored.accounts[name]; ok || name == owner {
			stored.twoFactor[name] = tf
		}
	}
	return stored, nil
}

// ResetTwoFactor turns the second factor of the account name off in file,
// for an owner who lost both the phone and the recovery codes. The server
// must not run meanwhile, or it writes its own state back.
func ResetTwoFactor(file, name string) error {
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return err
	}
	var parsed accountsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return errors.New(file + " is not valid JSON: " + err.Error())
	}
	if _, ok := parsed.TwoFactor[name]; !ok {
		return errors.New(name + " has no two-factor login")
	}
	delete(parsed.TwoFactor, name)
	return writeAccounts(file, parsed)
}

func writeAccounts(file string, stored accountsFile) error {
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// save writes the accounts to the file, in one step. The caller holds mu.
func (a *Auth) save() error {
	if a.cfg.AccountsFile == "" {
		return nil
	}
	stored := accountsFile{Accounts: []Account{}, TwoFactor: a.twoFactor}
	for _, account := range a.accounts {
		stored.Accounts = append(stored.Accounts, account)
	}
	slices.SortFunc(stored.Accounts, func(x, y Account) int { return strings.Compare(x.Name, y.Name) })
	return writeAccounts(a.cfg.AccountsFile, stored)
}

func (a *Auth) registerAccounts(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account/password", a.Require(a.changeOwnPassword))
	mux.HandleFunc("GET /api/users", a.RequireAdmin(a.listUsers))
	mux.HandleFunc("POST /api/users", a.RequireAdmin(a.createUser))
	mux.HandleFunc("POST /api/users/{name}", a.RequireAdmin(a.updateUser))
	mux.HandleFunc("POST /api/users/{name}/delete", a.RequireAdmin(a.deleteUser))
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

// checkNewPassword answers the request itself and returns false when
// password is too short or too long.
func checkNewPassword(w http.ResponseWriter, password string) bool {
	if len(password) < MinPasswordBytes {
		writeError(w, http.StatusBadRequest, "Das Passwort braucht mindestens 10 Zeichen")
		return false
	}
	if len(password) > MaxPasswordBytes {
		writeError(w, http.StatusBadRequest, "Das Passwort darf höchstens 72 Bytes lang sein")
		return false
	}
	return true
}

func (a *Auth) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := Username(r)
	if name == a.cfg.Username {
		writeError(w, http.StatusBadRequest, "Das Passwort des Hauptkontos steht in DATA_DEN_PASSWORD_HASH")
		return
	}
	if !checkNewPassword(w, body.Password) {
		return
	}
	if _, ok := a.checkPassword(name, body.Current); !ok {
		writeError(w, http.StatusForbidden, "Das bisherige Passwort stimmt nicht")
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	keep := ""
	if c, err := r.Cookie(CookieName); err == nil {
		keep = c.Value
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account, ok := a.accounts[name]
	if !ok {
		writeError(w, http.StatusNotFound, "Konto nicht gefunden")
		return
	}
	account.PasswordHash = hash
	a.accounts[name] = account
	if err := a.save(); err != nil {
		a.saveError(w, err)
		return
	}
	// Other devices have to log in again with the new password.
	a.dropSessions(name, keep)
	slog.Info("password changed", "username", name)
	a.note(r, name, "password", "")
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (a *Auth) listUsers(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	_, ownerTF := a.twoFactor[a.cfg.Username]
	users := []UserInfo{{Name: a.cfg.Username, Admin: true, Owner: true, TwoFactor: ownerTF}}
	for _, account := range a.accounts {
		_, tf := a.twoFactor[account.Name]
		users = append(users, UserInfo{Name: account.Name, Admin: account.Admin, Quota: account.Quota, TwoFactor: tf})
	}
	a.mu.Unlock()
	for i := range users {
		users[i].Used = -1
		if a.cfg.Usage != nil {
			if used, err := a.cfg.Usage(users[i].Name); err == nil {
				users[i].Used = used
			}
		}
	}
	slices.SortFunc(users[1:], func(x, y UserInfo) int { return strings.Compare(x.Name, y.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (a *Auth) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Admin    bool   `json:"admin"`
		Quota    int64  `json:"quota"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !accountName.MatchString(body.Name) {
		writeError(w, http.StatusBadRequest, "Benutzernamen: 1 bis 32 Zeichen, nur a-z, 0-9, Punkt, Bindestrich und Unterstrich")
		return
	}
	if body.Quota < 0 {
		writeError(w, http.StatusBadRequest, "Ungültiges Kontingent")
		return
	}
	if !checkNewPassword(w, body.Password) {
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, taken := a.accounts[body.Name]; taken || body.Name == a.cfg.Username {
		writeError(w, http.StatusConflict, "Den Benutzernamen gibt es schon")
		return
	}
	a.accounts[body.Name] = Account{Name: body.Name, PasswordHash: hash, Admin: body.Admin, Quota: body.Quota}
	if err := a.save(); err != nil {
		delete(a.accounts, body.Name)
		a.saveError(w, err)
		return
	}
	slog.Info("account created", "username", body.Name, "admin", body.Admin, "quota", body.Quota, "by", Username(r))
	a.note(r, Username(r), "user-create", body.Name)
	writeJSON(w, http.StatusCreated, UserInfo{Name: body.Name, Admin: body.Admin, Quota: body.Quota})
}

// otherAccount is the account a request names in its path. It answers the
// request itself and returns false when that is not one an admin may change.
func (a *Auth) otherAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if name == a.cfg.Username {
		writeError(w, http.StatusBadRequest, "Das Hauptkonto wird in den Einstellungen des Servers eingerichtet")
		return "", false
	}
	return name, true
}

func (a *Auth) updateUser(w http.ResponseWriter, r *http.Request) {
	name, ok := a.otherAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Password *string `json:"password"`
		Admin    *bool   `json:"admin"`
		Quota    *int64  `json:"quota"`
		// TwoFactor false turns the second factor off, for a lost phone.
		TwoFactor *bool `json:"twoFactor"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.TwoFactor != nil && *body.TwoFactor {
		writeError(w, http.StatusBadRequest, "Die Zwei-Faktor-Anmeldung richtet jeder selbst ein")
		return
	}
	if body.Quota != nil && *body.Quota < 0 {
		writeError(w, http.StatusBadRequest, "Ungültiges Kontingent")
		return
	}
	var hash string
	if body.Password != nil {
		if !checkNewPassword(w, *body.Password) {
			return
		}
		var err error
		if hash, err = HashPassword(*body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account, ok := a.accounts[name]
	if !ok {
		writeError(w, http.StatusNotFound, "Konto nicht gefunden")
		return
	}
	before := account
	if hash != "" {
		account.PasswordHash = hash
	}
	if body.Admin != nil {
		account.Admin = *body.Admin
	}
	if body.Quota != nil {
		account.Quota = *body.Quota
	}
	a.accounts[name] = account
	if err := a.save(); err != nil {
		a.accounts[name] = before
		a.saveError(w, err)
		return
	}
	if hash != "" {
		// The old password no longer lets anyone in.
		a.dropSessions(name, "")
	}
	resetTF := body.TwoFactor != nil
	if resetTF && !a.removeTwoFactor(w, name) {
		return
	}
	slog.Info("account changed", "username", name, "admin", account.Admin, "quota", account.Quota, "password", hash != "", "twoFactorOff", resetTF, "by", Username(r))
	a.note(r, Username(r), "user-change", name)
	if resetTF {
		a.note(r, Username(r), "2fa-reset", name)
	}
	_, tf := a.twoFactor[name]
	writeJSON(w, http.StatusOK, UserInfo{Name: name, Admin: account.Admin, Quota: account.Quota, TwoFactor: tf})
}

func (a *Auth) deleteUser(w http.ResponseWriter, r *http.Request) {
	name, ok := a.otherAccount(w, r)
	if !ok {
		return
	}
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	if name == Username(r) {
		writeError(w, http.StatusBadRequest, "Das eigene Konto lässt sich nicht löschen")
		return
	}
	if _, ok := a.account(name); !ok {
		writeError(w, http.StatusNotFound, "Konto nicht gefunden")
		return
	}
	if a.cfg.Removed != nil {
		if err := a.cfg.Removed(name); err != nil {
			slog.Error("moving the files of a deleted account aside", "username", name, "err", err)
			writeError(w, http.StatusInternalServerError, "Die Dateien des Kontos ließen sich nicht beiseitelegen")
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	account := a.accounts[name]
	tf, hadTF := a.twoFactor[name]
	delete(a.accounts, name)
	delete(a.twoFactor, name)
	if err := a.save(); err != nil {
		a.accounts[name] = account
		if hadTF {
			a.twoFactor[name] = tf
		}
		a.saveError(w, err)
		return
	}
	a.dropSessions(name, "")
	slog.Info("account deleted", "username", name, "by", Username(r))
	a.note(r, Username(r), "user-delete", name)
	writeJSON(w, http.StatusOK, map[string]string{})
}

func (a *Auth) saveError(w http.ResponseWriter, err error) {
	slog.Error("saving the accounts", "err", err)
	writeError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen")
}
