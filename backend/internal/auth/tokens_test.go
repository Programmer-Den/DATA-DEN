package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// tokenSetup returns a server with a route that echoes the user, as file
// handlers use Require.
func tokenSetup(t *testing.T, file string, c *clock) *http.ServeMux {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("richtig"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Username: "chriszly", PasswordHash: string(hash), AccountsFile: file, Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /api/whoami", a.Require(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(Username(r)))
	}))
	mux.HandleFunc("POST /api/whoami", a.Require(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(Username(r)))
	}))
	return mux
}

func createToken(t *testing.T, mux http.Handler, c *http.Cookie, body string) (string, string) {
	t.Helper()
	code, answer := call(mux, c, http.MethodPost, "/api/account/tokens", body)
	if code != http.StatusCreated {
		t.Fatalf("create token: status %d, %v", code, answer)
	}
	token, _ := answer["token"].(string)
	info, _ := answer["info"].(map[string]any)
	id, _ := info["id"].(string)
	if !strings.HasPrefix(token, "dd_") || len(token) != 46 || id == "" {
		t.Fatalf("token %q, id %q", token, id)
	}
	return token, id
}

func withToken(mux http.Handler, method, target, body string, set func(*http.Request)) (int, string) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.7:1"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	set(req)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func basic(user, token string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(user, token) }
}

func TestTokenUseAndRevoke(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := tokenSetup(t, file, c)
	owner := loginAs(t, mux, "chriszly", "richtig")

	if code, _ := call(mux, owner, http.MethodPost, "/api/account/tokens", `{"name":"Skript","password":"falsch"}`); code != http.StatusForbidden {
		t.Errorf("wrong password: status %d, want 403", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/tokens", `{"name":" ","password":"richtig"}`); code != http.StatusBadRequest {
		t.Errorf("empty name: status %d, want 400", code)
	}
	token, id := createToken(t, mux, owner, `{"name":"Backup-Skript","password":"richtig"}`)

	if code, body := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(token)); code != http.StatusOK || body != "chriszly" {
		t.Errorf("bearer: status %d, body %q", code, body)
	}
	if code, body := withToken(mux, http.MethodPost, "/api/whoami", "{}", basic("chriszly", token)); code != http.StatusOK || body != "chriszly" {
		t.Errorf("basic: status %d, body %q", code, body)
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", basic("someone", token)); code != http.StatusUnauthorized {
		t.Errorf("basic with another username: status %d, want 401", code)
	}
	// The account's own password is no app password.
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", basic("chriszly", "richtig")); code != http.StatusUnauthorized {
		t.Errorf("basic with the password: status %d, want 401", code)
	}
	// Account settings stay with the browser login.
	for _, target := range []string{"/api/account/tokens", "/api/account/2fa", "/api/users"} {
		if code, _ := withToken(mux, http.MethodGet, target, "", bearer(token)); code != http.StatusForbidden {
			t.Errorf("%s with a token: status %d, want 403", target, code)
		}
	}

	code, answer := call(mux, owner, http.MethodGet, "/api/account/tokens", "")
	list, _ := answer["tokens"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list: status %d, %v", code, answer)
	}
	entry := list[0].(map[string]any)
	if entry["name"] != "Backup-Skript" || entry["lastUsed"] == nil || entry["hash"] != nil {
		t.Errorf("list entry %v", entry)
	}
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Error("the token itself is in the accounts file")
	}

	if code, _ := call(mux, owner, http.MethodPost, "/api/account/tokens/"+id+"/delete", "{}"); code != http.StatusOK {
		t.Fatalf("revoke: status %d", code)
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(token)); code != http.StatusUnauthorized {
		t.Errorf("revoked token: status %d, want 401", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/tokens/"+id+"/delete", "{}"); code != http.StatusNotFound {
		t.Errorf("revoke again: status %d, want 404", code)
	}
}

