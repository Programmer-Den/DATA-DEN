package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTOTPCode(t *testing.T) {
	// The SHA-1 vectors of RFC 6238, cut to six digits.
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, unix/totpPeriod); got != want {
			t.Errorf("code at %d: %s, want %s", unix, got, want)
		}
	}
}

func TestMatchTOTP(t *testing.T) {
	secret := base32NoPad.EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(1111111109, 0)
	step := now.Unix() / totpPeriod
	if s, ok := matchTOTP(secret, "081804", now, 0); !ok || s != step {
		t.Errorf("current code: %d, %v", s, ok)
	}
	// A code from 30 seconds before still counts, one from 90 seconds before not.
	if _, ok := matchTOTP(secret, totpCode([]byte("12345678901234567890"), step-1), now, 0); !ok {
		t.Error("the previous code was refused")
	}
	if _, ok := matchTOTP(secret, totpCode([]byte("12345678901234567890"), step-3), now, 0); ok {
		t.Error("a code from 90 seconds before was accepted")
	}
	if _, ok := matchTOTP(secret, "081804", now, step); ok {
		t.Error("a code was accepted twice")
	}
	if _, ok := matchTOTP(secret, "", now, 0); ok {
		t.Error("an empty code was accepted")
	}
}

// enableTOTP turns the second factor on for the session c and returns the
// secret and the recovery codes.
func enableTOTP(t *testing.T, mux http.Handler, c *http.Cookie, now time.Time) ([]byte, []string) {
	t.Helper()
	code, answer := call(mux, c, http.MethodPost, "/api/account/2fa/setup", `{}`)
	if code != http.StatusOK {
		t.Fatalf("setup: status %d", code)
	}
	if !strings.HasPrefix(answer["qr"].(string), "data:image/png;base64,") || !strings.HasPrefix(answer["uri"].(string), "otpauth://totp/DATA-DEN:") {
		t.Errorf("setup: %v", answer)
	}
	secret, err := base32NoPad.DecodeString(answer["secret"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(mux, c, http.MethodPost, "/api/account/2fa/enable", `{"code":"000000x"}`); code != http.StatusBadRequest {
		t.Errorf("enable with a wrong code: status %d", code)
	}
	code, answer = call(mux, c, http.MethodPost, "/api/account/2fa/enable", `{"code":"`+totpCode(secret, now.Unix()/totpPeriod)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("enable: status %d", code)
	}
	var recovery []string
	for _, r := range answer["recovery"].([]any) {
		recovery = append(recovery, r.(string))
	}
	if len(recovery) != RecoveryCodes {
		t.Fatalf("%d recovery codes", len(recovery))
	}
	return secret, recovery
}

func loginWithCode(mux http.Handler, remote, username, password, code string) int {
	body := `{"username":"` + username + `","password":"` + password + `","code":"` + code + `"}`
	code2, _ := callFrom(mux, remote, body)
	return code2
}

func callFrom(mux http.Handler, remote, body string) (int, map[string]any) {
	return callReq(mux, remote, http.MethodPost, "/api/login", body)
}

func TestTwoFactorLogin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := accountsSetupClock(t, file, c)
	owner := loginAs(t, mux, "chriszly", "richtig")
	secret, recovery := enableTOTP(t, mux, owner, c.now)

	if _, answer := call(mux, owner, http.MethodGet, "/api/session", ""); answer["twoFactor"] != true {
		t.Errorf("session: %v", answer)
	}
	// Password alone: asks for the code, and is no failed login.
	for range MaxFailures + 1 {
		code, answer := callFrom(mux, "192.0.2.5:1", `{"username":"chriszly","password":"richtig"}`)
		if code != http.StatusUnauthorized || answer["twoFactor"] != true {
			t.Fatalf("without code: %d %v", code, answer)
		}
	}
	// The code used to turn it on is used up; the next one works once.
	c.now = c.now.Add(totpPeriod * time.Second)
	next := totpCode(secret, c.now.Unix()/totpPeriod)
	if code := loginWithCode(mux, "192.0.2.5:1", "chriszly", "falsch", next); code != http.StatusUnauthorized {
		t.Errorf("wrong password with code: status %d", code)
	}
	if code := loginWithCode(mux, "192.0.2.5:1", "chriszly", "richtig", next); code != http.StatusOK {
		t.Errorf("with code: status %d", code)
	}
	if code := loginWithCode(mux, "192.0.2.5:1", "chriszly", "richtig", next); code != http.StatusUnauthorized {
		t.Errorf("same code again: status %d, want 401", code)
	}
	// A recovery code works once, with or without its dash.
	plain := strings.ReplaceAll(strings.ToUpper(recovery[0]), "-", "")
	if code := loginWithCode(mux, "192.0.2.6:1", "chriszly", "richtig", plain); code != http.StatusOK {
		t.Errorf("recovery code: status %d", code)
	}
	if code := loginWithCode(mux, "192.0.2.6:1", "chriszly", "richtig", recovery[0]); code != http.StatusUnauthorized {
		t.Errorf("recovery code again: status %d, want 401", code)
	}
	if _, answer := call(mux, owner, http.MethodGet, "/api/account/2fa", ""); answer["enabled"] != true || answer["recoveryLeft"] != float64(RecoveryCodes-1) {
		t.Errorf("state: %v", answer)
	}

	// Wrong codes count towards the lockout.
	for range MaxFailures {
		loginWithCode(mux, "192.0.2.7:1", "chriszly", "richtig", "123456")
	}
	if code := loginWithCode(mux, "192.0.2.7:1", "chriszly", "richtig", recovery[1]); code != http.StatusTooManyRequests {
		t.Errorf("after %d wrong codes: status %d, want 429", MaxFailures, code)
	}

	// It survives a restart.
	again := accountsSetupClock(t, file, c)
	if code, _ := callFrom(again, "192.0.2.8:1", `{"username":"chriszly","password":"richtig"}`); code != http.StatusUnauthorized {
		t.Errorf("after restart without code: status %d", code)
	}
	info, _ := os.ReadFile(filepath.Clean(file))
	if strings.Contains(string(info), recovery[2]) {
		t.Error("a recovery code is stored in plain text")
	}
}

func TestDisableTwoFactor(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	mux := accountsSetupClock(t, "", c)
	owner := loginAs(t, mux, "chriszly", "richtig")
	_, recovery := enableTOTP(t, mux, owner, c.now)
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/2fa/setup", `{}`); code != http.StatusConflict {
		t.Errorf("setup while on: status %d, want 409", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/2fa/disable", `{"password":"falsch","code":"`+recovery[0]+`"}`); code != http.StatusForbidden {
		t.Errorf("wrong password: status %d", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/2fa/disable", `{"password":"richtig","code":"123456"}`); code != http.StatusForbidden {
		t.Errorf("wrong code: status %d", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/account/2fa/disable", `{"password":"richtig","code":"`+recovery[0]+`"}`); code != http.StatusOK {
		t.Fatalf("disable: status %d", code)
	}
	loginAs(t, mux, "chriszly", "richtig")
}

func TestAdminResetsTwoFactor(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	file := filepath.Join(t.TempDir(), "accounts.json")
	mux := accountsSetupClock(t, file, c)
	owner := loginAs(t, mux, "chriszly", "richtig")
	call(mux, owner, http.MethodPost, "/api/users", `{"name":"anna","password":"anna-passwort"}`)
	anna := loginAs(t, mux, "anna", "anna-passwort")
	enableTOTP(t, mux, anna, c.now)

	_, answer := call(mux, owner, http.MethodGet, "/api/users", "")
	if users := answer["users"].([]any); users[1].(map[string]any)["twoFactor"] != true {
		t.Errorf("list: %v", users)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna", `{"twoFactor":true}`); code != http.StatusBadRequest {
		t.Errorf("turning it on for someone else: status %d, want 400", code)
	}
	if code, _ := call(mux, owner, http.MethodPost, "/api/users/anna", `{"twoFactor":false}`); code != http.StatusOK {
		t.Fatalf("reset: status %d", code)
	}
	loginAs(t, mux, "anna", "anna-passwort")

	// The owner's is reset offline, with the server stopped.
	enableTOTP(t, mux, owner, c.now)
	if err := ResetTwoFactor(file, "chriszly"); err != nil {
		t.Fatal(err)
	}
	if err := ResetTwoFactor(file, "chriszly"); err == nil {
		t.Error("a second reset did not say there is nothing to reset")
	}
	loginAs(t, accountsSetupClock(t, file, c), "chriszly", "richtig")
}
