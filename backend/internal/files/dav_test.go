package files

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
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

// davGrantsSetup is grantsSetup with WebDAV.
func davGrantsSetup(t *testing.T) (*Spaces, http.Handler, string, *[]string) {
	t.Helper()
	s, h, data, _, audited := grantsSetup(t)
	s.RegisterDAV(h.(*http.ServeMux), func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	})
	return s, h, data, audited
}

func TestDavSharedFolderRead(t *testing.T) {
	_, h, data, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
	root := "/dav-shared/" + g.ID + "/"
	rec := dav(h, "anna", "PROPFIND", root, "", map[string]string{"Depth": "1"})
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND: status %d", rec.Code)
	}
	for _, want := range []string{root + "a.txt", root + "2026/"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("PROPFIND lacks %s: %s", want, rec.Body)
		}
	}
	if strings.Contains(rec.Body.String(), "geheim") {
		t.Errorf("PROPFIND shows what is next to the folder: %s", rec.Body)
	}
	if rec := dav(h, "anna", http.MethodGet, root+"2026/b.txt", "", nil); rec.Code != http.StatusOK || rec.Body.String() != "Projekt/2026/b.txt" {
		t.Errorf("GET: status %d, body %q", rec.Code, rec.Body)
	}
	if rec := dav(h, "anna", http.MethodGet, root+"%2e%2e/geheim.txt", "", nil); rec.Code == http.StatusOK {
		t.Errorf("GET out of the folder: %q", rec.Body)
	}
	// Only reading, refused as such, not as a server error.
	if rec := dav(h, "anna", http.MethodPut, root+"neu.txt", "x", nil); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("PUT: status %d", rec.Code)
	}
	if rec := dav(h, "anna", http.MethodDelete, root+"a.txt", "", nil); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("DELETE: status %d", rec.Code)
	}
	if rec := dav(h, "anna", "MKCOL", root+"Neu", "", nil); rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("MKCOL: status %d", rec.Code)
	}
	if exists(data, "Projekt/neu.txt") || !exists(data, "Projekt/a.txt") || exists(data, "Projekt/Neu") {
		t.Error("changed a folder shared only to read")
	}
	// Nobody else, and no unknown grant.
	for user, target := range map[string]string{"bob": root, "chef": root, "anna": "/dav-shared/" + strings.Repeat("0", 32) + "/"} {
		if rec := dav(h, user, "PROPFIND", target, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s PROPFIND %s: status %d, want 404", user, target, rec.Code)
		}
	}
}

