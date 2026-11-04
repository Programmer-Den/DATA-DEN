package files

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// grantsSetup is spacesSetup with shared folders: the accounts are chef (the
// owner), anna (10 bytes) and bob. chef has Projekt/a.txt, Projekt/2026/b.txt
// and geheim.txt next to it.
func grantsSetup(t *testing.T) (*Spaces, http.Handler, string, *[]Grant, *[]string) {
	t.Helper()
	data := t.TempDir()
	shared := &[]Grant{}
	audited := &[]string{}
	s, err := OpenSpaces(data, SpacesConfig{
		Owner: "chef",
		User:  func(r *http.Request) string { name, _ := r.Context().Value(userKey{}).(string); return name },
		Quota: func(name string) int64 {
			if name == "anna" {
				return 10
			}
			return 0
		},
		Accounts: func() []string { return []string{"chef", "anna", "bob"} },
		Shared:   func(g Grant) { *shared = append(*shared, g) },
		Audit: func(_ *http.Request, user, action, target string) {
			*audited = append(*audited, user+" "+action+" "+target)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mux := http.NewServeMux()
	require := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	}
	s.Register(mux, require)
	s.RegisterGrants(mux, require)
	s.RegisterTeams(mux, require)
	if err := os.MkdirAll(filepath.Join(data, "Projekt", "2026"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Projekt/a.txt", "Projekt/2026/b.txt", "geheim.txt"} {
		if err := os.WriteFile(filepath.Join(data, filepath.FromSlash(name)), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s, mux, data, shared, audited
}

func grantTo(t *testing.T, h http.Handler, owner, body string) Grant {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/grants", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", owner)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("grant: status %d, body %s", rec.Code, rec.Body)
	}
	var g Grant
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func grants(t *testing.T, h http.Handler, user string) (mine, withMe []Grant) {
	t.Helper()
	rec := get(h, "/api/grants", "", as(user))
	if rec.Code != http.StatusOK {
		t.Fatalf("grants: status %d", rec.Code)
	}
	var body struct{ Mine, WithMe []Grant }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Mine, body.WithMe
}

// in sends a request as user to the shared folder id.
func in(h http.Handler, user, id, method, target, body string) *httptest.ResponseRecorder {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	req := httptest.NewRequest(method, target+sep+"grant="+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGrantRead(t *testing.T) {
	_, h, _, shared, _ := grantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
	if g.Owner != "chef" || g.Path != "/Projekt" || g.Name != "Projekt" || g.To != "anna" || g.Write {
		t.Errorf("grant %+v", g)
	}
	if len(*shared) != 1 || (*shared)[0].ID != g.ID {
		t.Errorf("told %+v", *shared)
	}
	if mine, withMe := grants(t, h, "chef"); len(mine) != 1 || len(withMe) != 0 {
		t.Errorf("chef's: %+v %+v", mine, withMe)
	}
	if mine, withMe := grants(t, h, "anna"); len(mine) != 0 || len(withMe) != 1 || withMe[0].ID != g.ID {
		t.Errorf("anna's: %+v %+v", mine, withMe)
	}

	rec := in(h, "anna", g.ID, http.MethodGet, "/api/files?path=/", "")
	if l := listing(t, rec); l.Path != "/" || len(l.Entries) != 2 || l.Entries[0].Name != "2026" || l.Entries[1].Name != "a.txt" {
		t.Errorf("top %+v", l)
	}
	// Without a path, the shared folder itself, not the top of chef's space.
	if l := listing(t, in(h, "anna", g.ID, http.MethodGet, "/api/files", "")); l.Path != "/" || len(l.Entries) != 2 {
		t.Errorf("no path %+v", l)
	}
	if l := listing(t, in(h, "anna", g.ID, http.MethodGet, "/api/files?path=/2026", "")); l.Path != "/2026" || len(l.Entries) != 1 {
		t.Errorf("sub %+v", l)
	}
	if rec := in(h, "anna", g.ID, http.MethodGet, "/api/download?path=/2026/b.txt", ""); rec.Code != http.StatusOK || rec.Body.String() != "Projekt/2026/b.txt" {
		t.Errorf("download: status %d, body %s", rec.Code, rec.Body)
	}
	// Nothing outside the folder.
	for _, p := range []string{"/../geheim.txt", "/2026/../../geheim.txt"} {
		if rec := in(h, "anna", g.ID, http.MethodGet, "/api/download?path="+p, ""); rec.Code == http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
	// Reading only.
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/folders", `{"path":"/neu"}`); rec.Code != http.StatusForbidden {
		t.Errorf("new folder: status %d, want 403", rec.Code)
	}
	// The owner's recycle bin, search and the like stay the owner's.
	for _, target := range []string{"/api/trash", "/api/search?q=a", "/api/usage"} {
		if rec := in(h, "anna", g.ID, http.MethodGet, target, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", target, rec.Code)
		}
	}
	// Not for anyone else.
	if rec := in(h, "bob", g.ID, http.MethodGet, "/api/files?path=/", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bob: status %d, want 404", rec.Code)
	}
	if rec := in(h, "anna", "0123456789abcdef0123456789abcdef", http.MethodGet, "/api/files?path=/", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown grant: status %d, want 404", rec.Code)
	}
	// Without the grant, anna sees her own space.
	if l := listing(t, get(h, "/api/files", "/", as("anna"))); len(l.Entries) != 0 {
		t.Errorf("anna's own space %+v", l)
	}
}

func TestGrantSymlink(t *testing.T) {
	_, h, data, _, _ := grantsSetup(t)
	if err := os.Symlink(filepath.Join(data, "geheim.txt"), filepath.Join(data, "Projekt", "link.txt")); err != nil {
		t.Skip(err)
	}
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	if rec := in(h, "anna", g.ID, http.MethodGet, "/api/download?path=/link.txt", ""); rec.Code == http.StatusOK {
		t.Errorf("through a symlink: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/move", `{"from":"/link.txt","to":"/x.txt"}`); rec.Code == http.StatusOK {
		t.Errorf("moving a symlink: status %d", rec.Code)
	}
}

func TestGrantWrite(t *testing.T) {
	_, h, data, _, audited := grantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	if !g.Write {
		t.Errorf("grant %+v", g)
	}
	rec := in(h, "anna", g.ID, http.MethodPost, "/api/uploads", `{"path":"/neu.txt","size":3}`)
	var started struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &started); rec.Code != http.StatusCreated || err != nil || started.ID == "" {
		t.Fatalf("start: status %d, body %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/uploads/"+started.ID+"?grant="+g.ID, strings.NewReader("abc"))
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("X-User", "anna")
	put := httptest.NewRecorder()
	h.ServeHTTP(put, req)
	if put.Code != http.StatusOK || strings.Contains(put.Body.String(), "duplicates") {
		t.Fatalf("chunk: status %d, body %s", put.Code, put.Body)
	}
	if got := read(t, filepath.Join(data, "Projekt", "neu.txt")); got != "abc" {
		t.Errorf("the file holds %q", got)
	}
	// anna's quota of 10 bytes does not apply in chef's folder.
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/uploads", `{"path":"/gross.txt","size":100}`); rec.Code != http.StatusCreated {
		t.Errorf("100 bytes in chef's folder: status %d", rec.Code)
	}

	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/folders", `{"path":"/Neu"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"path":"/Neu"`) {
		t.Errorf("folder: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/move", `{"from":"/a.txt","to":"/Neu/a.txt"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"path":"/Neu/a.txt"`) {
		t.Errorf("move: status %d, body %s", rec.Code, rec.Body)
	}
	if !exists(data, "Projekt/Neu/a.txt") {
		t.Error("not moved")
	}
	rec = in(h, "anna", g.ID, http.MethodPost, "/api/delete", `{"path":"/Neu"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"id"`) {
		t.Errorf("delete: status %d, body %s", rec.Code, rec.Body)
	}
	// Into chef's recycle bin.
	if items := chefTrash(t, h); len(items) != 1 || items[0] != "/Projekt/Neu" {
		t.Errorf("chef's recycle bin %v", items)
	}
	// Never the shared folder itself, nor out of it.
	for target, body := range map[string]string{
		"/api/delete": `{"path":"/"}`,
		"/api/move":   `{"from":"/2026","to":"/../2026"}`,
	} {
		if rec := in(h, "anna", g.ID, http.MethodPost, target, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status %d, want 400", target, body, rec.Code)
		}
	}
	if !exists(data, "Projekt/2026") {
		t.Error("2026 was moved out")
	}
	// A key in other letters would reach the handler unscoped.
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/delete", `{"path":"/2026","PATH":"/geheim.txt"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PATH: status %d, want 400", rec.Code)
	}
	if !exists(data, "geheim.txt") {
		t.Error("geheim.txt was deleted")
	}
	want := []string{
		"chef grant-create /Projekt → anna (lesen und schreiben)",
		"anna upload chef:/Projekt/neu.txt",
		"anna folder chef:/Projekt/Neu",
		"anna move chef:/Projekt/a.txt → /Projekt/Neu/a.txt",
		"anna delete chef:/Projekt/Neu",
	}
	if strings.Join(*audited, "|") != strings.Join(want, "|") {
		t.Errorf("audit:\n%s\nwant\n%s", strings.Join(*audited, "\n"), strings.Join(want, "\n"))
	}
}

func chefTrash(t *testing.T, h http.Handler) []string {
	t.Helper()
	rec := get(h, "/api/trash", "", as("chef"))
	var body struct {
		Items []TrashItem `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	var paths []string
	for _, item := range body.Items {
		paths = append(paths, item.Path)
	}
	return paths
}

func TestGrantQuotaIsTheOwners(t *testing.T) {
	_, h, data, _, _ := grantsSetup(t)
	if err := os.MkdirAll(filepath.Join(data, usersDir, "anna", "Fotos"), 0o750); err != nil {
		t.Fatal(err)
	}
	g := grantTo(t, h, "anna", `{"path":"/Fotos","to":"bob","write":true}`)
	if rec := in(h, "bob", g.ID, http.MethodPost, "/api/uploads", `{"path":"/a.jpg","size":11}`); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("over anna's quota: status %d, want 507", rec.Code)
	}
	if rec := in(h, "bob", g.ID, http.MethodPost, "/api/uploads", `{"path":"/a.jpg","size":0}`); rec.Code != http.StatusCreated {
		t.Errorf("empty file: status %d", rec.Code)
	}
	if !exists(data, filepath.Join(usersDir, "anna", "Fotos", "a.jpg")) {
		t.Error("the file is not in anna's space")
	}
}

func TestGrantChecks(t *testing.T) {
	_, h, _, shared, _ := grantsSetup(t)
	for _, body := range []string{
		`{"path":"/geheim.txt","to":"anna"}`,
		`{"path":"/Projekt","to":"chef"}`,
		`{"path":"/Projekt","to":"niemand"}`,
		`{"path":"/fehlt","to":"anna"}`,
		`{"path":"/../x","to":"anna"}`,
	} {
		if code, _ := do(h, http.MethodPost, "/api/grants", "application/json", body, as("chef")); code == http.StatusCreated || code == http.StatusOK {
			t.Errorf("%s: status %d", body, code)
		}
	}
	// Sharing again changes the rights; nobody is told twice.
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
	again := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	if again.ID != g.ID || !again.Write || len(*shared) != 1 {
		t.Errorf("again %+v, told %d times", again, len(*shared))
	}
	code, answer := do(h, http.MethodGet, "/api/accounts", "", "", as("anna"))
	if names, _ := answer["names"].([]any); code != http.StatusOK || len(names) != 2 || names[0] != "chef" || names[1] != "bob" {
		t.Errorf("accounts for anna: %d %v", code, answer)
	}
}

func TestGrantFollowsAndEnds(t *testing.T) {
	s, h, _, _, _ := grantsSetup(t)
	g := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna"}`)
	if code, _ := do(h, http.MethodPost, "/api/move", "application/json", `{"from":"/Projekt","to":"/Arbeit"}`, as("chef")); code != http.StatusOK {
		t.Fatalf("move: status %d", code)
	}
	if _, withMe := grants(t, h, "anna"); len(withMe) != 1 || withMe[0].Path != "/Arbeit" || withMe[0].Name != "Arbeit" {
		t.Errorf("after the move %+v", withMe)
	}
	if rec := in(h, "anna", g.ID, http.MethodGet, "/api/download?path=/a.txt", ""); rec.Code != http.StatusOK {
		t.Errorf("download after the move: status %d", rec.Code)
	}
	if code, _ := do(h, http.MethodPost, "/api/delete", "application/json", `{"path":"/Arbeit"}`, as("chef")); code != http.StatusOK {
		t.Fatalf("delete: status %d", code)
	}
	if _, withMe := grants(t, h, "anna"); len(withMe) != 1 || !withMe[0].Missing {
		t.Errorf("after deleting %+v", withMe)
	}
	if rec := in(h, "anna", g.ID, http.MethodGet, "/api/files?path=/", ""); rec.Code != http.StatusNotFound {
		t.Errorf("listing a deleted folder: status %d", rec.Code)
	}

	// anna leaves; bob cannot end someone else's.
	if err := os.MkdirAll(filepath.Join(s.owner.root.Name(), "Projekt"), 0o750); err != nil {
		t.Fatal(err)
	}
	g2 := grantTo(t, h, "chef", `{"path":"/Projekt","to":"bob"}`)
	if code, _ := do(h, http.MethodPost, "/api/grants/delete", "application/json", `{"id":"`+g2.ID+`"}`, as("anna")); code != http.StatusNotFound {
		t.Errorf("anna ending bob's: status %d, want 404", code)
	}
	if code, _ := do(h, http.MethodPost, "/api/grants/delete", "application/json", `{"id":"`+g.ID+`"}`, as("anna")); code != http.StatusOK {
		t.Errorf("anna leaving: status %d", code)
	}
	if _, withMe := grants(t, h, "anna"); len(withMe) != 0 {
		t.Errorf("anna still has %+v", withMe)
	}
	// A deleted account takes its grants along.
	if err := s.Retire("bob"); err != nil {
		t.Fatal(err)
	}
	if mine, _ := grants(t, h, "chef"); len(mine) != 0 {
		t.Errorf("chef still has %+v", mine)
	}
}

func TestTeamFolder(t *testing.T) {
	_, h, data, shared, audited := grantsSetup(t)
	admin := func(method, target, body string) (int, map[string]any) {
		return do(h, method, target, "application/json", body, as("chef"))
	}
	if code, _ := admin(http.MethodPost, "/api/teams", `{"name":"familie"}`); code != http.StatusCreated {
		t.Fatalf("create: status %d", code)
	}
	for _, body := range []string{`{"name":"familie"}`, `{"name":"Groß"}`, `{"name":"../x"}`} {
		if code, _ := admin(http.MethodPost, "/api/teams", body); code == http.StatusCreated {
			t.Errorf("%s: status %d", body, code)
		}
	}
	if code, _ := admin(http.MethodPost, "/api/teams/familie/members", `{"to":"anna","write":true}`); code != http.StatusOK {
		t.Fatalf("anna: status %d", code)
	}
	if code, _ := admin(http.MethodPost, "/api/teams/familie/members", `{"to":"bob"}`); code != http.StatusOK {
		t.Fatalf("bob: status %d", code)
	}
	if code, _ := admin(http.MethodPost, "/api/teams/familie/members", `{"to":"niemand"}`); code != http.StatusBadRequest {
		t.Errorf("unknown account: status %d", code)
	}
	if code, _ := admin(http.MethodPost, "/api/teams/fehlt/members", `{"to":"anna"}`); code != http.StatusNotFound {
		t.Errorf("unknown team: status %d", code)
	}
	if len(*shared) != 2 || !(*shared)[0].Team {
		t.Errorf("told %+v", *shared)
	}
	code, answer := admin(http.MethodGet, "/api/teams", "")
	teams, _ := answer["teams"].([]any)
	if code != http.StatusOK || len(teams) != 1 || len(teams[0].(map[string]any)["members"].([]any)) != 2 {
		t.Errorf("teams: %d %v", code, answer)
	}

	_, withMe := grants(t, h, "anna")
	if len(withMe) != 1 || withMe[0].Owner != "@familie" || !withMe[0].Team || withMe[0].Missing {
		t.Fatalf("anna's %+v", withMe)
	}
	g := withMe[0]
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/folders", `{"path":"/Urlaub"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"path":"/Urlaub"`) {
		t.Fatalf("folder: status %d, body %s", rec.Code, rec.Body)
	}
	if !exists(data, teamsDir+"/familie/Urlaub") {
		t.Error("not in the team folder")
	}
	if l := listing(t, in(h, "anna", g.ID, http.MethodGet, "/api/files?path=/", "")); l.Path != "/" || len(l.Entries) != 1 {
		t.Errorf("listing %+v", l)
	}
	// Members who may change it restore from its recycle bin themselves.
	rec := in(h, "anna", g.ID, http.MethodPost, "/api/delete", `{"path":"/Urlaub"}`)
	id := jsonID(rec.Body.Bytes())
	if rec.Code != http.StatusOK || id == "" {
		t.Fatalf("delete: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := in(h, "anna", g.ID, http.MethodGet, "/api/trash", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/Urlaub") {
		t.Errorf("trash: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := in(h, "anna", g.ID, http.MethodPost, "/api/trash/restore", `{"id":"`+id+`"}`); rec.Code != http.StatusOK {
		t.Errorf("restore: status %d, body %s", rec.Code, rec.Body)
	}
	if !exists(data, teamsDir+"/familie/Urlaub") {
		t.Error("not restored")
	}
	// bob only reads.
	_, bobs := grants(t, h, "bob")
	if rec := in(h, "bob", bobs[0].ID, http.MethodGet, "/api/trash", ""); rec.Code != http.StatusForbidden {
		t.Errorf("bob's trash: status %d, want 403", rec.Code)
	}
	if rec := in(h, "bob", bobs[0].ID, http.MethodGet, "/api/files?path=/Urlaub", ""); rec.Code != http.StatusOK {
		t.Errorf("bob's listing: status %d", rec.Code)
	}
	// A plain shared folder keeps its owner's recycle bin to itself.
	plain := grantTo(t, h, "chef", `{"path":"/Projekt","to":"anna","write":true}`)
	if rec := in(h, "anna", plain.ID, http.MethodGet, "/api/trash", ""); rec.Code != http.StatusForbidden {
		t.Errorf("chef's trash through a plain grant: status %d, want 403", rec.Code)
	}
	// The team folder is not part of the owner's space or usage.
	if l := listing(t, get(h, "/api/files", "/", as("chef"))); len(l.Entries) != 2 {
		t.Errorf("chef sees %+v", l.Entries)
	}

	if code, _ := admin(http.MethodPost, "/api/teams/familie/members/delete", `{"to":"bob"}`); code != http.StatusOK {
		t.Errorf("remove bob: status %d", code)
	}
	if _, bobs := grants(t, h, "bob"); len(bobs) != 0 {
		t.Errorf("bob still has %+v", bobs)
	}
	if code, _ := admin(http.MethodPost, "/api/teams/familie/delete", ""); code != http.StatusOK {
		t.Errorf("delete: status %d", code)
	}
	if _, withMe := grants(t, h, "anna"); len(withMe) != 1 || withMe[0].ID != plain.ID {
		t.Errorf("anna still has %+v", withMe)
	}
	if exists(data, teamsDir+"/familie") {
		t.Error("the team folder is still there")
	}
	entries, _ := os.ReadDir(filepath.Join(data, usersDir, ".deleted"))
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".team-familie-") {
		t.Errorf("moved aside as %v", entries)
	}
	if !slices.Contains(*audited, "chef team-create familie") || !slices.Contains(*audited, "anna folder @familie:/Urlaub") {
		t.Errorf("audit %v", *audited)
	}
}

// jsonID is the "id" of a JSON answer.
func jsonID(body []byte) string {
	var answer struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &answer)
	return answer.ID
}
