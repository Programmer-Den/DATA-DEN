package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type userKey struct{}

// spacesSetup serves the data directory with Spaces; the account of a
// request is its "X-User" header, the owner is "chef", and "anna" may store
// 10 bytes.
func spacesSetup(t *testing.T) (*Spaces, http.Handler, string) {
	t.Helper()
	data := t.TempDir()
	s, err := OpenSpaces(data, SpacesConfig{
		Owner: "chef",
		User:  func(r *http.Request) string { name, _ := r.Context().Value(userKey{}).(string); return name },
		Quota: func(name string) int64 {
			if name == "anna" {
				return 10
			}
			return 0
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mux := http.NewServeMux()
	s.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	})
	return s, mux, data
}

func as(name string) http.Header { return http.Header{"X-User": {name}} }

func TestRoutesMatchRegister(t *testing.T) {
	f, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	f.serve()
	for _, route := range Routes {
		method, p, _ := strings.Cut(route, " ")
		p = strings.ReplaceAll(p, "{id}", "x")
		if _, pattern := f.mux.Handler(httptest.NewRequest(method, p, nil)); pattern != route {
			t.Errorf("%s is served by %q", route, pattern)
		}
	}
}

func TestSpacesAreSeparate(t *testing.T) {
	_, h, data := spacesSetup(t)
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/chef.txt","size":0}`, as("chef")); code != http.StatusCreated {
		t.Fatalf("owner's upload: status %d", code)
	}
	// The owner's files are the data directory itself, as before.
	if !exists(data, "chef.txt") {
		t.Fatal("the owner's file is not in the data directory")
	}

	body := `{"path":"/anna.txt","size":3}`
	code, answer := do(h, http.MethodPost, "/api/uploads", "application/json", body, as("anna"))
	if code != http.StatusCreated {
		t.Fatalf("anna's upload: status %d", code)
	}
	if code, _ := do(h, http.MethodPut, "/api/uploads/"+answer["id"].(string), "application/octet-stream", "abc",
		http.Header{"Upload-Offset": {"0"}, "X-User": {"anna"}}); code != http.StatusOK {
		t.Fatalf("anna's chunk: status %d", code)
	}
	if got := read(t, filepath.Join(data, usersDir, "anna", "anna.txt")); got != "abc" {
		t.Errorf("anna's file holds %q", got)
	}

	// Neither sees the other's files, nor the folder of the accounts.
	rec := get(h, "/api/files", "/", as("anna"))
	if l := listing(t, rec); len(l.Entries) != 1 || l.Entries[0].Name != "anna.txt" {
		t.Errorf("anna sees %+v", l.Entries)
	}
	rec = get(h, "/api/files", "/", as("chef"))
	if l := listing(t, rec); len(l.Entries) != 1 || l.Entries[0].Name != "chef.txt" {
		t.Errorf("the owner sees %+v", l.Entries)
	}
	if rec := get(h, "/api/download", "/"+usersDir+"/anna/anna.txt", as("chef")); rec.Code != http.StatusNotFound {
		t.Errorf("the owner downloading anna's file: status %d, want 404", rec.Code)
	}
	if rec := get(h, "/api/download", "/../chef.txt", as("anna")); rec.Code == http.StatusOK {
		t.Error("anna downloaded the owner's file")
	}
}

