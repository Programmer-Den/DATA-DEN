package files

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func syncTree(t *testing.T, h http.Handler, p string) map[string]SyncEntry {
	t.Helper()
	rec := get(h, "/api/sync/tree", p, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree %s: status %d, body %s", p, rec.Code, rec.Body)
	}
	var answer struct {
		Entries []SyncEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	byPath := map[string]SyncEntry{}
	for _, e := range answer.Entries {
		byPath[e.Path] = e
	}
	return byPath
}

func TestSyncTree(t *testing.T) {
	data, h := tree(t)
	if err := os.MkdirAll(filepath.Join(data, ".data-den-trash", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	all := syncTree(t, h, "/")
	for _, want := range []string{"Fotos", "Fotos/2026", "Fotos/a.jpg", "Fotos/2026/b.jpg", "notizen.txt"} {
		if _, ok := all[want]; !ok {
			t.Errorf("%s missing in %v", want, all)
		}
	}
	if len(all) != 5 {
		t.Errorf("got %d entries, want 5: %v", len(all), all)
	}
	if e := all["notizen.txt"]; e.Dir || e.Size != int64(len("notizen.txt")) || e.Mod == 0 {
		t.Errorf("notizen.txt: %+v", e)
	}
	if !all["Fotos"].Dir {
		t.Error("Fotos is not a folder")
	}
	sub := syncTree(t, h, "/Fotos")
	if _, ok := sub["2026/b.jpg"]; !ok || len(sub) != 3 {
		t.Errorf("below /Fotos: %v", sub)
	}
	for p, want := range map[string]int{"/notizen.txt": http.StatusBadRequest, "/fehlt": http.StatusNotFound, "/..": http.StatusBadRequest, "/.data-den-trash": http.StatusNotFound} {
		if rec := get(h, "/api/sync/tree", p, nil); rec.Code != want {
			t.Errorf("%s: status %d, want %d", p, rec.Code, want)
		}
	}
}

func TestSyncTreeChecksumAfterUpload(t *testing.T) {
	h, _ := uploadSetup(t)
	_, id := start(t, h, "/a.txt", 3)
	if code, _ := chunk(h, id, 0, "abc"); code != http.StatusOK {
		t.Fatalf("upload: status %d", code)
	}
	e := syncTree(t, h, "/")["a.txt"]
	if e.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("checksum %q", e.SHA256)
	}
}

func TestUploadReplaceIfUnchanged(t *testing.T) {
	data := t.TempDir()
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	h := http.Handler(mux)
	_, id := start(t, h, "/a.txt", 3)
	_, answer := chunk(h, id, 0, "alt")
	mod, _ := answer["mod"].(float64)
	if mod == 0 {
		t.Fatalf("no mod in %v", answer)
	}
	old := syncTree(t, h, "/")["a.txt"]

	replace := func(size int64, mod int64) (int, string) {
		body := fmt.Sprintf(`{"path":"/a.txt","size":4,"expect":{"size":%d,"mod":%d}}`, size, mod)
		code, answer := do(h, http.MethodPost, "/api/uploads", "application/json", body, nil)
		id, _ := answer["id"].(string)
		return code, id
	}
	if code, _ := replace(old.Size, old.Mod+1); code != http.StatusPreconditionFailed {
		t.Errorf("wrong time: status %d", code)
	}
	if code, _ := replace(old.Size+1, old.Mod); code != http.StatusPreconditionFailed {
		t.Errorf("wrong size: status %d", code)
	}
	code, id := replace(old.Size, old.Mod)
	if code != http.StatusCreated {
		t.Fatalf("replace: status %d", code)
	}
	if code, answer := chunk(h, id, 0, "neu!"); code != http.StatusOK {
		t.Fatalf("chunk: status %d, %v", code, answer)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "neu!" {
		t.Errorf("content %q", got)
	}
	if versions := f.versionsOf("a.txt"); len(versions) != 1 {
		t.Fatalf("the old content is not kept as a version: %v", versions)
	}

	// The file changed between start and finish: nothing is replaced.
	now := syncTree(t, h, "/")["a.txt"]
	code, id = replace(now.Size, now.Mod)
	if code != http.StatusCreated {
		t.Fatalf("second replace: status %d", code)
	}
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("andere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := chunk(h, id, 0, "drei"); code != http.StatusPreconditionFailed {
		t.Errorf("changed meanwhile: status %d", code)
	}
	if got := read(t, filepath.Join(data, "a.txt")); got != "andere" {
		t.Errorf("overwritten: %q", got)
	}
}

func TestDeleteIfUnchanged(t *testing.T) {
	data, h := tree(t)
	e := syncTree(t, h, "/")["notizen.txt"]
	if rec := post(h, "/api/delete", fmt.Sprintf(`{"path":"/notizen.txt","size":%d,"mod":%d}`, e.Size, e.Mod+1)); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("changed: status %d", rec.Code)
	}
	if rec := post(h, "/api/delete", fmt.Sprintf(`{"path":"/notizen.txt","size":%d}`, e.Size)); rec.Code != http.StatusBadRequest {
		t.Errorf("size alone: status %d", rec.Code)
	}
	if !exists(data, "notizen.txt") {
		t.Fatal("deleted although changed")
	}
	if rec := post(h, "/api/delete", fmt.Sprintf(`{"path":"/notizen.txt","size":%d,"mod":%d}`, e.Size, e.Mod)); rec.Code != http.StatusOK {
		t.Errorf("unchanged: status %d, body %s", rec.Code, rec.Body)
	}
	if exists(data, "notizen.txt") {
		t.Error("not deleted")
	}
}
