package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// davSetup serves WebDAV with Spaces like spacesSetup: the account is the
// "X-User" header, "anna" may store 10 bytes.
func davSetup(t *testing.T) (*Spaces, http.Handler, string) {
	t.Helper()
	s, _, data := spacesSetup(t)
	mux := http.NewServeMux()
	s.RegisterDAV(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	})
	return s, mux, data
}

func dav(h http.Handler, user, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-User", user)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDavPutGetAndList(t *testing.T) {
	s, h, data := davSetup(t)
	if rec := dav(h, "chef", "MKCOL", "/dav/Fotos", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("MKCOL: status %d", rec.Code)
	}
	if rec := dav(h, "chef", http.MethodPut, "/dav/Fotos/a.txt", "hallo", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d, %s", rec.Code, rec.Body)
	}
	if got := readFile(t, filepath.Join(data, "Fotos", "a.txt")); got != "hallo" {
		t.Errorf("content %q", got)
	}
	if s.owner.sums.byPath["Fotos/a.txt"] == nil {
		t.Error("no checksum for the new file")
	}
	if rec := dav(h, "chef", http.MethodGet, "/dav/Fotos/a.txt", "", nil); rec.Code != http.StatusOK || rec.Body.String() != "hallo" {
		t.Errorf("GET: status %d, %q", rec.Code, rec.Body)
	}

	// The server's own folders are neither listed nor reachable.
	if err := os.MkdirAll(filepath.Join(data, ".data-den-trash"), 0o750); err != nil {
		t.Fatal(err)
	}
	rec := dav(h, "chef", "PROPFIND", "/dav/", "", map[string]string{"Depth": "1"})
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "/dav/Fotos/") {
		t.Fatalf("PROPFIND: status %d, %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), ".data-den-") {
		t.Errorf("PROPFIND lists the server's folders: %s", rec.Body)
	}
	if rec := dav(h, "chef", "PROPFIND", "/dav/.data-den-trash", "", map[string]string{"Depth": "0"}); rec.Code != http.StatusNotFound {
		t.Errorf("PROPFIND of a hidden folder: status %d", rec.Code)
	}
	if rec := dav(h, "chef", http.MethodPut, "/dav/.data-den-users/x", "x", nil); rec.Code == http.StatusCreated {
		t.Error("PUT into a hidden folder worked")
	}
}

func TestDavOverwriteKeepsAVersion(t *testing.T) {
	s, h, data := davSetup(t)
	dav(h, "chef", http.MethodPut, "/dav/a.txt", "eins", nil)
	if rec := dav(h, "chef", http.MethodPut, "/dav/a.txt", "zwei", nil); rec.Code != http.StatusCreated && rec.Code != http.StatusNoContent {
		t.Fatalf("overwrite: status %d", rec.Code)
	}
	if got := readFile(t, filepath.Join(data, "a.txt")); got != "zwei" {
		t.Errorf("content %q", got)
	}
	if versions := s.owner.versionsOf("a.txt"); len(versions) != 1 {
		t.Errorf("versions %v, want the old content", versions)
	}
}

func TestDavMoveAndDelete(t *testing.T) {
	s, h, data := davSetup(t)
	dav(h, "chef", http.MethodPut, "/dav/a.txt", "inhalt", nil)
	if rec := dav(h, "chef", "MOVE", "/dav/a.txt", "", map[string]string{"Destination": "http://example.com/dav/b.txt"}); rec.Code != http.StatusCreated {
		t.Fatalf("MOVE: status %d", rec.Code)
	}
	if exists(data, "a.txt") || !exists(data, "b.txt") {
		t.Error("not moved")
	}
	if rec := dav(h, "chef", http.MethodDelete, "/dav/b.txt", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d", rec.Code)
	}
	if exists(data, "b.txt") {
		t.Error("still there")
	}
	if items := s.owner.trashItems(); len(items) != 1 || items[0].Path != "/b.txt" {
		t.Errorf("recycle bin %v, want b.txt", items)
	}
	if rec := dav(h, "chef", http.MethodDelete, "/dav/", "", nil); rec.Code == http.StatusNoContent {
		t.Error("deleted the top folder")
	}
}

func TestDavSpacesAndQuota(t *testing.T) {
	_, h, data := davSetup(t)
	if rec := dav(h, "anna", http.MethodPut, "/dav/klein.txt", "0123", nil); rec.Code != http.StatusCreated {
		t.Fatalf("anna's PUT: status %d", rec.Code)
	}
	if !exists(data, ".data-den-users/anna/klein.txt") || exists(data, "klein.txt") {
		t.Error("anna's file is not in her space")
	}
	if rec := dav(h, "anna", http.MethodPut, "/dav/gross.txt", "0123456789", nil); rec.Code < 400 {
		t.Errorf("over the quota: status %d", rec.Code)
	}
	if exists(data, ".data-den-users/anna/gross.txt") {
		t.Error("the file over the quota was kept")
	}
	entries, _ := os.ReadDir(filepath.Join(data, ".data-den-users", "anna", uploadDir))
	if len(entries) != 0 {
		t.Errorf("left over in the upload folder: %v", entries)
	}
}

func TestDavRefusesSymlinksOut(t *testing.T) {
	_, h, data := davSetup(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("geheim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if rec := dav(h, "chef", http.MethodGet, "/dav/link.txt", "", nil); rec.Code == http.StatusOK {
		t.Errorf("served a file outside the data directory: %q", rec.Body)
	}
}
