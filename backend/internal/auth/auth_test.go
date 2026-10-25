package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// setup returns a server with the account chriszly / "richtig".
func setup(t *testing.T, proxy string) (*http.ServeMux, *clock) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("richtig"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	a, err := New(Config{Username: "chriszly", PasswordHash: string(hash), TrustedProxy: proxy, Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	return mux, c
}

func login(mux http.Handler, remote, username, password string, header http.Header) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body)))
	req.RemoteAddr = remote
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func getSession(mux http.Handler, c *http.Cookie) int {
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoginAndLogout(t *testing.T) {
	mux, _ := setup(t, "")
	rec := login(mux, "192.0.2.1:1234", "chriszly", "richtig", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", rec.Code, rec.Body)
	}
	c := sessionCookie(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %+v, want HttpOnly, Secure and SameSite=Strict", c)
	}
	if code := getSession(mux, c); code != http.StatusOK {
		t.Fatalf("session after login: status %d", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(c)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if code := getSession(mux, c); code != http.StatusUnauthorized {
		t.Errorf("session after logout: status %d, want 401", code)
	}
}

func TestNoSession(t *testing.T) {
	mux, _ := setup(t, "")
	if code := getSession(mux, nil); code != http.StatusUnauthorized {
		t.Errorf("without cookie: status %d, want 401", code)
	}
	if code := getSession(mux, &http.Cookie{Name: CookieName, Value: "made-up", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}); code != http.StatusUnauthorized {
		t.Errorf("made-up cookie: status %d, want 401", code)
	}
}

func TestSessionExpires(t *testing.T) {
	mux, c := setup(t, "")
	cookie := sessionCookie(t, login(mux, "192.0.2.1:1", "chriszly", "richtig", nil))
	c.now = c.now.Add(SessionLifetime)
	if code := getSession(mux, cookie); code != http.StatusUnauthorized {
		t.Errorf("after %v: status %d, want 401", SessionLifetime, code)
	}
}

func TestWrongLogin(t *testing.T) {
	mux, _ := setup(t, "")
	tests := []struct{ username, password string }{
		{"chriszly", "falsch"},
		{"someone", "richtig"},
		{"", ""},
		{"chriszly", strings.Repeat("x", MaxPasswordBytes+1)},
	}
	for i, tt := range tests {
		// A different address each time, so the lockout does not kick in.
		rec := login(mux, fmt.Sprintf("198.51.100.%d:1", i+1), tt.username, tt.password, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%q/%q: status %d, want 401", tt.username, tt.password, rec.Code)
		}
	}
}

func TestLockout(t *testing.T) {
	mux, c := setup(t, "")
	for range MaxFailures {
		login(mux, "192.0.2.7:1", "chriszly", "falsch", nil)
	}
	rec := login(mux, "192.0.2.7:1", "chriszly", "richtig", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: status %d, Retry-After %q", MaxFailures, rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another client is not locked out.
	if rec := login(mux, "192.0.2.8:1", "chriszly", "richtig", nil); rec.Code != http.StatusOK {
		t.Errorf("other client: status %d, want 200", rec.Code)
	}
	c.now = c.now.Add(FailureWindow)
	if rec := login(mux, "192.0.2.7:1", "chriszly", "richtig", nil); rec.Code != http.StatusOK {
		t.Errorf("after the window: status %d, want 200", rec.Code)
	}
}

func TestLockoutBehindProxy(t *testing.T) {
	mux, _ := setup(t, "10.0.0.2")
	from := func(client string) http.Header {
		// The client's own made-up entry comes first, the proxy's last.
		return http.Header{"X-Forwarded-For": {"203.0.113.99, " + client}}
	}
	for range MaxFailures {
		login(mux, "10.0.0.2:4000", "chriszly", "falsch", from("203.0.113.1"))
	}
	if rec := login(mux, "10.0.0.2:4000", "chriszly", "richtig", from("203.0.113.1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("locked client: status %d, want 429", rec.Code)
	}
	if rec := login(mux, "10.0.0.2:4000", "chriszly", "richtig", from("203.0.113.2")); rec.Code != http.StatusOK {
		t.Errorf("other client through the proxy: status %d, want 200", rec.Code)
	}
}

func TestForwardedForIgnoredFromOthers(t *testing.T) {
	mux, _ := setup(t, "10.0.0.2")
	for range MaxFailures {
		// Not the proxy: a changing X-Forwarded-For must not dodge the lockout.
		login(mux, "192.0.2.9:1", "chriszly", "falsch", http.Header{"X-Forwarded-For": {"203.0.113.5"}})
	}
	rec := login(mux, "192.0.2.9:1", "chriszly", "richtig", http.Header{"X-Forwarded-For": {"203.0.113.6"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status %d, want 429", rec.Code)
	}
}

func TestLoginNeedsJSON(t *testing.T) {
	mux, _ := setup(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("username=chriszly&password=richtig"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: status %d, want 415", rec.Code)
	}
}

func TestNotConfigured(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	if rec := login(mux, "192.0.2.1:1", "", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

func TestBadConfig(t *testing.T) {
	if _, err := New(Config{Username: "chriszly", PasswordHash: "plain"}); err == nil {
		t.Error("a password that is not a bcrypt hash was accepted")
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)
	if _, err := New(Config{PasswordHash: string(hash)}); err == nil {
		t.Error("an empty username was accepted")
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("geheim")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("geheim")) != nil {
		t.Error("the hash does not match the password")
	}
	for _, bad := range []string{"", strings.Repeat("x", MaxPasswordBytes+1)} {
		if _, err := HashPassword(bad); err == nil {
			t.Errorf("HashPassword accepted a password of %d bytes", len(bad))
		}
	}
}
