package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func init() {
	hashCost = bcrypt.MinCost
}

// accountsSetup is like setup, with the accounts kept in file.
func accountsSetup(t *testing.T, file string, removed func(string) error) *http.ServeMux {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("richtig"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{
		Username: "chriszly", PasswordHash: string(hash), AccountsFile: file, Removed: removed,
		Usage: func(name string) (int64, error) { return int64(len(name)), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	return mux
}

func call(mux http.Handler, c *http.Cookie, method, target, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var answer map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	return rec.Code, answer
}

func loginAs(t *testing.T, mux http.Handler, username, password string) *http.Cookie {
	t.Helper()
	rec := login(mux, "192.0.2.1:1", username, password, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s: status %d, body %s", username, rec.Code, rec.Body)
	}
	return sessionCookie(t, rec)
}

func TestCreateAndUseAccount(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts", "accounts.json")
	mux := accountsSetup(t, file, nil)
	owner := loginAs(t, mux, "chriszly", "richtig")

	code, _ := call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort","quota":1000}`)
	if code != http.StatusCreated {
		t.Fatalf("create: status %d", code)
	}
	anna := loginAs(t, mux, "anna", "anna-passwort")
	if code, answer := call(mux, anna, http.MethodGet, "/api/session", ""); code != http.StatusOK || answer["username"] != "anna" || answer["admin"] != false {
		t.Errorf("anna's session: %d %v", code, answer)
	}
	if code, _ := call(mux, anna, http.MethodGet, "/api/users", ""); code != http.StatusForbidden {
		t.Errorf("list of accounts for a non-admin: status %d, want 403", code)
	}
	if code, _ := call(mux, anna, http.MethodPost, "/api/users", `{"name":"evil","password":"evil-passwort","admin":true}`); code != http.StatusForbidden {
		t.Errorf("create by a non-admin: status %d, want 403", code)
	}

	code, answer := call(mux, owner, http.MethodGet, "/api/users", "")
	users, _ := answer["users"].([]any)
	if code != http.StatusOK || len(users) != 2 {
		t.Fatalf("list: %d %v", code, answer)
	}
	first, second := users[0].(map[string]any), users[1].(map[string]any)
	if first["name"] != "chriszly" || first["owner"] != true || second["name"] != "anna" || second["quota"] != float64(1000) || second["used"] != float64(4) {
		t.Errorf("list: %v", users)
	}
	if strings.Contains(fmtJSON(answer), "passwordHash") {
		t.Error("the list shows password hashes")
	}

	// The file keeps the account over a restart, readable only by the server.
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file: %v, %v", info, err)
	}
	again := accountsSetup(t, file, nil)
	loginAs(t, again, "anna", "anna-passwort")
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCreateAccountRefuses(t *testing.T) {
	mux := accountsSetup(t, "", nil)
	owner := loginAs(t, mux, "chriszly", "richtig")
	for _, body := range []string{
		`{"name":"chriszly","password":"lang-genug-1"}`,
		`{"name":"Anna","password":"lang-genug-1"}`,
		`{"name":"../x","password":"lang-genug-1"}`,
		`{"name":".data-den-x","password":"lang-genug-1"}`,
		`{"name":"anna","password":"kurz"}`,
		`{"name":"anna","password":"lang-genug-1","quota":-1}`,
	} {
		if code, _ := call(mux, owner, http.MethodPost, "/api/users", body); code == http.StatusCreated {
			t.Errorf("%s was accepted", body)
		}
	}
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"lang-genug-1"}`)
	if code, _ := call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"lang-genug-2"}`); code != http.StatusConflict {
		t.Errorf("same name twice: status %d, want 409", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader("name=x&password=lang-genug-1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(owner)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: status %d, want 415", rec.Code)
	}
}

func TestUpdateAccount(t *testing.T) {
	mux := accountsSetup(t, "", nil)
	owner := loginAs(t, mux, "chriszly", "richtig")
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort"}`)
	anna := loginAs(t, mux, "anna", "anna-passwort")

	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna", `{"admin":true,"quota":5}`); code != http.StatusOK {
		t.Fatalf("update: status %d", code)
	}
	if _, answer := call(mux, anna, http.MethodGet, "/api/session", ""); answer["admin"] != true {
		t.Errorf("anna is no admin after the change: %v", answer)
	}
	// A new password logs anna out everywhere.
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna", `{"password":"neues-passwort"}`); code != http.StatusOK {
		t.Fatalf("new password: status %d", code)
	}
	if code := getSession(mux, anna); code != http.StatusUnauthorized {
		t.Errorf("anna's old session: status %d, want 401", code)
	}
	loginAs(t, mux, "anna", "neues-passwort")

	if code, _ := call(mux, owner, http.MethodPost, "/api/users/chriszly", `{"quota":5}`); code != http.StatusBadRequest {
		t.Errorf("changing the owner: status %d, want 400", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/nobody", `{"quota":5}`); code != http.StatusNotFound {
		t.Errorf("unknown account: status %d, want 404", code)
	}
}

func TestDeleteAccount(t *testing.T) {
	var removed []string
	mux := accountsSetup(t, "", func(name string) error {
		removed = append(removed, name)
		if name == "stuck" {
			return errors.New("disk full")
		}
		return nil
	})
	owner := loginAs(t, mux, "chriszly", "richtig")
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort","admin":true}`)
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"stuck","password":"stuck-passwort"}`)
	anna := loginAs(t, mux, "anna", "anna-passwort")

	if code, _ := call(mux, anna, http.MethodPost, "/api/users/anna/delete", `{}`); code != http.StatusBadRequest {
		t.Errorf("deleting oneself: status %d, want 400", code)
	}
	if code, _ := call(mux, anna, http.MethodPost, "/api/users/chriszly/delete", `{}`); code != http.StatusBadRequest {
		t.Errorf("deleting the owner: status %d, want 400", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna/delete", `{}`); code != http.StatusOK {
		t.Fatalf("delete: status %d", code)
	}
	if code := getSession(mux, anna); code != http.StatusUnauthorized {
		t.Errorf("deleted account's session: status %d, want 401", code)
	}
	if rec := login(mux, "192.0.2.2:1", "anna", "anna-passwort", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted account's login: status %d, want 401", rec.Code)
	}
	// When the files cannot be moved aside, the account stays.
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/stuck/delete", `{}`); code != http.StatusInternalServerError {
		t.Errorf("delete with failing files: status %d, want 500", code)
	}
	loginAs(t, mux, "stuck", "stuck-passwort")
	if strings.Join(removed, ",") != "anna,stuck" {
		t.Errorf("removed %v", removed)
	}
}

func TestChangeOwnPassword(t *testing.T) {
	mux := accountsSetup(t, "", nil)
	owner := loginAs(t, mux, "chriszly", "richtig")
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort"}`)
	anna := loginAs(t, mux, "anna", "anna-passwort")
	other := loginAs(t, mux, "anna", "anna-passwort")

	if code, _ := call(mux, anna, http.MethodPost, "/api/account/password", `{"current":"falsch","password":"neues-passwort"}`); code != http.StatusForbidden {
		t.Errorf("wrong current password: status %d, want 403", code)
	}
	if code, _ := call(mux, anna, http.MethodPost, "/api/account/password", `{"current":"anna-passwort","password":"neues-passwort"}`); code != http.StatusOK {
		t.Fatalf("change: status %d", code)
	}
	if code := getSession(mux, anna); code != http.StatusOK {
		t.Errorf("the session that changed it: status %d, want 200", code)
	}
	if code := getSession(mux, other); code != http.StatusUnauthorized {
		t.Errorf("another session: status %d, want 401", code)
	}
	loginAs(t, mux, "anna", "neues-passwort")
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/password", `{"current":"richtig","password":"neues-passwort"}`); code != http.StatusBadRequest {
		t.Errorf("owner: status %d, want 400", code)
	}
}

func TestBrokenAccountsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{AccountsFile: file}); err == nil {
		t.Error("a broken accounts file was accepted")
	}
}