func TestDavSharedFolderRefusesSymlinks(t *testing.T) {
	_, h, data, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	root := "/dav-shared/" + g.ID + "/"
	if err := os.Symlink("..", filepath.Join(data, "Projekt", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../geheim.txt", filepath.Join(data, "Projekt", "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{root + "link/geheim.txt", root + "link.txt"} {
		if rec := dav(h, "anna", http.MethodGet, target, "", nil); rec.Code == http.StatusOK {
			t.Errorf("GET %s: served %q from outside the shared folder", target, rec.Body)
		}
	}
	if rec := dav(h, "anna", "PROPFIND", root+"link/", "", map[string]string{"Depth": "1"}); rec.Code == http.StatusMultiStatus {
		t.Errorf("PROPFIND through a symlink: %s", rec.Body)
	}
	// The folder's listing leaves the links out and shows the rest.
	rec := dav(h, "anna", "PROPFIND", root, "", map[string]string{"Depth": "1"})
	if body := rec.Body.String(); rec.Code != http.StatusMultiStatus || !strings.Contains(body, root+"a.txt") ||
		!strings.HasSuffix(strings.TrimSpace(body), "</D:multistatus>") || strings.Contains(body, root+"link") {
		t.Errorf("PROPFIND of the folder: status %d, %s", rec.Code, body)
	}
	if rec := dav(h, "anna", http.MethodPut, root+"link/neu.txt", "x", nil); rec.Code < 400 {
		t.Errorf("PUT through a symlink: status %d", rec.Code)
	}
	if exists(data, "neu.txt") {
		t.Error("wrote next to the shared folder")
	}
}

// A file locked on the owner's drive is locked on the shared folder's drive
// too, and the other way round.
func TestDavLocksAcrossDrives(t *testing.T) {
	_, h, _, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	root := "/dav-shared/" + g.ID + "/"
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := dav(h, "chef", "LOCK", "/dav/Projekt/a.txt", lock, map[string]string{"Depth": "0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("chef LOCK: status %d %s", rec.Code, rec.Body)
	}
	token := rec.Header().Get("Lock-Token")
	if rec := dav(h, "anna", http.MethodPut, root+"a.txt", "anna", nil); rec.Code != http.StatusLocked {
		t.Errorf("anna PUT on chef's lock: status %d, want 423", rec.Code)
	}
	if rec := dav(h, "anna", "LOCK", root+"a.txt", lock, map[string]string{"Depth": "0"}); rec.Code != http.StatusLocked {
		t.Errorf("anna LOCK on chef's lock: status %d, want 423", rec.Code)
	}
	if rec := dav(h, "chef", "UNLOCK", "/dav/Projekt/a.txt", "", map[string]string{"Lock-Token": token}); rec.Code != http.StatusNoContent {
		t.Fatalf("chef UNLOCK: status %d", rec.Code)
	}
	rec = dav(h, "anna", "LOCK", root+"a.txt", lock, map[string]string{"Depth": "0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("anna LOCK: status %d %s", rec.Code, rec.Body)
	}
	if rec := dav(h, "chef", http.MethodPut, "/dav/Projekt/a.txt", "chef", nil); rec.Code != http.StatusLocked {
		t.Errorf("chef PUT on anna's lock: status %d, want 423", rec.Code)
	}
}

// A grantee can neither undo nor use the owner's locks, and a read-only
// grantee takes none.
func TestDavLocksStayWithTheirDrive(t *testing.T) {
	_, h, _, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	ro := grantTo(t, h, "chef", `{"path":"/Projekt","to":"bob"}`)
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := dav(h, "chef", "LOCK", "/dav/geheim.txt", lock, map[string]string{"Depth": "0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("chef LOCK: status %d", rec.Code)
	}
	outside := strings.Trim(rec.Header().Get("Lock-Token"), "<>")
	if !strings.HasPrefix(outside, "opaquelocktoken:") || len(outside) < 40 {
		t.Errorf("a guessable token: %q", outside)
	}
	if rec := dav(h, "chef", "LOCK", "/dav/Projekt/a.txt", lock, map[string]string{"Depth": "0"}); rec.Code != http.StatusOK {
		t.Fatalf("chef LOCK inside: status %d", rec.Code)
	}
	root := "/dav-shared/" + g.ID + "/"
	if rec := dav(h, "anna", "UNLOCK", root+"a.txt", "", map[string]string{"Lock-Token": "<" + outside + ">"}); rec.Code == http.StatusNoContent {
		t.Error("anna removed chef's lock outside the shared folder")
	}
	if rec := dav(h, "chef", http.MethodPut, "/dav/geheim.txt", "x", nil); rec.Code != http.StatusLocked {
		t.Errorf("chef's lock is gone: PUT status %d", rec.Code)
	}
	if rec := dav(h, "chef", "UNLOCK", "/dav/geheim.txt", "", map[string]string{"Lock-Token": "<" + outside + ">"}); rec.Code != http.StatusNoContent {
		t.Errorf("chef UNLOCK: status %d", rec.Code)
	}
	// The lock system's own tokens count up; they are no way in.
	for _, guess := range []string{"1", "2"} {
		if rec := dav(h, "anna", http.MethodPut, root+"a.txt", "anna", map[string]string{"If": "(<" + guess + ">)"}); rec.Code < 400 {
			t.Errorf("anna PUT with token %s: status %d", guess, rec.Code)
		}
	}
	if rec := dav(h, "bob", "LOCK", "/dav-shared/"+ro.ID+"/", lock, map[string]string{"Depth": "infinity"}); rec.Code != http.StatusForbidden {
		t.Errorf("read-only LOCK: status %d, want 403", rec.Code)
	}
}

// An upload whose body broke off leaves the old file as it was.
func TestDavBrokenUploadKeepsTheFile(t *testing.T) {
	_, h, data := davSetup(t)
	if rec := dav(h, "chef", http.MethodPut, "/dav/a.txt", "alt", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPut, "/dav/a.txt", io.MultiReader(strings.NewReader("neu und abgebr"), iotest.ErrReader(io.ErrUnexpectedEOF)))
	req.Header.Set("X-User", "chef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("broken PUT: status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPut, "/dav/a.txt", strings.NewReader("kurz"))
	req.ContentLength = 100
	req.Header.Set("X-User", "chef")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("short PUT: status %d", rec.Code)
	}
	if got := readFile(t, filepath.Join(data, "a.txt")); got != "alt" {
		t.Errorf("the file holds %q, want the old one", got)
	}
	// COPY and LOCK write files too, with a body of their own or none.
	if rec := dav(h, "chef", "COPY", "/dav/a.txt", "", map[string]string{"Destination": "/dav/b.txt"}); rec.Code != http.StatusCreated {
		t.Errorf("COPY: status %d", rec.Code)
	}
	if got := readFile(t, filepath.Join(data, "b.txt")); got != "alt" {
		t.Errorf("the copy holds %q", got)
	}
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	if rec := dav(h, "chef", "LOCK", "/dav/neu.txt", lock, map[string]string{"Depth": "0"}); rec.Code != http.StatusCreated {
		t.Errorf("LOCK of a new name: status %d", rec.Code)
	}
	if !exists(data, "neu.txt") {
		t.Error("LOCK of a new name made no file")
	}
}

func TestDavSharedFolderWrite(t *testing.T) {
	_, h, data, audited := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	root := "/dav-shared/" + g.ID + "/"
	// anna's quota of 10 bytes does not apply in chef's folder.
	if rec := dav(h, "anna", http.MethodPut, root+"neu.txt", "mehr als zehn Bytes", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d", rec.Code)
	}
	if got := readFile(t, filepath.Join(data, "Projekt", "neu.txt")); got != "mehr als zehn Bytes" {
		t.Errorf("the file holds %q", got)
	}
	if rec := dav(h, "anna", "MOVE", root+"a.txt", "", map[string]string{"Destination": root + "2026/a.txt"}); rec.Code != http.StatusCreated {
		t.Errorf("MOVE: status %d", rec.Code)
	}
	if rec := dav(h, "anna", http.MethodDelete, root+"2026", "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: status %d", rec.Code)
	}
	if items := chefTrash(t, h); len(items) != 1 || items[0] != "/Projekt/2026" {
		t.Errorf("chef's recycle bin %v", items)
	}
	// Never the shared folder itself, nor out of it.
	if rec := dav(h, "anna", http.MethodDelete, root, "", nil); rec.Code < 400 {
		t.Errorf("DELETE of the folder: status %d", rec.Code)
	}
	if rec := dav(h, "anna", "MOVE", root+"neu.txt", "", map[string]string{"Destination": root + "../neu.txt"}); rec.Code == http.StatusCreated {
		t.Errorf("MOVE out of the folder: status %d", rec.Code)
	}
	if !exists(data, "Projekt") || !exists(data, "Projekt/neu.txt") || exists(data, "neu.txt") {
		t.Error("the folder or a file in it left the folder")
	}
	want := []string{
		"chef grant-create /Projekt → anna (lesen und schreiben)",
		"anna upload chef:/Projekt/neu.txt",
		"anna move chef:/Projekt/a.txt → /Projekt/2026/a.txt",
		"anna delete chef:/Projekt/2026",
	}
	if strings.Join(*audited, "|") != strings.Join(want, "|") {
		t.Errorf("audit:\n%s\nwant\n%s", strings.Join(*audited, "\n"), strings.Join(want, "\n"))
	}
}

func TestDavMoveKeepsTheGrant(t *testing.T) {
	_, h, _, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
	if rec := dav(h, "chef", "MOVE", "/dav/Projekt", "", map[string]string{"Destination": "/dav/Arbeit"}); rec.Code != http.StatusCreated {
		t.Fatalf("MOVE: status %d", rec.Code)
	}
	if _, withMe := grants(t, h, "anna"); len(withMe) != 1 || withMe[0].Path != "/Arbeit" {
		t.Errorf("anna's shared folders after the move: %+v", withMe)
	}
	if rec := dav(h, "anna", http.MethodGet, "/dav-shared/"+g.ID+"/a.txt", "", nil); rec.Code != http.StatusOK {
		t.Errorf("GET after the move: status %d", rec.Code)
	}
}

func TestDavQuotaProperties(t *testing.T) {
	_, h, _ := davSetup(t)
	if rec := dav(h, "anna", http.MethodPut, "/dav/klein.txt", "0123", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d", rec.Code)
	}
	body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><quota-used-bytes/><quota-available-bytes/></prop></propfind>`
	rec := dav(h, "anna", "PROPFIND", "/dav/", body, map[string]string{"Depth": "0"})
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND: status %d", rec.Code)
	}
	for _, want := range []string{">4</D:quota-used-bytes>", ">6</D:quota-available-bytes>"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("PROPFIND lacks %s: %s", want, rec.Body)
		}
	}
	// A file has none, and the quota cannot be changed.
	rec = dav(h, "anna", "PROPFIND", "/dav/klein.txt", body, map[string]string{"Depth": "0"})
	if strings.Contains(rec.Body.String(), ">6<") {
		t.Errorf("PROPFIND of a file: %s", rec.Body)
	}
	patch := `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><quota-available-bytes>999</quota-available-bytes></prop></set></propertyupdate>`
	rec = dav(h, "anna", "PROPPATCH", "/dav/", patch, nil)
	if !strings.Contains(rec.Body.String(), "403") {
		t.Errorf("PROPPATCH: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestDavProppatchKeepsTheFile(t *testing.T) {
	_, h, data := davSetup(t)
	if rec := dav(h, "chef", http.MethodPut, "/dav/a.txt", "inhalt", nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT: status %d", rec.Code)
	}
	// What Explorer sends after copying a file.
	patch := `<?xml version="1.0"?><propertyupdate xmlns="DAV:" xmlns:Z="urn:schemas-microsoft-com:"><set><prop>` +
		`<Z:Win32LastModifiedTime>Wed, 18 Nov 2026 10:00:00 GMT</Z:Win32LastModifiedTime>` +
		`<Z:Win32FileAttributes>00000020</Z:Win32FileAttributes></prop></set></propertyupdate>`
	rec := dav(h, "chef", "PROPPATCH", "/dav/a.txt", patch, nil)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "200 OK") {
		t.Errorf("PROPPATCH: status %d, body %s", rec.Code, rec.Body)
	}
	if got := readFile(t, filepath.Join(data, "a.txt")); got != "inhalt" {
		t.Errorf("after PROPPATCH the file holds %q", got)
	}
	other := `<?xml version="1.0"?><propertyupdate xmlns="DAV:"><set><prop><displayname>b</displayname></prop></set></propertyupdate>`
	if rec := dav(h, "chef", "PROPPATCH", "/dav/a.txt", other, nil); !strings.Contains(rec.Body.String(), "403") {
		t.Errorf("PROPPATCH of another property: status %d, body %s", rec.Code, rec.Body)
	}
}

// Ending a grant, or making it read-only, releases the locks taken through
// its drive: nobody else could ever undo them.
func TestDavGrantEndReleasesItsLocks(t *testing.T) {
	_, h, _, _ := davGrantsSetup(t)
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	for _, end := range []string{"revoke", "read-only"} {
		g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
		rec := dav(h, "anna", "LOCK", "/dav-shared/"+g.ID+"/a.txt", lock, map[string]string{"Depth": "0"})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: anna LOCK: status %d %s", end, rec.Code, rec.Body)
		}
		if rec := dav(h, "chef", "LOCK", "/dav/Projekt/"+end+".txt", lock, map[string]string{"Depth": "0"}); rec.Code >= 300 {
			t.Fatalf("%s: chef LOCK: status %d", end, rec.Code)
		}
		if end == "revoke" {
			if code, _ := do(h, http.MethodPost, "/api/grants/delete", "application/json", `{"id":"`+g.ID+`"}`, as("chef")); code != http.StatusOK {
				t.Fatalf("revoke: status %d", code)
			}
		} else {
			grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
		}
		if rec := dav(h, "chef", http.MethodPut, "/dav/Projekt/a.txt", "chef", nil); rec.Code >= 400 {
			t.Errorf("%s: chef PUT after the grant ended: status %d", end, rec.Code)
		}
		// The owner's own lock stays.
		if rec := dav(h, "chef", http.MethodPut, "/dav/Projekt/"+end+".txt", "chef", nil); rec.Code != http.StatusLocked {
			t.Errorf("%s: chef's own lock is gone: PUT status %d", end, rec.Code)
		}
		do(h, http.MethodPost, "/api/grants/delete", "application/json", `{"id":"`+g.ID+`"}`, as("chef"))
	}
}

// A lock through a shared drive runs out within sharedLockMax, a refresh
// answers the lock root as the drive sees it, and a drive cannot refresh a
// lock outside its folder.
func TestDavRefreshStaysWithTheDrive(t *testing.T) {
	sp, h, _, _ := davGrantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	root := "/dav-shared/" + g.ID + "/"
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	rec := dav(h, "anna", "LOCK", root+"a.txt", lock, map[string]string{"Depth": "0", "Timeout": "Infinite"})
	if rec.Code != http.StatusOK {
		t.Fatalf("anna LOCK: status %d", rec.Code)
	}
	token := rec.Header().Get("Lock-Token")
	// A lock through a shared folder always runs out.
	f, err := sp.space("chef")
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := f.locks().lock(strings.Trim(token, "<>")); !ok || l.until.IsZero() || time.Until(l.until) > sharedLockMax {
		t.Errorf("a shared folder's lock without an end: %+v %v", l, ok)
	}
	rec = dav(h, "anna", "LOCK", root+"a.txt", "", map[string]string{"If": "(" + token + ")", "Timeout": "Second-600"})
	if rec.Code != http.StatusOK {
		t.Fatalf("anna refresh: status %d %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<D:href>/a.txt</D:href>") || strings.Contains(body, "Projekt") {
		t.Errorf("refresh answers the root outside the drive: %s", body)
	}
	rec = dav(h, "chef", "LOCK", "/dav/geheim.txt", lock, map[string]string{"Depth": "0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("chef LOCK: status %d", rec.Code)
	}
	outside := rec.Header().Get("Lock-Token")
	if rec := dav(h, "anna", "LOCK", root+"a.txt", "", map[string]string{"If": "(" + outside + ")"}); rec.Code < 400 {
		t.Errorf("anna refreshed chef's lock outside the folder: status %d", rec.Code)
	}
}

// Uploads at the same time hold their room, so together they cannot go
// over the quota.
func TestDavParallelUploadsKeepTheQuota(t *testing.T) {
	s, _, _ := davSetup(t)
	f, err := s.space("anna")
	if err != nil {
		t.Fatal(err)
	}
	f.quota.Store(10)
	d := davFS{f: f, dir: ".", quota: true}
	a, err := d.create(context.Background(), "a.txt", os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.create(context.Background(), "b.txt", os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("012345")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("012345")); err == nil {
		t.Error("the second upload went over the quota")
	}
	_ = a.Close()
	_ = b.Close()
	if got := f.reserved.Load(); got != 0 {
		t.Errorf("still held after the uploads: %d bytes", got)
	}
}

// Taking a member out of a team releases the locks of their team drive.
func TestDavTeamMemberLeavesReleasesLocks(t *testing.T) {
	_, h, _, _ := davGrantsSetup(t)
	admin := func(method, target, body string) int {
		code, _ := do(h, method, target, "application/json", body, as("chef"))
		return code
	}
	if code := admin(http.MethodPost, "/api/teams", `{"name":"familie"}`); code != http.StatusCreated {
		t.Fatalf("create: status %d", code)
	}
	for _, to := range []string{"anna", "bob"} {
		if code := admin(http.MethodPost, "/api/teams/familie/members", `{"to":"`+to+`","write":true}`); code != http.StatusOK {
			t.Fatalf("member %s: status %d", to, code)
		}
	}
	_, withMe := grants(t, h, "anna")
	_, bobs := grants(t, h, "bob")
	if len(withMe) != 1 || len(bobs) != 1 {
		t.Fatalf("grants: %+v %+v", withMe, bobs)
	}
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	if rec := dav(h, "anna", "LOCK", "/dav-shared/"+withMe[0].ID+"/a.txt", lock, map[string]string{"Depth": "0"}); rec.Code >= 300 {
		t.Fatalf("anna LOCK: status %d", rec.Code)
	}
	bob := "/dav-shared/" + bobs[0].ID + "/a.txt"
	if rec := dav(h, "bob", http.MethodPut, bob, "bob", nil); rec.Code != http.StatusLocked {
		t.Fatalf("bob PUT on anna's lock: status %d, want 423", rec.Code)
	}
	if code := admin(http.MethodPost, "/api/teams/familie/members/delete", `{"to":"anna"}`); code != http.StatusOK {
		t.Fatalf("remove anna: status %d", code)
	}
	if rec := dav(h, "bob", http.MethodPut, bob, "bob", nil); rec.Code >= 400 {
		t.Errorf("bob PUT after anna left: status %d", rec.Code)
	}
}
