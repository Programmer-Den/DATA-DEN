package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func trash(t *testing.T, h http.Handler) []TrashItem {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/trash", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Days  int         `json:"days"`
		Items []TrashItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Days != TrashDays {
		t.Errorf("days %d", body.Days)
	}
	return body.Items
}

func TestDeleteGoesToTrash(t *testing.T) {
	data, h := tree(t)
	rec := post(h, "/api/delete", `{"path":"/notizen.txt"}`)
	var deleted struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil || !trashID.MatchString(deleted.ID) {
		t.Fatalf("delete answered %s", rec.Body)
	}
	post(h, "/api/delete", `{"path":"/Fotos"}`)
	items := trash(t, h)
	if len(items) != 2 || items[1].ID != deleted.ID {
		t.Fatalf("items %+v", items)
	}
	folder, file := items[0], items[1]
	if folder.Path != "/Fotos" || !folder.Dir || folder.Size != int64(len("Fotos/a.jpg")+len("Fotos/2026/b.jpg")) {
		t.Errorf("folder %+v", folder)
	}
	if file.Path != "/notizen.txt" || file.Name != "notizen.txt" || file.Dir || file.Size != int64(len("notizen.txt")) {
		t.Errorf("file %+v", file)
	}
	// Out of sight, still on the disk.
	if exists(data, "Fotos") || !exists(data, trashDir+"/"+folder.ID+"/item/2026/b.jpg") {
		t.Error("the folder is not in the recycle bin")
	}
	for _, e := range listing(t, get(h, "/api/files", "/", nil)).Entries {
		if e.Name == trashDir {
			t.Error("the recycle bin is listed")
		}
	}
	if rec := media(h, "photo"); rec.Body.String() != `{"kind":"photo","items":[]}`+"\n" {
		t.Errorf("deleted photos in the media: %s", rec.Body)
	}
}

func TestRestore(t *testing.T) {
	data, h := tree(t)
	post(h, "/api/delete", `{"path":"/Fotos/2026"}`)
	post(h, "/api/delete", `{"path":"/Fotos"}`)
	items := trash(t, h)

	// The folder 2026 was in is gone too: it is made again.
	if rec := post(h, "/api/trash/restore", `{"id":"`+items[1].ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if !exists(data, "Fotos/2026/b.jpg") {
		t.Error("not restored")
	}
	// Now Fotos is taken: restoring the other one would overwrite it.
	rec := post(h, "/api/trash/restore", `{"id":"`+items[0].ID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("onto a taken name: status %d", rec.Code)
	}
	if len(trash(t, h)) != 1 {
		t.Error("the refused item left the recycle bin")
	}
	if exists(data, trashDir+"/"+items[1].ID) {
		t.Error("the restored item's folder stayed")
	}
}

func TestDeleteForGoodAndEmpty(t *testing.T) {
	data, h := tree(t)
	post(h, "/api/delete", `{"path":"/notizen.txt"}`)
	post(h, "/api/delete", `{"path":"/Fotos/a.jpg"}`)
	post(h, "/api/delete", `{"path":"/Fotos/2026"}`)
	items := trash(t, h)
	if rec := post(h, "/api/trash/delete", `{"id":"`+items[0].ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(trash(t, h)) != 2 || exists(data, trashDir+"/"+items[0].ID) {
		t.Error("not deleted for good")
	}
	if rec := post(h, "/api/trash/empty", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("empty: status %d", rec.Code)
	}
	if len(trash(t, h)) != 0 {
		t.Error("not emptied")
	}
}

func TestTrashErrors(t *testing.T) {
	_, h := tree(t)
	tests := []struct {
		endpoint, body string
		want           int
	}{
		{"/api/trash/restore", `{"id":"../../notizen.txt"}`, http.StatusBadRequest},
		{"/api/trash/restore", `{"id":"0123456789abcdef0123456789abcdef"}`, http.StatusNotFound},
		{"/api/trash/delete", `{"id":""}`, http.StatusBadRequest},
		{"/api/delete", `{"path":"/.data-den-trash"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := post(h, tt.endpoint, tt.body); rec.Code != tt.want {
			t.Errorf("%s %s: status %d, want %d", tt.endpoint, tt.body, rec.Code, tt.want)
		}
	}
	// Emptying needs the page's own script, not a form on another site.
	req := httptest.NewRequest(http.MethodPost, "/api/trash/empty", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("empty without JSON: status %d, want 415", rec.Code)
	}
}

func TestTrashPurgedAfterWaiting(t *testing.T) {
	data, h := tree(t)
	post(h, "/api/delete", `{"path":"/notizen.txt"}`)
	post(h, "/api/delete", `{"path":"/Fotos"}`)
	items := trash(t, h)
	// Back-date the folder's deletion past the waiting time.
	old := items[0]
	old.Deleted = time.Now().AddDate(0, 0, -TrashDays-1)
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, trashDir, old.ID, "info.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	left := trash(t, h)
	if len(left) != 1 || left[0].ID != items[1].ID || exists(data, trashDir+"/"+old.ID) {
		t.Errorf("left %+v", left)
	}
}
