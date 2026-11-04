package files

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dropFile uploads content through the upload link id as name, in one
// chunk, and returns the name it was saved as.
func dropFile(t *testing.T, h http.Handler, id, name, content string, cookies ...*http.Cookie) string {
	t.Helper()
	base := "/api/s/" + id + "/uploads"
	body, _ := json.Marshal(map[string]any{"path": "/" + name, "size": len(content)})
	rec := public(h, http.MethodPost, base, string(body), cookies...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start %s: status %d, body %s", name, rec.Code, rec.Body)
	}
	var started struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	if content == "" {
		return name
	}
	req := httptest.NewRequest(http.MethodPut, base+"/"+started.ID, strings.NewReader(content))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Upload-Offset", "0")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var done map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &done)
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk %s: status %d, body %s", name, rec.Code, rec.Body)
	}
	if _, ok := done["duplicates"]; ok {
		t.Errorf("the answer tells about other files: %v", done)
	}
	saved, _ := done["name"].(string)
	return saved
}

func TestUploadLink(t *testing.T) {
	data, h, _ := shareTree(t)
	s := share(t, h, `{"path":"/Fotos","days":7,"upload":true}`)
	if !s.Upload || !s.Dir {
		t.Errorf("share %+v", s)
	}
	if items := shareList(t, h); len(items) != 1 || !items[0].Upload {
		t.Errorf("list %+v", items)
	}
	base := "/api/s/" + s.ID
	rec := public(h, http.MethodGet, base, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"upload":true`) {
		t.Errorf("info: status %d, body %s", rec.Code, rec.Body)
	}
	// Nothing comes out: no listing, no download, no ZIP.
	for _, endpoint := range []string{"/files?path=/", "/download?path=/a.jpg", "/zip?path=/"} {
		if rec := public(h, http.MethodGet, base+endpoint, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", endpoint, rec.Code)
		}
	}

	if saved := dropFile(t, h, s.ID, "neu.jpg", "hallo"); saved != "neu.jpg" {
		t.Errorf("saved as %q", saved)
	}
	if got := read(t, filepath.Join(data, "Fotos", "neu.jpg")); got != "hallo" {
		t.Errorf("the file holds %q", got)
	}
	// A taken name gets the next free one; nothing is overwritten.
	if saved := dropFile(t, h, s.ID, "a.jpg", "zweites"); saved != "a (1).jpg" {
		t.Errorf("saved as %q", saved)
	}
	if saved := dropFile(t, h, s.ID, "a.jpg", "drittes"); saved != "a (2).jpg" {
		t.Errorf("saved as %q", saved)
	}
	if got := read(t, filepath.Join(data, "Fotos", "a.jpg")); got != "Fotos/a.jpg" {
		t.Errorf("a.jpg was overwritten with %q", got)
	}
	if dropFile(t, h, s.ID, "leer.txt", ""); !exists(data, "Fotos/leer.txt") {
		t.Error("an empty file was not saved")
	}

	// Only a file name, never into the folders inside or out of the folder.
	for _, p := range []string{"/2026/b.jpg", "/../x.jpg", "/.data-den-shares", "/"} {
		body, _ := json.Marshal(map[string]any{"path": p, "size": 1})
		if rec := public(h, http.MethodPost, base+"/uploads", string(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", p, rec.Code)
		}
	}
	if rec := public(h, http.MethodPost, base+"/uploads", "path=/x&size=1"); rec.Code == http.StatusCreated {
		t.Error("an upload without a JSON body")
	}
}

func TestUploadLinkKeepsUploadsApart(t *testing.T) {
	_, h, _ := shareTree(t)
	link := share(t, h, `{"path":"/Fotos","days":7,"upload":true}`)
	other := share(t, h, `{"path":"/Fotos","days":7,"upload":true}`)
	// An upload behind the login cannot be continued through a link.
	_, id := start(t, h, "/intern.txt", 3)
	put := func(target string) int {
		req := httptest.NewRequest(http.MethodPut, target, strings.NewReader("abc"))
		req.Header.Set("Upload-Offset", "0")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := put("/api/s/" + link.ID + "/uploads/" + id); code != http.StatusNotFound {
		t.Errorf("a login upload through a link: status %d, want 404", code)
	}
	// Nor one of a link through another link, or behind the login.
	rec := public(h, http.MethodPost, "/api/s/"+link.ID+"/uploads", `{"path":"/x.txt","size":3}`)
	var started struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	if code := put("/api/s/" + other.ID + "/uploads/" + started.ID); code != http.StatusNotFound {
		t.Errorf("through another link: status %d, want 404", code)
	}
	if code := put("/api/uploads/" + started.ID); code != http.StatusNotFound {
		t.Errorf("behind the login: status %d, want 404", code)
	}
	if rec := public(h, http.MethodGet, "/api/s/"+link.ID+"/uploads/"+started.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("state: status %d", rec.Code)
	}
}

func TestUploadLinkChecks(t *testing.T) {
	data, h, _ := shareTree(t)
	if rec := post(h, "/api/shares", `{"path":"/notizen.txt","days":7,"upload":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an upload link to a file: status %d, want 400", rec.Code)
	}
	// A plain link takes no uploads.
	plain := share(t, h, `{"path":"/Fotos","days":7}`)
	if rec := public(h, http.MethodPost, "/api/s/"+plain.ID+"/uploads", `{"path":"/x.jpg","size":1}`); rec.Code != http.StatusNotFound {
		t.Errorf("upload through a plain link: status %d, want 404", rec.Code)
	}
	// Once the folder is gone, nothing goes in.
	s := share(t, h, `{"path":"/Fotos/2026","days":7,"upload":true}`)
	if err := os.RemoveAll(filepath.Join(data, "Fotos", "2026")); err != nil {
		t.Fatal(err)
	}
	if rec := public(h, http.MethodPost, "/api/s/"+s.ID+"/uploads", `{"path":"/x.jpg","size":1}`); rec.Code != http.StatusNotFound {
		t.Errorf("into a missing folder: status %d, want 404", rec.Code)
	}
	if exists(data, "Fotos/2026") {
		t.Error("the folder was made again")
	}
}

func TestUploadLinkPassword(t *testing.T) {
	data, h, _ := shareTree(t)
	s := share(t, h, `{"path":"/Fotos","days":7,"password":"geheim","upload":true}`)
	base := "/api/s/" + s.ID
	if rec := public(h, http.MethodPost, base+"/uploads", `{"path":"/x.jpg","size":1}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("locked: status %d, want 401", rec.Code)
	}
	rec := public(h, http.MethodPost, base+"/unlock", `{"password":"geheim"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock: status %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if saved := dropFile(t, h, s.ID, "x.jpg", "x", cookies...); saved != "x.jpg" || !exists(data, "Fotos/x.jpg") {
		t.Errorf("saved as %q", saved)
	}
}

func TestUploadLinkQuota(t *testing.T) {
	s, _, data := spacesSetup(t)
	var got []string
	s.cfg.Audit = func(_ *http.Request, user, action, target string) { got = append(got, user+" "+action+" "+target) }
	mux := http.NewServeMux()
	require := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	}
	s.Register(mux, require)
	s.RegisterShares(mux, require, noGuard{})
	if err := os.MkdirAll(filepath.Join(data, usersDir, "anna", "Eingang"), 0o750); err != nil {
		t.Fatal(err)
	}
	code, answer := do(mux, http.MethodPost, "/api/shares", "application/json", `{"path":"/Eingang","days":7,"upload":true}`, as("anna"))
	if code != http.StatusCreated {
		t.Fatalf("anna's link: status %d %v", code, answer)
	}
	id := answer["id"].(string)
	// Anna may store 10 bytes; the link fills her space, not the owner's.
	if saved := dropFile(t, mux, id, "a.txt", "12345"); saved != "a.txt" {
		t.Errorf("saved as %q", saved)
	}
	if !exists(data, filepath.Join(usersDir, "anna", "Eingang", "a.txt")) {
		t.Error("the file is not in anna's space")
	}
	body := `{"path":"/b.txt","size":` + strconv.Itoa(6) + `}`
	if rec := public(mux, http.MethodPost, "/api/s/"+id+"/uploads", body); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("over anna's quota: status %d, want 507", rec.Code)
	}
	if strings.Join(got, "|") != "anna share-create /Eingang|anna share-upload /Eingang/a.txt" {
		t.Errorf("audit %v", got)
	}
}
