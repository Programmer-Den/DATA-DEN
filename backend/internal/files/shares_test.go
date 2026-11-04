package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// guard counts wrong passwords and locks out after the third.
type guard struct{ failed []string }

func (g *guard) Wait(*http.Request) time.Duration {
	if len(g.failed) >= 3 {
		return time.Minute
	}
	return 0
}

func (g *guard) Failed(_ *http.Request, id string) { g.failed = append(g.failed, id) }

// shareTree is tree with the share links: everything is logged in, but the
// /api/s/ requests never check that.
func shareTree(t *testing.T) (string, http.Handler, *guard) {
	t.Helper()
	data, _ := tree(t)
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	g := &guard{}
	mux := http.NewServeMux()
	open := func(next http.HandlerFunc) http.HandlerFunc { return next }
	f.Register(mux, open)
	f.RegisterShares(mux, open, g)
	return data, mux, g
}

func share(t *testing.T, h http.Handler, body string) Share {
	t.Helper()
	rec := post(h, "/api/shares", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var s Share
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func shareList(t *testing.T, h http.Handler) []Share {
	t.Helper()
	rec := get(h, "/api/shares", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []Share `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Items
}

// public sends a request to a share link, with cookies if any.
func public(h http.Handler, method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestShareFolder(t *testing.T) {
	data, h, _ := shareTree(t)
	s := share(t, h, `{"path":"/Fotos","days":7}`)
	if !trashID.MatchString(s.ID) || s.Path != "/Fotos" || s.Name != "Fotos" || !s.Dir || s.Password || s.PasswordHash != "" {
		t.Errorf("share %+v", s)
	}
	if days := s.Expires.Sub(s.Created).Hours() / 24; days != 7 {
		t.Errorf("expires after %v days", days)
	}
	if items := shareList(t, h); len(items) != 1 || items[0].ID != s.ID {
		t.Errorf("list %+v", items)
	}
	// The share file is the server's own: not listed, not reachable.
	if exists(data, sharesDir) == false {
		t.Error("no share file")
	}
	for _, e := range listing(t, get(h, "/api/files", "/", nil)).Entries {
		if e.Name == sharesDir {
			t.Error("the shares are listed")
		}
	}

	base := "/api/s/" + s.ID
	rec := public(h, http.MethodGet, base, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Fotos"`) || strings.Contains(rec.Body.String(), "/Fotos") {
		t.Errorf("info: status %d, body %s", rec.Code, rec.Body)
	}
	top := listing(t, get(h, base+"/files", "", nil))
	if top.Path != "/" || len(top.Entries) != 2 || top.Entries[0].Name != "2026" || top.Entries[1].Name != "a.jpg" {
		t.Errorf("top %+v", top)
	}
	if sub := listing(t, get(h, base+"/files", "/2026", nil)); sub.Path != "/2026" || len(sub.Entries) != 1 {
		t.Errorf("sub %+v", sub)
	}
	if rec := get(h, base+"/download", "/2026/b.jpg", nil); rec.Code != http.StatusOK || rec.Body.String() != "Fotos/2026/b.jpg" {
		t.Errorf("download: status %d, body %s", rec.Code, rec.Body)
	}
	// Nothing outside the shared folder.
	for _, p := range []string{"/../notizen.txt", "/2026/../../notizen.txt", "/fehlt.jpg"} {
		if rec := get(h, base+"/download", p, nil); rec.Code == http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
	if rec := get(h, base+"/files", "/../", nil); rec.Code == http.StatusOK {
		t.Errorf("listing above the share: status %d", rec.Code)
	}

	rec = get(h, base+"/zip", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "Fotos.zip") {
		t.Fatalf("zip: status %d, headers %v", rec.Code, rec.Header())
	}
	archive, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
		if file.Name == "2026/b.jpg" {
			r, _ := file.Open()
			content, _ := io.ReadAll(r)
			if string(content) != "Fotos/2026/b.jpg" {
				t.Errorf("content %q", content)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"2026/b.jpg", "a.jpg"}) {
		t.Errorf("zip has %v", names)
	}
}

func TestShareFile(t *testing.T) {
	_, h, _ := shareTree(t)
	s := share(t, h, `{"path":"/notizen.txt","days":1}`)
	base := "/api/s/" + s.ID
	rec := public(h, http.MethodGet, base, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"size":11`) {
		t.Errorf("info: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := get(h, base+"/download", "", nil); rec.Code != http.StatusOK || rec.Body.String() != "notizen.txt" {
		t.Errorf("download: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := get(h, base+"/download", "/anderes.txt", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a path in a shared file: status %d", rec.Code)
	}
	for _, endpoint := range []string{"/files", "/zip"} {
		if rec := get(h, base+endpoint, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s of a file: status %d", endpoint, rec.Code)
		}
	}
}

func TestSharePassword(t *testing.T) {
	_, h, g := shareTree(t)
	s := share(t, h, `{"path":"/Fotos","days":7,"password":"geheim"}`)
	if !s.Password || s.PasswordHash != "" {
		t.Errorf("share %+v", s)
	}
	base := "/api/s/" + s.ID
	for _, endpoint := range []string{"", "/files", "/download?path=/a.jpg", "/zip"} {
		rec := public(h, http.MethodGet, base+endpoint, "")
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"password":true`) {
			t.Errorf("%s locked: status %d, body %s", endpoint, rec.Code, rec.Body)
		}
	}
	if rec := public(h, http.MethodPost, base+"/unlock", `{"password":"falsch"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d", rec.Code)
	}
	if len(g.failed) != 1 || g.failed[0] != s.ID {
		t.Errorf("failures %v", g.failed)
	}
	rec := public(h, http.MethodPost, base+"/unlock", `{"password":"geheim"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock: status %d, body %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != base || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("cookies %+v", cookies)
	}
	if rec := public(h, http.MethodGet, base+"/download?path=/a.jpg", "", cookies...); rec.Code != http.StatusOK {
		t.Errorf("unlocked: status %d", rec.Code)
	}
	// The cookie opens only its own share.
	other := share(t, h, `{"path":"/notizen.txt","days":7,"password":"geheim"}`)
	if rec := public(h, http.MethodGet, "/api/s/"+other.ID, "", cookies...); rec.Code != http.StatusUnauthorized {
		t.Errorf("other share: status %d", rec.Code)
	}

	// Too many wrong guesses lock the client out, even with the right one.
	g.failed = []string{"a", "b", "c"}
	rec = public(h, http.MethodPost, base+"/unlock", `{"password":"geheim"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("locked out: status %d", rec.Code)
	}
}

func TestShareExpiresAndRevoke(t *testing.T) {
	data, h, _ := shareTree(t)
	s := share(t, h, `{"path":"/Fotos","days":7}`)
	s.Expires = time.Now().Add(-time.Minute)
	content, _ := json.Marshal(s)
	file := filepath.Join(data, sharesDir, s.ID+".json")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := public(h, http.MethodGet, "/api/s/"+s.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("expired: status %d", rec.Code)
	}
	if exists(data, sharesDir+"/"+s.ID+".json") {
		t.Error("the expired share is still there")
	}

	s = share(t, h, `{"path":"/Fotos","days":7}`)
	if rec := post(h, "/api/shares/delete", `{"id":"`+s.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := public(h, http.MethodGet, "/api/s/"+s.ID+"/files", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoked: status %d", rec.Code)
	}
	if items := shareList(t, h); len(items) != 0 {
		t.Errorf("list %+v", items)
	}
	for _, id := range []string{s.ID, "nicht-hex", "../../etc"} {
		if rec := post(h, "/api/shares/delete", `{"id":"`+id+`"}`); rec.Code != http.StatusNotFound {
			t.Errorf("revoke %s: status %d", id, rec.Code)
		}
		if rec := public(h, http.MethodGet, "/api/s/"+url.PathEscape(id), ""); rec.Code != http.StatusNotFound {
			t.Errorf("open %s: status %d", id, rec.Code)
		}
	}
}

func TestShareFollowsMoves(t *testing.T) {
	_, h, _ := shareTree(t)
	folder := share(t, h, `{"path":"/Fotos/2026","days":7}`)
	file := share(t, h, `{"path":"/notizen.txt","days":7}`)
	post(h, "/api/move", `{"from":"/Fotos","to":"/Bilder"}`)
	post(h, "/api/move", `{"from":"/notizen.txt","to":"/Bilder/liste.txt"}`)
	if rec := get(h, "/api/s/"+folder.ID+"/download", "/b.jpg", nil); rec.Code != http.StatusOK {
		t.Errorf("moved folder: status %d, body %s", rec.Code, rec.Body)
	}
	rec := public(h, http.MethodGet, "/api/s/"+file.ID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"liste.txt"`) {
		t.Errorf("moved file: status %d, body %s", rec.Code, rec.Body)
	}

	// In the recycle bin, the link stops working and the list says so.
	post(h, "/api/delete", `{"path":"/Bilder/liste.txt"}`)
	if rec := public(h, http.MethodGet, "/api/s/"+file.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted file: status %d", rec.Code)
	}
	for _, s := range shareList(t, h) {
		if s.Missing != (s.ID == file.ID) {
			t.Errorf("missing %+v", s)
		}
	}
}

func TestShareSymlink(t *testing.T) {
	data, h, _ := shareTree(t)
	// A link inside the shared folder to elsewhere in the data directory.
	if err := os.Symlink(filepath.Join(data, "notizen.txt"), filepath.Join(data, "Fotos", "link.txt")); err != nil {
		t.Fatal(err)
	}
	s := share(t, h, `{"path":"/Fotos","days":7}`)
	if rec := get(h, "/api/s/"+s.ID+"/download", "/link.txt", nil); rec.Code != http.StatusNotFound {
		t.Errorf("symlink: status %d", rec.Code)
	}
	rec := get(h, "/api/s/"+s.ID+"/zip", "", nil)
	archive, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if file.Name == "link.txt" {
			t.Error("the symlink is in the ZIP")
		}
	}
	if rec := post(h, "/api/shares", `{"path":"/Fotos/link.txt","days":7}`); rec.Code != http.StatusBadRequest {
		t.Errorf("sharing a symlink: status %d", rec.Code)
	}
}

func TestCreateShareChecks(t *testing.T) {
	_, h, _ := shareTree(t)
	tests := []struct {
		body string
		want int
	}{
		{`{"path":"/Fotos","days":0}`, http.StatusBadRequest},
		{`{"path":"/Fotos","days":366}`, http.StatusBadRequest},
		{`{"path":"/","days":7}`, http.StatusBadRequest},
		{`{"path":"/../raus","days":7}`, http.StatusBadRequest},
		{`{"path":"/.data-den-trash","days":7}`, http.StatusBadRequest},
		{`{"path":"/fehlt","days":7}`, http.StatusNotFound},
		{`{"path":"/Fotos","days":7,"password":"` + strings.Repeat("x", 73) + `"}`, http.StatusBadRequest},
		{`kein json`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/shares", tt.body); rec.Code != tt.want {
			t.Errorf("%.60s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
}