func TestTokenReadOnlyAndExpiry(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := tokenSetup(t, "", c)
	owner := loginAs(t, mux, "chriszly", "richtig")
	token, _ := createToken(t, mux, owner, `{"name":"Lesen","password":"richtig","readOnly":true,"expiresDays":2}`)

	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(token)); code != http.StatusOK {
		t.Errorf("read: status %d, want 200", code)
	}
	if code, _ := withToken(mux, http.MethodPost, "/api/whoami", "{}", bearer(token)); code != http.StatusForbidden {
		t.Errorf("write with a read-only token: status %d, want 403", code)
	}
	c.now = c.now.Add(48 * time.Hour)
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(token)); code != http.StatusUnauthorized {
		t.Errorf("expired: status %d, want 401", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/tokens", `{"name":"x","password":"richtig","expiresDays":4000}`); code != http.StatusBadRequest {
		t.Errorf("too long: status %d, want 400", code)
	}
}

func TestWrongTokensLockOut(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := tokenSetup(t, "", c)
	for i := range MaxFailures {
		if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer("dd_wrong")); code != http.StatusUnauthorized {
			t.Fatalf("try %d: status %d, want 401", i, code)
		}
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer("dd_wrong")); code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: status %d, want 429", MaxFailures, code)
	}
}

func TestTokensSurviveRestartAndGoWithTheAccount(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := tokenSetup(t, file, c)
	owner := loginAs(t, mux, "chriszly", "richtig")
	if code, _ := call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort"}`); code != http.StatusCreated {
		t.Fatalf("create account: status %d", code)
	}
	anna := loginAs(t, mux, "anna", "anna-passwort")
	annaToken, _ := createToken(t, mux, anna, `{"name":"Handy","password":"anna-passwort"}`)
	ownerToken, _ := createToken(t, mux, owner, `{"name":"NAS","password":"richtig"}`)

	mux = tokenSetup(t, file, c)
	if code, body := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(annaToken)); code != http.StatusOK || body != "anna" {
		t.Fatalf("after a restart: status %d, body %q", code, body)
	}
	owner = loginAs(t, mux, "chriszly", "richtig")
	code, answer := call(mux, owner, http.MethodGet, "/api/users", "")
	if users, _ := answer["users"].([]any); code != http.StatusOK || len(users) != 2 || users[1].(map[string]any)["tokens"] != 1.0 {
		t.Errorf("users: status %d, %v", code, answer)
	}

	// An admin revokes them all, say for a lost phone.
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna", `{"tokens":false}`); code != http.StatusOK {
		t.Fatalf("revoke all: status %d", code)
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(annaToken)); code != http.StatusUnauthorized {
		t.Errorf("after revoking all: status %d, want 401", code)
	}

	anna = loginAs(t, mux, "anna", "anna-passwort")
	annaToken, _ = createToken(t, mux, anna, `{"name":"Handy","password":"anna-passwort"}`)
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna/delete", "{}"); code != http.StatusOK {
		t.Fatalf("delete account: status %d", code)
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(annaToken)); code != http.StatusUnauthorized {
		t.Errorf("token of a deleted account: status %d, want 401", code)
	}
	if code, _ := withToken(mux, http.MethodGet, "/api/whoami", "", bearer(ownerToken)); code != http.StatusOK {
		t.Errorf("owner's token: status %d, want 200", code)
	}
	var parsed accountsFile
	data, _ := os.ReadFile(filepath.Clean(file))
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Tokens) != 1 {
		t.Errorf("tokens in the file: %v, %v", parsed.Tokens, err)
	}
}

func TestRequireTokenForWebDAV(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	hash, err := bcrypt.GenerateFromPassword([]byte("richtig"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Username: "chriszly", PasswordHash: string(hash), Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("/dav/", a.RequireToken(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(Username(r)))
	}))
	owner := loginAs(t, mux, "chriszly", "richtig")
	token, _ := createToken(t, mux, owner, `{"name":"Laufwerk","password":"richtig","readOnly":true}`)

	req := httptest.NewRequest("PROPFIND", "/dav/", nil)
	req.AddCookie(owner)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("with a session only: status %d, WWW-Authenticate %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if code, body := withToken(mux, "PROPFIND", "/dav/", "", basic("chriszly", token)); code != http.StatusOK || body != "chriszly" {
		t.Errorf("PROPFIND with a read-only token: status %d, %q", code, body)
	}
	if code, _ := withToken(mux, http.MethodPut, "/dav/a.txt", "x", basic("chriszly", token)); code != http.StatusForbidden {
		t.Errorf("PUT with a read-only token: status %d, want 403", code)
	}
}
