package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// sumsSetup makes the data directory of tree and returns it with the Files
// and the handler.
func sumsSetup(t *testing.T) (string, *Files, http.Handler) {
	t.Helper()
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "Fotos", "2026"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Fotos/a.jpg", "Fotos/2026/b.jpg", "notizen.txt"} {
		writeFile(t, data, name, name)
	}
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return data, f, mux
}

func writeFile(t *testing.T, data, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(data, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func getJSON(t *testing.T, h http.Handler, endpoint string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", endpoint, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

func duplicateGroups(t *testing.T, h http.Handler) ([]Duplicate, int64) {
	t.Helper()
	var body struct {
		Groups []Duplicate `json:"groups"`
		Wasted int64       `json:"wasted"`
	}
	getJSON(t, h, "/api/duplicates", &body)
	return body.Groups, body.Wasted
}

func integrityState(t *testing.T, h http.Handler) Integrity {
	t.Helper()
	var state Integrity
	getJSON(t, h, "/api/integrity", &state)
	return state
}

// damage changes a file's content but keeps its size and time, as a bad
// disk would.
func damage(t *testing.T, data, name, content string) {
	t.Helper()
	full := filepath.Join(data, filepath.FromSlash(name))
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, data, name, content)
	if err := os.Chtimes(full, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckMakesChecksumsAndFindsDuplicates(t *testing.T) {
	data, f, h := sumsSetup(t)
	writeFile(t, data, "Fotos/kopie.jpg", "Fotos/a.jpg")
	writeFile(t, data, "notizen-kopie.txt", "Fotos/a.jpg")
	writeFile(t, data, "leer1.txt", "")
	writeFile(t, data, "leer2.txt", "")
	writeFile(t, data, "doppelt.txt", "notizen.txt")
	f.check(context.Background(), false)

	state := integrityState(t, h)
	if state.Files != 8 || state.Running || state.Total != 8 || state.Done != 8 || state.LastCheck.IsZero() || len(state.Damaged) != 0 {
		t.Errorf("state %+v", state)
	}
	groups, wasted := duplicateGroups(t, h)
	if len(groups) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	// Most space to gain first; empty files are never duplicates.
	first := groups[0]
	if first.SHA256 != sha("Fotos/a.jpg") || first.Size != int64(len("Fotos/a.jpg")) ||
		len(first.Paths) != 3 || first.Paths[0] != "/Fotos/a.jpg" || first.Paths[2] != "/notizen-kopie.txt" {
		t.Errorf("first group %+v", first)
	}
	if groups[1].Paths[0] != "/doppelt.txt" || groups[1].Paths[1] != "/notizen.txt" {
		t.Errorf("second group %+v", groups[1])
	}
	if want := int64(2*len("Fotos/a.jpg") + len("notizen.txt")); wasted != want {
		t.Errorf("wasted %d, want %d", wasted, want)
	}

	// The checksums are kept, hidden like the other folders.
	if !exists(data, sumsFile) {
		t.Error("the checksums are not saved")
	}
	for _, e := range listing(t, get(h, "/api/files", "/", nil)).Entries {
		if e.Name == sumsDir {
			t.Error("the checksums are listed")
		}
	}
}

func TestCheckFindsDamage(t *testing.T) {
	data, f, h := sumsSetup(t)
	var told [][]string
	f.account = "anna"
	f.damaged = func(account string, paths []string) {
		if account != "anna" {
			t.Errorf("told about the space of %q", account)
		}
		told = append(told, paths)
	}
	ctx := context.Background()
	f.check(ctx, false)
	damage(t, data, "Fotos/a.jpg", "Fotos/X.jpg")

	// Read again only when due, or when asked for all.
	f.check(ctx, false)
	if state := integrityState(t, h); len(state.Damaged) != 0 || state.Total != 0 {
		t.Errorf("not due yet: %+v", state)
	}
	f.check(ctx, true)
	state := integrityState(t, h)
	if len(state.Damaged) != 1 {
		t.Fatalf("damaged %+v", state.Damaged)
	}
	d := state.Damaged[0]
	if d.Path != "/Fotos/a.jpg" || d.Expected != sha("Fotos/a.jpg") || d.Actual != sha("Fotos/X.jpg") || d.Found.IsZero() {
		t.Errorf("damaged %+v", d)
	}
	// Told once, not again on the next check.
	f.check(ctx, true)
	if len(told) != 1 || !slices.Equal(told[0], []string{"/Fotos/a.jpg"}) {
		t.Errorf("told %v", told)
	}

	// A file put right again, for example by the mirror, is fine again.
	damage(t, data, "Fotos/a.jpg", "Fotos/a.jpg")
	f.check(ctx, true)
	if state := integrityState(t, h); len(state.Damaged) != 0 {
		t.Errorf("still damaged: %+v", state.Damaged)
	}

	// Accepting takes the content as it is now.
	damage(t, data, "Fotos/a.jpg", "Fotos/Y.jpg")
	f.check(ctx, true)
	if rec := post(h, "/api/integrity/accept", `{"path":"/Fotos/a.jpg"}`); rec.Code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := post(h, "/api/integrity/accept", `{"path":"/Fotos/a.jpg"}`); rec.Code != http.StatusNotFound {
		t.Errorf("accepting twice: status %d", rec.Code)
	}
	f.check(ctx, true)
	if state := integrityState(t, h); len(state.Damaged) != 0 {
		t.Errorf("damaged after accepting: %+v", state.Damaged)
	}
}

func TestCheckFollowsChanges(t *testing.T) {
	data, f, h := sumsSetup(t)
	ctx := context.Background()
	f.check(ctx, false)

	// A changed file (other size) gets a new checksum, not an alarm.
	writeFile(t, data, "notizen.txt", "neuer, längerer Inhalt")
	// A file not read for VerifyDays is read again.
	f.sums.mu.Lock()
	f.sums.byPath["Fotos/a.jpg"].Checked = time.Now().AddDate(0, 0, -VerifyDays-1)
	f.sums.mu.Unlock()
	if err := os.Remove(filepath.Join(data, "Fotos", "2026", "b.jpg")); err != nil {
		t.Fatal(err)
	}
	f.check(ctx, false)

	state := integrityState(t, h)
	if state.Total != 2 || state.Files != 2 || len(state.Damaged) != 0 {
		t.Errorf("state %+v", state)
	}
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	if s := f.sums.byPath["notizen.txt"]; s == nil || s.SHA256 != sha("neuer, längerer Inhalt") {
		t.Errorf("changed file %+v", s)
	}
	if s := f.sums.byPath["Fotos/a.jpg"]; s == nil || time.Since(s.Checked) > time.Hour {
		t.Errorf("not read again: %+v", s)
	}
	if _, ok := f.sums.byPath["Fotos/2026/b.jpg"]; ok {
		t.Error("a removed file keeps its checksum")
	}
}

func TestChecksumsFollowMovesAndTrash(t *testing.T) {
	_, f, h := sumsSetup(t)
	f.check(context.Background(), false)
	has := func(name string) bool {
		f.sums.mu.Lock()
		defer f.sums.mu.Unlock()
		return f.sums.byPath[name] != nil
	}

	post(h, "/api/move", `{"from":"/Fotos","to":"/Bilder"}`)
	if !has("Bilder/2026/b.jpg") || has("Fotos/2026/b.jpg") {
		t.Error("the checksums did not follow the move")
	}
	rec := post(h, "/api/delete", `{"path":"/Bilder/a.jpg"}`)
	var deleted struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if has("Bilder/a.jpg") || !has(trashedName(deleted.ID)) {
		t.Error("the checksum did not go into the recycle bin")
	}
	// The recycle bin is neither counted nor checked away.
	f.check(context.Background(), false)
	if state := integrityState(t, h); state.Files != 2 || !has(trashedName(deleted.ID)) {
		t.Errorf("state %+v", state)
	}
	post(h, "/api/trash/restore", `{"id":"`+deleted.ID+`"}`)
	if !has("Bilder/a.jpg") {
		t.Error("the checksum did not come back")
	}
	rec = post(h, "/api/delete", `{"path":"/Bilder/a.jpg"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	post(h, "/api/trash/delete", `{"id":"`+deleted.ID+`"}`)
	if has(trashedName(deleted.ID)) {
		t.Error("a file deleted for good keeps its checksum")
	}
}

func TestUploadReportsDuplicates(t *testing.T) {
	_, f, h := sumsSetup(t)
	f.check(context.Background(), false)
	code, id := start(t, h, "/Neu/c.jpg", len("Fotos/a.jpg"))
	if code != http.StatusCreated {
		t.Fatalf("start: status %d", code)
	}
	chunk(h, id, 0, "Fotos/")
	code, answer := chunk(h, id, 6, "a.jpg")
	if code != http.StatusOK {
		t.Fatalf("status %d, answer %v", code, answer)
	}
	dups, _ := answer["duplicates"].([]any)
	if len(dups) != 1 || dups[0] != "/Fotos/a.jpg" {
		t.Errorf("duplicates %v", answer["duplicates"])
	}
	// The checksum is taken from the upload, so the file is not read again.
	f.sums.mu.Lock()
	s := f.sums.byPath["Neu/c.jpg"]
	f.sums.mu.Unlock()
	if s == nil || s.SHA256 != sha("Fotos/a.jpg") {
		t.Errorf("checksum %+v", s)
	}
	f.check(context.Background(), false)
	if state := integrityState(t, h); state.Total != 0 || state.Files != 4 {
		t.Errorf("state %+v", state)
	}
}

func TestChecksumsSurviveARestart(t *testing.T) {
	data, f, _ := sumsSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.StartChecks(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.sums.mu.Lock()
		done := !f.sums.lastCheck.IsZero()
		f.sums.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first check did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if len(again.sums.byPath) != 3 || again.sums.byPath["notizen.txt"].SHA256 != sha("notizen.txt") {
		t.Errorf("loaded %+v", again.sums.byPath)
	}

	// A broken file starts over instead of failing.
	writeFile(t, data, sumsFile, "{kaputt")
	broken, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broken.Close() }()
	if len(broken.sums.byPath) != 0 {
		t.Errorf("loaded %+v", broken.sums.byPath)
	}
}

func TestCheckNow(t *testing.T) {
	_, f, h := sumsSetup(t)
	if rec := post(h, "/api/integrity/check", `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("status %d", rec.Code)
	}
	// Asked twice before it ran: still one check.
	if rec := post(h, "/api/integrity/check", `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("status %d", rec.Code)
	}
	if all := <-f.sums.kick; !all {
		t.Error("not a check of all files")
	}
	// Only from the page's own script.
	req := httptest.NewRequest(http.MethodPost, "/api/integrity/check", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("without JSON: status %d", rec.Code)
	}
	for _, body := range []string{`{"path":"/../x"}`, `{"path":"/.data-den-sums/index.json"}`} {
		if rec := post(h, "/api/integrity/accept", body); rec.Code != http.StatusBadRequest {
			t.Errorf("accept %s: status %d", body, rec.Code)
		}
	}
}

func TestChecksumsPerSpace(t *testing.T) {
	s, h, _ := spacesSetup(t)
	upload := func(user, p string) map[string]any {
		t.Helper()
		code, answer := do(h, http.MethodPost, "/api/uploads", "application/json", `{"path":"`+p+`","size":3}`, as(user))
		if code != http.StatusCreated {
			t.Fatalf("%s: status %d", p, code)
		}
		code, answer = do(h, http.MethodPut, "/api/uploads/"+answer["id"].(string), "application/octet-stream", "abc",
			http.Header{"Upload-Offset": {"0"}, "X-User": {user}})
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", p, code)
		}
		return answer
	}
	upload("chef", "/a.txt")
	// Another account's file with the same content is not a duplicate.
	if dups := upload("anna", "/b.txt")["duplicates"].([]any); len(dups) != 0 {
		t.Errorf("anna sees %v", dups)
	}
	if dups := upload("chef", "/c.txt")["duplicates"].([]any); len(dups) != 1 || dups[0] != "/a.txt" {
		t.Errorf("chef sees %v", dups)
	}
	// The owner's check leaves the other spaces out.
	s.owner.check(context.Background(), true)
	var state Integrity
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/integrity", nil)
	req.Header = as("chef")
	h.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil || state.Files != 2 {
		t.Errorf("chef's state %+v, %v", state, err)
	}
}
