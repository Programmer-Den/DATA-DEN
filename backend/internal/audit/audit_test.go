package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setup returns a log in a temporary folder whose clock moves a second per
// entry. Requests name their user in X-User and are admins with X-Admin.
func setup(t *testing.T) (*Log, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "audit", "audit.log")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	l := New(Config{
		File:  file,
		IP:    func(r *http.Request) string { return r.RemoteAddr },
		User:  func(r *http.Request) string { return r.Header.Get("X-User") },
		Admin: func(r *http.Request) bool { return r.Header.Get("X-Admin") != "" },
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	return l, file
}

func list(t *testing.T, l *Log, target string, header http.Header) []Entry {
	t.Helper()
	mux := http.NewServeMux()
	l.Register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header = header
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", target, rec.Code)
	}
	var answer struct{ Entries []Entry }
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Entries
}

func TestRecordAndList(t *testing.T) {
	l, file := setup(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.1"
	l.Record(req, "anna", "login", "")
	l.Record(req, "anna", "upload", "/a.jpg")
	l.Record(req, "chef", "user-create", "bob")
	l.Record(nil, strings.Repeat("x", 1000), "login-failed", "")

	all := list(t, l, "/api/audit", http.Header{"X-User": {"chef"}, "X-Admin": {"1"}})
	if len(all) != 4 || all[0].Action != "login-failed" || all[3].Action != "login" {
		t.Fatalf("all entries: %+v", all)
	}
	if all[3].IP != "192.0.2.1" || all[2].Target != "/a.jpg" || all[0].IP != "" || len(all[0].User) != maxField {
		t.Errorf("entries: %+v", all)
	}
	if got := list(t, l, "/api/audit?user=anna&limit=1", http.Header{"X-User": {"chef"}, "X-Admin": {"1"}}); len(got) != 1 || got[0].Action != "upload" {
		t.Errorf("admin filtering for anna: %+v", got)
	}
	// Without admin rights only one's own, whatever the filter says.
	if got := list(t, l, "/api/audit?user=chef", http.Header{"X-User": {"anna"}}); len(got) != 2 || got[0].User != "anna" {
		t.Errorf("anna's own: %+v", got)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log file: %v, %v", info, err)
	}
}

func TestRotate(t *testing.T) {
	l, file := setup(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	// A full file, with a line cut off by a crash at its end.
	old, _ := json.Marshal(Entry{Time: time.Unix(1, 0), User: "anna", Action: "login"})
	full := append(append(old, '\n'), make([]byte, MaxBytes)...)
	if err := os.WriteFile(file, full, 0o600); err != nil {
		t.Fatal(err)
	}
	l.Record(nil, "anna", "logout", "")
	if _, err := os.Stat(file + ".1"); err != nil {
		t.Fatalf("the full file was not moved: %v", err)
	}
	entries, err := l.Entries(Filter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "logout" || entries[1].Action != "login" {
		t.Errorf("entries over both files: %+v", entries)
	}
}

func TestBadLimitAndNoFile(t *testing.T) {
	l, _ := setup(t)
	mux := http.NewServeMux()
	l.Register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/audit?limit=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad limit: status %d", rec.Code)
	}
	// Without a file nothing is kept, and nothing breaks.
	none := New(Config{})
	none.Record(nil, "anna", "login", "")
	if entries, err := none.Entries(Filter{}, 10); err != nil || len(entries) != 0 {
		t.Errorf("no file: %v, %v", entries, err)
	}
	var nilLog *Log
	nilLog.Record(nil, "anna", "login", "")
}

func TestFilter(t *testing.T) {
	l, _ := setup(t)
	l.Record(nil, "anna", "upload", "/Fotos/Urlaub.jpg")
	l.Record(nil, "anna", "delete", "/Fotos/alt.jpg")
	// bob in the folder anna shares with him.
	bobs := httptest.NewRequest(http.MethodPut, "/", nil)
	bobs.RemoteAddr = "192.0.2.7"
	l.Record(bobs, "bob", "upload", "anna:/Fotos/Strand.jpg")
	l.Record(nil, "bob", "delete", "/Notizen.txt")
	l.Record(nil, "anna", "login", "")
	admin := http.Header{"X-User": {"chef"}, "X-Admin": {"1"}}

	if got := list(t, l, "/api/audit?action=upload,delete&target=fotos", admin); len(got) != 3 || got[0].User != "bob" || got[2].Action != "upload" {
		t.Errorf("uploads and deletions in Fotos: %+v", got)
	}
	// anna sees what bob did in her folder, but not bob's own files.
	got := list(t, l, "/api/audit?action=upload", http.Header{"X-User": {"anna"}})
	if len(got) != 2 || got[0].Target != "anna:/Fotos/Strand.jpg" || got[0].IP != "" || got[1].User != "anna" {
		t.Errorf("anna's uploads: %+v", got)
	}
	// Paging back from the oldest entry shown.
	page := list(t, l, "/api/audit?limit=2", admin)
	older := list(t, l, "/api/audit?limit=2&before="+page[1].Time.Format(time.RFC3339Nano), admin)
	if len(older) != 2 || older[0].IP != "192.0.2.7" || older[1].Action != "delete" {
		t.Errorf("older entries: %+v", older)
	}

	mux := http.NewServeMux()
	l.Register(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/audit?before=gestern", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad time: status %d", rec.Code)
	}
}
