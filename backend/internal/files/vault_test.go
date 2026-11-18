package files

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vaultSetup returns the handler of a fresh space and its data directory.
func vaultSetup(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return mux, dir
}

func vaultDo(h http.Handler, method, target string, body []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func vaultJSON(h http.Handler, target string, v any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(v)
	return vaultDo(h, http.MethodPost, target, body, "application/json")
}

var (
	proofA = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	proofB = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	// encName stands for an encrypted name, long enough for nonce and tag.
	encName  = strings.Repeat("A", 40)
	encName2 = strings.Repeat("B", 40)
)

// sealed is what the browser uploads: the magic, a salt and a chunk.
func sealed(n int) []byte {
	return append([]byte(vaultMagic), bytes.Repeat([]byte{7}, 32+16+n)...)
}

func setUpVault(t *testing.T, h http.Handler) {
	t.Helper()
	rec := vaultJSON(h, "/api/vault/keys", map[string]any{"keys": map[string]int{"version": 1}, "proof": proofA})
	if rec.Code != http.StatusOK {
		t.Fatalf("set up: %d %s", rec.Code, rec.Body)
	}
}

func TestVaultKeys(t *testing.T) {
	h, dir := vaultSetup(t)
	rec := vaultDo(h, http.MethodGet, "/api/vault", nil, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"keys":null}` {
		t.Fatalf("before: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []map[string]any{
		{"keys": "text", "proof": proofA},
		{"keys": map[string]int{"version": 1}, "proof": "short"},
		{"keys": map[string]string{"pad": strings.Repeat("x", maxVaultKeys)}, "proof": proofA},
	} {
		if rec := vaultJSON(h, "/api/vault/keys", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", bad["proof"], rec.Code)
		}
	}
	setUpVault(t, h)
	rec = vaultDo(h, http.MethodGet, "/api/vault", nil, "")
	if !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Errorf("after: %s", rec.Body)
	}
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, vaultKeysFile)))
	if err != nil || strings.Contains(string(data), proofA) {
		t.Errorf("the proof itself is stored: %v %s", err, data)
	}

	// Replacing needs the same proof.
	if rec := vaultJSON(h, "/api/vault/keys", map[string]any{"keys": map[string]int{"version": 2}, "proof": proofB}); rec.Code != http.StatusForbidden {
		t.Errorf("wrong proof: %d", rec.Code)
	}
	if rec := vaultJSON(h, "/api/vault/keys", map[string]any{"keys": map[string]int{"version": 2}, "proof": proofA}); rec.Code != http.StatusOK {
		t.Errorf("right proof: %d %s", rec.Code, rec.Body)
	}
	rec = vaultDo(h, http.MethodGet, "/api/vault", nil, "")
	if !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Errorf("replaced: %s", rec.Body)
	}
	if rec := vaultDo(h, http.MethodPost, "/api/vault/keys", []byte(`{}`), "text/plain"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("no JSON: %d", rec.Code)
	}
}

func TestVaultNeedsSetUp(t *testing.T) {
	h, _ := vaultSetup(t)
	if rec := vaultDo(h, http.MethodGet, "/api/vault/files", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("list: %d", rec.Code)
	}
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path=/"+encName, sealed(10), ""); rec.Code != http.StatusNotFound {
		t.Errorf("upload: %d", rec.Code)
	}
}

func TestVaultFiles(t *testing.T) {
	h, dir := vaultSetup(t)
	setUpVault(t, h)

	if rec := vaultJSON(h, "/api/vault/folders", map[string]string{"path": "/" + encName}); rec.Code != http.StatusCreated {
		t.Fatalf("folder: %d %s", rec.Code, rec.Body)
	}
	if rec := vaultJSON(h, "/api/vault/folders", map[string]string{"path": "/" + encName}); rec.Code != http.StatusConflict {
		t.Errorf("folder again: %d", rec.Code)
	}
	file := "/" + encName + "/" + encName2
	content := sealed(100)
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path="+url.QueryEscape(file), content, ""); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path="+url.QueryEscape(file), content, ""); rec.Code != http.StatusConflict {
		t.Errorf("never overwritten: %d", rec.Code)
	}
	stored, err := os.ReadFile(filepath.Clean(filepath.Join(dir, vaultFilesDir, encName, encName2)))
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("stored: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, uploadDir)); len(entries) != 0 {
		t.Errorf("left in the upload folder: %v", entries)
	}

	rec := vaultDo(h, http.MethodGet, "/api/vault/files?path=/"+encName, nil, "")
	var listed struct{ Entries []VaultEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Entries) != 1 ||
		listed.Entries[0].Name != encName2 || listed.Entries[0].Size != int64(len(content)) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = vaultDo(h, http.MethodGet, "/api/vault/files", nil, "")
	if !strings.Contains(rec.Body.String(), `"dir":true`) {
		t.Errorf("root: %s", rec.Body)
	}

	rec = vaultDo(h, http.MethodGet, "/api/vault/file?path="+url.QueryEscape(file), nil, "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d", rec.Code)
	}

	moved := "/" + encName2
	if rec := vaultJSON(h, "/api/vault/move", map[string]string{"from": file, "to": moved}); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if rec := vaultJSON(h, "/api/vault/move", map[string]string{"from": "/" + encName, "to": "/" + encName + "/" + encName2}); rec.Code != http.StatusBadRequest {
		t.Errorf("into itself: %d", rec.Code)
	}
	if rec := vaultJSON(h, "/api/vault/delete", map[string]string{"path": "/" + encName}); rec.Code != http.StatusOK {
		t.Errorf("delete folder: %d", rec.Code)
	}
	if rec := vaultJSON(h, "/api/vault/delete", map[string]string{"path": moved}); rec.Code != http.StatusOK {
		t.Errorf("delete file: %d", rec.Code)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, vaultFilesDir)); len(entries) != 0 {
		t.Errorf("left: %v", entries)
	}
	if rec := vaultJSON(h, "/api/vault/delete", map[string]string{"path": moved}); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}
}

func TestVaultRefuses(t *testing.T) {
	h, _ := vaultSetup(t)
	setUpVault(t, h)
	for name, p := range map[string]string{
		"plain name": "/geheim.pdf",
		"dot dot":    "/../" + encName,
		"the root":   "/",
		"too short":  "/" + strings.Repeat("A", 20),
	} {
		if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path="+url.QueryEscape(p), sealed(10), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	// Plain files are refused, whatever their name.
	plain := bytes.Repeat([]byte("x"), 100)
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path=/"+encName, plain, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("plain: %d", rec.Code)
	}
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path=/"+encName, []byte(vaultMagic), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("too short: %d", rec.Code)
	}
	if rec := vaultDo(h, http.MethodPut, "/api/vault/file?path=/"+encName+"/"+encName2, sealed(10), ""); rec.Code != http.StatusNotFound {
		t.Errorf("no folder: %d", rec.Code)
	}
	// Never through the normal file API.
	if rec := get(h, "/api/files", "/"+vaultDir, nil); rec.Code != http.StatusNotFound {
		t.Errorf("listed: %d", rec.Code)
	}
}

func TestVaultQuota(t *testing.T) {
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	setUpVault(t, mux)
	used, err := f.usage()
	if err != nil {
		t.Fatal(err)
	}
	f.quota.Store(used + 100)
	if rec := vaultDo(mux, http.MethodPut, "/api/vault/file?path=/"+encName, sealed(200), ""); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("over quota: %d", rec.Code)
	}
	if rec := vaultDo(mux, http.MethodPut, "/api/vault/file?path=/"+encName, sealed(10), ""); rec.Code != http.StatusCreated {
		t.Errorf("within: %d %s", rec.Code, rec.Body)
	}
}

// Uploads at the same time cannot take the same room twice.
func TestReserve(t *testing.T) {
	f, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	used, err := f.usage()
	if err != nil {
		t.Fatal(err)
	}
	f.quota.Store(used + 100)
	release, ok, err := f.reserve(60)
	if err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if _, ok, _ := f.reserve(60); ok {
		t.Error("the second fit into the room the first holds")
	}
	release()
	if _, ok, _ := f.reserve(60); !ok {
		t.Error("the room was not given back")
	}
}
