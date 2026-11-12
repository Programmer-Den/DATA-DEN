package files

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"
)

// replace uploads content over the file p.
func replace(t *testing.T, h http.Handler, p, content string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"path": p, "size": len(content), "replace": true})
	code, answer := do(h, http.MethodPost, "/api/uploads", "application/json", string(body), nil)
	if code != http.StatusCreated {
		t.Fatalf("start replacing %s: status %d, %v", p, code, answer)
	}
	if content == "" {
		return
	}
	if code, answer := chunk(h, answer["id"].(string), 0, content); code != http.StatusOK {
		t.Fatalf("replacing %s: status %d, %v", p, code, answer)
	}
}

func versionsOf(t *testing.T, h http.Handler, p string) []Version {
	t.Helper()
	rec := get(h, "/api/versions", p, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("versions of %s: status %d", p, rec.Code)
	}
	var answer struct {
		Versions []Version `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Versions
}

func TestReplaceKeepsVersions(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "brief.txt"), []byte("eins"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/brief.txt", "zwei")
	replace(t, h, "/brief.txt", "drei!")
	if got := read(t, filepath.Join(data, "brief.txt")); got != "drei!" {
		t.Errorf("file %q, want drei!", got)
	}
	versions := versionsOf(t, h, "/brief.txt")
	if len(versions) != 2 || versions[0].Size != 4 || versions[0].Path != "/brief.txt" {
		t.Fatalf("versions %+v", versions)
	}
	rec := versionContent(h, "/brief.txt", versions[0].ID)
	if rec.Code != http.StatusOK || rec.Body.String() != "zwei" {
		t.Errorf("newest version: status %d, %q", rec.Code, rec.Body.String())
	}
	if rec := versionContent(h, "/brief.txt", versions[1].ID); rec.Body.String() != "eins" {
		t.Errorf("oldest version %q", rec.Body.String())
	}
	// The versions folder is never listed.
	if l := listing(t, get(h, "/api/files", "/", nil)); len(l.Entries) != 1 {
		t.Errorf("listing %+v", l.Entries)
	}
}

func TestReplaceOnlyWhenAsked(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.Mkdir(filepath.Join(data, "Ordner"), 0o750); err != nil {
		t.Fatal(err)
	}
	body := `{"path": "/Ordner", "size": 1, "replace": true}`
	if code, _ := do(h, http.MethodPost, "/api/uploads", "application/json", body, nil); code != http.StatusConflict {
		t.Errorf("replacing a folder: status %d, want 409", code)
	}
	// Replacing a file that is not there just creates it.
	replace(t, h, "/neu.txt", "neu")
	if got := read(t, filepath.Join(data, "neu.txt")); got != "neu" {
		t.Errorf("file %q", got)
	}
	if v := versionsOf(t, h, "/neu.txt"); len(v) != 0 {
		t.Errorf("versions of a new file: %+v", v)
	}
}

func TestRestoreVersion(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/a.txt", "neu")
	old := versionsOf(t, h, "/a.txt")[0]
	if rec := post(h, "/api/versions/restore", fmt.Sprintf(`{"path": "/a.txt", "id": %q}`, old.ID)); rec.Code != http.StatusOK {
		t.Fatalf("restore: status %d, %s", rec.Code, rec.Body)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "alt" {
		t.Errorf("file %q, want alt", got)
	}
	// What was replaced by restoring is a version now.
	versions := versionsOf(t, h, "/a.txt")
	if len(versions) != 1 || versions[0].ID == old.ID {
		t.Fatalf("versions %+v", versions)
	}
	if rec := versionContent(h, "/a.txt", versions[0].ID); rec.Body.String() != "neu" {
		t.Errorf("version %q, want neu", rec.Body.String())
	}

	// A deleted file comes back from a version.
	if rec := post(h, "/api/delete", `{"path": "/a.txt"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := post(h, "/api/versions/restore", fmt.Sprintf(`{"path": "/a.txt", "id": %q}`, versions[0].ID)); rec.Code != http.StatusOK {
		t.Fatalf("restore after delete: status %d, %s", rec.Code, rec.Body)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "neu" {
		t.Errorf("file %q, want neu", got)
	}
}

func TestVersionRequestsRefused(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/a.txt", "neu")
	id := versionsOf(t, h, "/a.txt")[0].ID
	tests := []struct {
		body string
		want int
	}{
		{`{"path": "/a.txt", "id": "../../a.txt"}`, http.StatusBadRequest},
		{`{"path": "/../a.txt", "id": "` + id + `"}`, http.StatusBadRequest},
		{`{"path": "/b.txt", "id": "` + id + `"}`, http.StatusNotFound},
		{`{"path": "/a.txt", "id": "00000000000000000000000000000000"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/versions/restore", tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
	if rec := get(h, "/api/versions", "/"+versionsDir, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("versions of the versions folder: status %d, want 400", rec.Code)
	}
	if rec := get(h, "/api/download", "/"+path.Join(versionFolder("a.txt"), id), nil); rec.Code != http.StatusNotFound {
		t.Errorf("download from the versions folder: status %d, want 404", rec.Code)
	}
	req := `{"path": "/a.txt", "id": "` + id + `"}`
	if code, _ := do(h, http.MethodPost, "/api/versions/restore", "text/plain", req, nil); code != http.StatusUnsupportedMediaType {
		t.Errorf("restore without JSON: status %d, want 415", code)
	}
}

func TestDeleteVersion(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/a.txt", "neu")
	id := versionsOf(t, h, "/a.txt")[0].ID
	if rec := post(h, "/api/versions/delete", `{"path": "/a.txt", "id": "`+id+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if v := versionsOf(t, h, "/a.txt"); len(v) != 0 {
		t.Errorf("versions %+v", v)
	}
	if _, err := os.Stat(filepath.Join(data, filepath.FromSlash(versionFolder("a.txt")))); !os.IsNotExist(err) {
		t.Errorf("the empty versions folder stayed: %v", err)
	}
}

func TestVersionsLimited(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MaxVersions+2; i++ {
		replace(t, h, "/a.txt", fmt.Sprint(i))
	}
	versions := versionsOf(t, h, "/a.txt")
	if len(versions) != MaxVersions {
		t.Fatalf("%d versions, want %d", len(versions), MaxVersions)
	}
	// The oldest went.
	oldest := versions[len(versions)-1]
	if rec := versionContent(h, "/a.txt", oldest.ID); rec.Body.String() != "2" {
		t.Errorf("oldest version %q, want 2", rec.Body.String())
	}
}

func TestVersionsExpire(t *testing.T) {
	data := t.TempDir()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := f.root.Stat("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.versionMu.Lock()
	v, err := f.keepVersion("a.txt", info)
	f.versionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	v.Saved = time.Now().AddDate(0, 0, -VersionDays-1)
	b, _ := json.Marshal(v)
	if err := f.root.WriteFile(path.Join(versionFolder("a.txt"), v.ID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	f.purgeVersions()
	if left := f.versionsOf("a.txt"); len(left) != 0 {
		t.Errorf("versions left %+v", left)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "alt" {
		t.Errorf("the file itself changed: %q", got)
	}
}

func TestVersionsFollowMoves(t *testing.T) {
	h, data := uploadSetup(t)
	if err := os.MkdirAll(filepath.Join(data, "Texte"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "Texte", "a.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/Texte/a.txt", "neu")
	if rec := post(h, "/api/move", `{"from": "/Texte", "to": "/Briefe"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := post(h, "/api/move", `{"from": "/Briefe/a.txt", "to": "/Briefe/b.txt"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if v := versionsOf(t, h, "/Texte/a.txt"); len(v) != 0 {
		t.Errorf("versions at the old path %+v", v)
	}
	versions := versionsOf(t, h, "/Briefe/b.txt")
	if len(versions) != 1 || versions[0].Path != "/Briefe/b.txt" {
		t.Fatalf("versions %+v", versions)
	}
	if rec := versionContent(h, "/Briefe/b.txt", versions[0].ID); rec.Body.String() != "alt" {
		t.Errorf("version %q, want alt", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(data, filepath.FromSlash(versionFolder("Texte/a.txt")))); !os.IsNotExist(err) {
		t.Errorf("the old versions folder stayed: %v", err)
	}
}

// versionContent downloads the version id of the file p.
func versionContent(h http.Handler, p, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/versions/download?path="+url.QueryEscape(p)+"&id="+id, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReplaceWithSameContentKeepsNoVersion(t *testing.T) {
	h, data := uploadSetup(t)
	replace(t, h, "/a.txt", "eins")
	replace(t, h, "/a.txt", "eins")
	if v := versionsOf(t, h, "/a.txt"); len(v) != 0 {
		t.Fatalf("versions after the same content: %+v", v)
	}
	replace(t, h, "/a.txt", "zwei")
	if v := versionsOf(t, h, "/a.txt"); len(v) != 1 {
		t.Fatalf("versions after other content: %+v", v)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "zwei" {
		t.Errorf("file %q, want zwei", got)
	}

	// Without a checksum of the file as it is, the old content is kept.
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("drei"), 0o600); err != nil {
		t.Fatal(err)
	}
	replace(t, h, "/a.txt", "drei")
	if v := versionsOf(t, h, "/a.txt"); len(v) != 2 {
		t.Errorf("versions after a change outside the app: %+v", v)
	}
}