func TestQuota(t *testing.T) {
	s, h, _ := spacesSetup(t)
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/big","size":11}`, as("anna")); code != http.StatusInsufficientStorage {
		t.Errorf("11 bytes with a quota of 10: status %d, want 507", code)
	}
	// Unfinished uploads count as well.
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/a","size":6}`, as("anna")); code != http.StatusCreated {
		t.Fatalf("6 bytes: status %d", code)
	}
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/b","size":6}`, as("anna")); code != http.StatusInsufficientStorage {
		t.Errorf("6 more bytes: status %d, want 507", code)
	}
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/c","size":4}`, as("anna")); code != http.StatusCreated {
		t.Errorf("4 more bytes: status %d, want 201", code)
	}
	// No limit for the owner.
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"/big","size":1000}`, as("chef")); code != http.StatusCreated {
		t.Errorf("owner: status %d, want 201", code)
	}
	if used, err := s.Usage("anna"); err != nil || used != 0 {
		t.Errorf("anna's usage before any upload finished: %d, %v", used, err)
	}
}

func TestUsage(t *testing.T) {
	s, h, data := spacesSetup(t)
	dir := filepath.Join(data, usersDir, "bob")
	if err := os.MkdirAll(filepath.Join(dir, ".data-den-trash", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a": 5, ".data-den-trash/x/item": 7} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if used, err := s.Usage("bob"); err != nil || used != 12 {
		t.Errorf("bob's usage: %d, %v; want 12 with the recycle bin", used, err)
	}
	// The owner's usage leaves out the spaces of the others.
	if err := os.WriteFile(filepath.Join(data, "own"), make([]byte, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	if used, err := s.Usage("chef"); err != nil || used != 3 {
		t.Errorf("owner's usage: %d, %v; want 3", used, err)
	}
	code, answer := do(h, http.MethodGet, "/api/usage", "", "", as("anna"))
	if code != http.StatusOK || answer["quota"] != float64(10) || answer["used"] != float64(0) {
		t.Errorf("/api/usage: status %d, %v", code, answer)
	}
}

func TestRetire(t *testing.T) {
	s, h, data := spacesSetup(t)
	if code, _ := do(h, http.MethodPost, "/api/folders", "application/json", `{"path":"/Fotos"}`, as("bob")); code != http.StatusCreated {
		t.Fatalf("folder: status %d", code)
	}
	if err := s.Retire("bob"); err != nil {
		t.Fatal(err)
	}
	if exists(data, usersDir+"/bob") {
		t.Error("bob's folder is still in place")
	}
	moved, _ := filepath.Glob(filepath.Join(data, usersDir, ".deleted", "bob-*", "Fotos"))
	if len(moved) != 1 {
		t.Errorf("bob's files were not moved aside: %v", moved)
	}
	// A new bob starts empty.
	if l := listing(t, get(h, "/api/files", "/", as("bob"))); len(l.Entries) != 0 {
		t.Errorf("the new bob sees %+v", l.Entries)
	}
	if err := s.Retire("chef"); err == nil {
		t.Error("the owner's space was retired")
	}
}

func TestInvalidAccountName(t *testing.T) {
	_, h, _ := spacesSetup(t)
	for _, name := range []string{"", "..", "a/b", ".data-den-x"} {
		if rec := get(h, "/api/files", "/", as(name)); rec.Code != http.StatusInternalServerError {
			t.Errorf("account %q: status %d, want 500", name, rec.Code)
		}
	}
}

func TestSpacesTrashDaysAndRun(t *testing.T) {
	s, h, _ := spacesSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Run(ctx)
	if err := s.SetTrashDays(7); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTrashDays(0); err == nil {
		t.Error("0 days were accepted")
	}
	// A space opened after both gets the setting and its own search index.
	code, answer := do(h, http.MethodGet, "/api/trash", "", "", as("anna"))
	if code != http.StatusOK || answer["days"] != float64(7) {
		t.Errorf("anna's recycle bin: %d %v", code, answer)
	}
	if code, _ := do(h, http.MethodGet, "/api/search?q=x", "", "", as("anna")); code != http.StatusOK {
		t.Errorf("anna's search: status %d", code)
	}
	if err := s.Retire("anna"); err != nil {
		t.Fatal(err)
	}
}

type noGuard struct{}

func (noGuard) Wait(*http.Request) time.Duration { return 0 }
func (noGuard) Failed(*http.Request, string)     {}

func TestSharesPerSpace(t *testing.T) {
	s, _, data := spacesSetup(t)
	mux := http.NewServeMux()
	require := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	}
	s.Register(mux, require)
	s.RegisterShares(mux, require, noGuard{})
	for name, dir := range map[string]string{"chef": data, "anna": filepath.Join(data, usersDir, "anna")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	for _, name := range []string{"chef", "anna"} {
		code, answer := do(mux, http.MethodPost, "/api/shares", "application/json", `{"path":"/a.txt","days":7}`, as(name))
		if code != http.StatusCreated {
			t.Fatalf("%s's share: status %d %v", name, code, answer)
		}
		ids[name] = answer["id"].(string)
	}
	// Each sees only their own links.
	code, answer := do(mux, http.MethodGet, "/api/shares", "", "", as("anna"))
	if items, _ := answer["items"].([]any); code != http.StatusOK || len(items) != 1 {
		t.Errorf("anna's links: %d %v", code, answer)
	}
	// Each link serves the file of the account that made it, without a login.
	for name, id := range ids {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/s/"+id+"/download", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != name {
			t.Errorf("%s's link: status %d, body %q", name, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/s/0123456789abcdef0123456789abcdef", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: status %d, want 404", rec.Code)
	}
}
