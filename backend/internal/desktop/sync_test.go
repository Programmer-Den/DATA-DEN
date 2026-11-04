package desktop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
)

const token = "dd_test"

type fixture struct {
	data, local string
	syncer      *Syncer
}

// newFixture runs a DATA-DEN server on a temporary data directory and a
// Syncer for an empty folder on the "PC", synced with remote.
func newFixture(t *testing.T, remote string) *fixture {
	t.Helper()
	data := t.TempDir()
	f, err := files.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, `{"error":"Nicht angemeldet"}`, http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	local := t.TempDir()
	return &fixture{data: data, local: local, syncer: &Syncer{
		Client: &Client{Server: srv.URL, Token: token},
		Local:  local,
		Remote: remote,
		Host:   "PC",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return time.Date(2026, 11, 4, 21, 5, 0, 0, time.Local) },
	}}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // test files
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

func gone(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
	return errors.Is(err, os.ErrNotExist)
}

// round syncs and then checks that a second round has nothing left to do.
func (fx *fixture) round(t *testing.T) int {
	t.Helper()
	n, err := fx.syncer.Round(context.Background())
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	// A conflict copy is uploaded in the round after.
	for range 2 {
		again, err := fx.syncer.Round(context.Background())
		if err != nil {
			t.Fatalf("next round: %v", err)
		}
		if again == 0 {
			return n
		}
	}
	t.Fatal("the rounds do not settle")
	return n
}

func TestFirstRoundBothWays(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "a.txt", "vom PC")
	write(t, fx.local, "Ordner/b.txt", "auch vom PC")
	if err := os.Mkdir(filepath.Join(fx.local, "leer"), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, fx.data, "c.txt", "vom Server")
	write(t, fx.data, "Fotos/2026/d.jpg", "Foto")
	write(t, fx.local, "desktop.ini", "ignoriert")
	if n := fx.round(t); n == 0 {
		t.Fatal("nothing synced")
	}
	if content(t, fx.data, "a.txt") != "vom PC" || content(t, fx.data, "Ordner/b.txt") != "auch vom PC" {
		t.Error("not uploaded")
	}
	if content(t, fx.local, "c.txt") != "vom Server" || content(t, fx.local, "Fotos/2026/d.jpg") != "Foto" {
		t.Error("not downloaded")
	}
	if gone(fx.data, "leer") {
		t.Error("the empty folder is missing on the server")
	}
	if !gone(fx.data, "desktop.ini") {
		t.Error("desktop.ini was uploaded")
	}
}

func TestSameFilesNoConflict(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "a.txt", "gleich")
	write(t, fx.data, "a.txt", "gleich")
	fx.round(t)
	entries, _ := os.ReadDir(fx.local)
	for _, e := range entries {
		if strings.Contains(e.Name(), "Konflikt") {
			t.Errorf("conflict copy %s for equal files", e.Name())
		}
	}
}

func TestChangesFollow(t *testing.T) {
	fx := newFixture(t, "/Sync")
	write(t, fx.local, "a.txt", "eins")
	write(t, fx.local, "b.txt", "zwei")
	write(t, fx.local, "c.txt", "drei")
	write(t, fx.local, "d.txt", "vier")
	fx.round(t)
	if content(t, fx.data, "Sync/a.txt") != "eins" {
		t.Fatal("not uploaded into /Sync")
	}

	write(t, fx.local, "a.txt", "eins, geändert")
	write(t, fx.data, "Sync/b.txt", "zwei, auf dem Server geändert")
	if err := os.Remove(filepath.Join(fx.local, "c.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fx.data, "Sync", "d.txt")); err != nil {
		t.Fatal(err)
	}
	fx.round(t)
	if got := content(t, fx.data, "Sync/a.txt"); got != "eins, geändert" {
		t.Errorf("server a.txt %q", got)
	}
	if got := content(t, fx.local, "b.txt"); got != "zwei, auf dem Server geändert" {
		t.Errorf("PC b.txt %q", got)
	}
	if !gone(fx.data, "Sync/c.txt") {
		t.Error("c.txt not deleted on the server")
	}
	if !gone(fx.local, "d.txt") {
		t.Error("d.txt not deleted on the PC")
	}
	if got := content(t, fx.local, ".data-den-sync/deleted/2026-11-04/d.txt"); got != "vier" {
		t.Errorf("d.txt not kept aside: %q", got)
	}
	// The deleted c.txt is in the recycle bin, the old a.txt is a version.
	trash, _ := os.ReadDir(filepath.Join(fx.data, ".data-den-trash"))
	if len(trash) != 1 {
		t.Errorf("recycle bin has %d items, want 1", len(trash))
	}
	if versions, _ := os.ReadDir(filepath.Join(fx.data, ".data-den-versions")); len(versions) != 1 {
		t.Errorf("%d files with versions, want 1", len(versions))
	}
}

func TestConflictKeepsBoth(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "Notiz.txt", "alt")
	fx.round(t)
	write(t, fx.local, "Notiz.txt", "neu am PC")
	write(t, fx.data, "Notiz.txt", "neu am Server!")
	fx.round(t)
	copyName := "Notiz (Konflikt 2026-11-04 2105 PC).txt"
	if got := content(t, fx.local, "Notiz.txt"); got != "neu am Server!" {
		t.Errorf("PC Notiz.txt %q", got)
	}
	if got := content(t, fx.local, copyName); got != "neu am PC" {
		t.Errorf("PC copy %q", got)
	}
	if got := content(t, fx.data, copyName); got != "neu am PC" {
		t.Errorf("server copy %q", got)
	}
}

