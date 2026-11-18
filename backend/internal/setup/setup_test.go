package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
)

func newSetup(t *testing.T) (*Setup, *http.ServeMux, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		OwnerFile:    filepath.Join(dir, ".data-den-accounts", "owner.json"),
		CodeFile:     filepath.Join(dir, ".data-den-accounts", "setup-code"),
		DataDir:      dir,
		HTTPS:        true,
		TrustedProxy: "192.168.1.20",
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	return s, mux, cfg
}

func post(mux http.Handler, target, body string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var answer map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	return rec.Code, answer
}

func codeFile(t *testing.T, cfg Config) string {
	t.Helper()
	data, err := os.ReadFile(cfg.CodeFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestCodeFile(t *testing.T) {
	s, _, cfg := newSetup(t)
	code := codeFile(t, cfg)
	if code != s.code || len(code) != 14 || strings.Count(code, "-") != 2 {
		t.Errorf("code %q in the file, %q in the server", code, s.code)
	}
	if info, err := os.Stat(cfg.CodeFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v, want 0600", info.Mode(), err)
	}
}

func TestCheck(t *testing.T) {
	_, mux, cfg := newSetup(t)
	if status, _ := post(mux, "/api/setup/check", `{"code":"AAAA-AAAA-AAAA"}`); status != http.StatusForbidden {
		t.Errorf("wrong code: status %d, want 403", status)
	}
	// Typed in lower case and with spaces, it still fits.
	code := " " + strings.ToLower(codeFile(t, cfg)) + " "
	status, checks := post(mux, "/api/setup/check", `{"code":"`+code+`"}`)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if checks["writable"] != true || checks["https"] != true || checks["trustedProxy"] != "192.168.1.20" || checks["dataDir"] != cfg.DataDir {
		t.Errorf("checks %v", checks)
	}
}

func TestCheckNotWritable(t *testing.T) {
	s, mux, cfg := newSetup(t)
	s.cfg.DataDir = filepath.Join(cfg.DataDir, "missing")
	status, checks := post(mux, "/api/setup/check", `{"code":"`+codeFile(t, cfg)+`"}`)
	if status != http.StatusOK || checks["writable"] != false || checks["writeError"] == "" {
		t.Errorf("status %d, checks %v", status, checks)
	}
	status, _ = post(mux, "/api/setup", `{"code":"`+codeFile(t, cfg)+`","username":"chriszly","password":"ein-langes-passwort"}`)
	if status != http.StatusServiceUnavailable {
		t.Errorf("create: status %d, want 503", status)
	}
}

func TestCreate(t *testing.T) {
	s, mux, cfg := newSetup(t)
	code := codeFile(t, cfg)
	for _, body := range []string{
		`{"code":"AAAA-AAAA-AAAA","username":"chriszly","password":"ein-langes-passwort"}`,
		`{"code":"` + code + `","username":"Chriszly","password":"ein-langes-passwort"}`,
		`{"code":"` + code + `","username":"chriszly","password":"kurz"}`,
		`{"code":"` + code + `","username":"chriszly","password":"` + strings.Repeat("x", 73) + `"}`,
	} {
		if status, _ := post(mux, "/api/setup", body); status == http.StatusCreated {
			t.Errorf("%s: created", body)
		}
	}
	select {
	case <-s.Done():
		t.Fatal("done before the account was created")
	default:
	}

	status, answer := post(mux, "/api/setup", `{"code":"`+code+`","username":"chriszly","password":"ein-langes-passwort"}`)
	if status != http.StatusCreated || answer["username"] != "chriszly" {
		t.Fatalf("status %d, answer %v", status, answer)
	}
	owner := <-s.Done()
	if owner.Name != "chriszly" || owner.PasswordHash == "" {
		t.Errorf("owner %+v", owner)
	}
	name, hash, err := auth.LoadOwner(cfg.OwnerFile)
	if err != nil || name != "chriszly" || hash != owner.PasswordHash {
		t.Errorf("saved %q %q %v", name, hash, err)
	}
	if _, err := os.Stat(cfg.CodeFile); !os.IsNotExist(err) {
		t.Errorf("the code file is still there: %v", err)
	}
	if status, _ := post(mux, "/api/setup", `{"code":"`+code+`","username":"other","password":"ein-langes-passwort"}`); status != http.StatusConflict {
		t.Errorf("second time: status %d, want 409", status)
	}
}

func TestWrongCodesKeepIt(t *testing.T) {
	delay := wrongCodeDelay
	wrongCodeDelay = 0
	t.Cleanup(func() { wrongCodeDelay = delay })
	_, mux, cfg := newSetup(t)
	code := codeFile(t, cfg)
	for range 50 {
		if status, _ := post(mux, "/api/setup/check", `{"code":"AAAA-AAAA-AAAA"}`); status != http.StatusForbidden {
			t.Fatalf("wrong code: status %d, want 403", status)
		}
	}
	if codeFile(t, cfg) != code {
		t.Error("wrong codes replaced the code")
	}
	if status, _ := post(mux, "/api/setup/check", `{"code":"`+code+`"}`); status != http.StatusOK {
		t.Errorf("the code after wrong ones: status %d, want 200", status)
	}
}

func TestNeedsJSON(t *testing.T) {
	_, mux, _ := newSetup(t)
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader("code=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status %d, want 415", rec.Code)
	}
}
