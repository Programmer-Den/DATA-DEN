package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOwner(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "owner.json")
	if name, hash, err := LoadOwner(file); err != nil || name != "" || hash != "" {
		t.Errorf("no file: %q %q %v, want nothing", name, hash, err)
	}
	if err := SaveOwner(file, "chriszly", "$2a$04$hash"); err != nil {
		t.Fatal(err)
	}
	if name, hash, err := LoadOwner(file); err != nil || name != "chriszly" || hash != "$2a$04$hash" {
		t.Errorf("saved: %q %q %v", name, hash, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v, want 0600", info.Mode(), err)
	}
	for _, broken := range []string{"{", `{"name":"Not Valid","passwordHash":"x"}`, `{"name":"chriszly"}`} {
		if err := os.WriteFile(file, []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadOwner(file); err == nil {
			t.Errorf("%s: no error", broken)
		}
	}
}

func TestOwnerFromFileChangesPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "owner.json")
	mux := accountsSetupWith(t, Config{OwnerFile: file})
	owner := loginAs(t, mux, "chriszly", "richtig")
	other := loginAs(t, mux, "chriszly", "richtig")
	if _, session := call(mux, owner, http.MethodGet, "/api/session", ""); session["fixedPassword"] != nil {
		t.Errorf("session %v, want no fixedPassword", session)
	}

	if code, _ := call(mux, owner, http.MethodPost, "/api/account/password", `{"current":"falsch","password":"neues-passwort"}`); code != http.StatusForbidden {
		t.Errorf("wrong current password: status %d, want 403", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/password", `{"current":"richtig","password":"neues-passwort"}`); code != http.StatusOK {
		t.Fatalf("change: status %d", code)
	}
	if code := getSession(mux, other); code != http.StatusUnauthorized {
		t.Errorf("another session: status %d, want 401", code)
	}
	loginAs(t, mux, "chriszly", "neues-passwort")
	if name, hash, err := LoadOwner(file); err != nil || name != "chriszly" || hash == "" {
		t.Errorf("file: %q %q %v", name, hash, err)
	}
}

func TestOwnerFromSettingsHasFixedPassword(t *testing.T) {
	mux := accountsSetup(t, "", nil)
	owner := loginAs(t, mux, "chriszly", "richtig")
	if _, session := call(mux, owner, http.MethodGet, "/api/session", ""); session["fixedPassword"] != true {
		t.Errorf("session %v, want fixedPassword", session)
	}
}