func TestDeleteChangedFileKeepsIt(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "a.txt", "alt")
	write(t, fx.local, "bleibt.txt", "x")
	fx.round(t)
	write(t, fx.local, "a.txt", "am PC geändert")
	if err := os.Remove(filepath.Join(fx.data, "a.txt")); err != nil {
		t.Fatal(err)
	}
	fx.round(t)
	if got := content(t, fx.data, "a.txt"); got != "am PC geändert" {
		t.Errorf("server a.txt %q", got)
	}
}

func TestFolderDeletedAsAWhole(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "Alt/1.txt", "1")
	write(t, fx.local, "Alt/2/3.txt", "3")
	write(t, fx.local, "bleibt.txt", "x")
	fx.round(t)
	if err := os.RemoveAll(filepath.Join(fx.local, "Alt")); err != nil {
		t.Fatal(err)
	}
	fx.round(t)
	if !gone(fx.data, "Alt") {
		t.Error("the folder is still on the server")
	}
	trash, _ := os.ReadDir(filepath.Join(fx.data, ".data-den-trash"))
	if len(trash) != 1 {
		t.Errorf("recycle bin has %d items, want the folder alone", len(trash))
	}
}

func TestFolderDeletedOnServerKeepsNewFiles(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "Alt/1.txt", "1")
	write(t, fx.local, "bleibt.txt", "x")
	fx.round(t)
	if err := os.RemoveAll(filepath.Join(fx.data, "Alt")); err != nil {
		t.Fatal(err)
	}
	write(t, fx.local, "Alt/neu.txt", "neu")
	fx.round(t)
	if !gone(fx.local, "Alt/1.txt") {
		t.Error("1.txt still on the PC")
	}
	if content(t, fx.data, "Alt/neu.txt") != "neu" {
		t.Error("the new file was not uploaded")
	}
}

func TestEmptySideStops(t *testing.T) {
	fx := newFixture(t, "/")
	write(t, fx.local, "a.txt", "a")
	fx.round(t)
	if err := os.Remove(filepath.Join(fx.local, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.syncer.Round(context.Background()); err == nil {
		t.Error("an emptied folder deletes everything on the server")
	}
	if gone(fx.data, "a.txt") {
		t.Error("deleted on the server")
	}
}

func TestWrongToken(t *testing.T) {
	fx := newFixture(t, "/")
	fx.syncer.Client.Token = "falsch"
	if _, err := fx.syncer.Round(context.Background()); !errors.Is(err, ErrLogin) {
		t.Errorf("err %v, want ErrLogin", err)
	}
}

func TestRemotesSkipsNames(t *testing.T) {
	s := &Syncer{FoldCase: true, LocalName: WindowsName, warned: map[string]bool{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	got := s.remotes([]Remote{
		{Path: "A.txt"},
		{Path: "a.txt"},
		{Path: "frage?.txt"},
		{Path: "Ordner:x", Dir: true},
		{Path: "Ordner:x/b.txt"},
		{Path: "ok", Dir: true},
		{Path: "ok/c.txt"},
	})
	if len(got) != 3 {
		t.Errorf("got %v, want A.txt, ok and ok/c.txt", got)
	}
	for _, p := range []string{"A.txt", "ok", "ok/c.txt"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s missing", p)
		}
	}
}

func TestWindowsName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.txt": true, "Ärger ß.pdf": true, "con": false, "CON.txt": false, "com1.log": false, "console.txt": true,
		"a:b": false, "a?": false, "punkt.": false, "leer ": false, "": false,
	} {
		if got := WindowsName(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

func TestIgnored(t *testing.T) {
	for name, want := range map[string]bool{
		".data-den-sync": true, "Thumbs.db": true, "~$Brief.docx": true, "x.TMP": true, "Brief.docx": false,
	} {
		if got := Ignored(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}
