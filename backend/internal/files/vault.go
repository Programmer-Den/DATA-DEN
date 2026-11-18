package files

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// The vault (Tresor) holds files that are encrypted in the browser, end to
// end: the server stores only ciphertext under encrypted names and never
// sees a key. The browser keeps the vault key, wrapped once with a key from
// the account's passphrase and once with a recovery key, in vaultKeysFile;
// the server cannot unwrap either.
//
//	GET  /api/vault                                  -> {"keys": {...}} or {"keys": null}
//	POST /api/vault/keys    {"keys": {...}, "proof"}  -> 200, set up or new wrapped keys
//	GET  /api/vault/files?path=/<name>               -> {"entries": [...]}
//	POST /api/vault/folders {"path"}                 -> 201
//	PUT  /api/vault/file?path=/<name>/<name>  (body) -> 201
//	GET  /api/vault/file?path=/<name>                -> the ciphertext
//	POST /api/vault/move    {"from", "to"}           -> 200
//	POST /api/vault/delete  {"path"}                 -> 200, gone for good
//
// The vault lives in vaultDir, hidden like the server's other folders, so
// the search, thumbnails, text recognition, share links, the sync client and
// WebDAV never see it. Its files count against the quota.
//
// proof is what the browser derives from the vault key for this purpose.
// The server keeps only its SHA-256 hash: the first POST sets it up, and
// replacing the wrapped keys later, such as for a new passphrase, needs the
// same proof, so a stolen session cannot lock the account out of its vault.
const (
	vaultDir       = ".data-den-vault"
	vaultKeysFile  = vaultDir + "/keys.json"
	vaultFilesDir  = vaultDir + "/files"
	maxVaultKeys   = 4096
	vaultProofSize = 32
	// maxVaultPlain is the largest file before encryption, as the browser
	// checks it, since it holds the file in memory to encrypt and decrypt it.
	maxVaultPlain = 1 << 30
	// MaxVaultFile is the largest encrypted file: maxVaultPlain with the
	// magic, the salt and the tag of each 1 MiB chunk.
	MaxVaultFile int64 = maxVaultPlain + int64(len(vaultMagic)) + 32 + 16*(maxVaultPlain>>20)
	// vaultMagic starts every encrypted file, followed by a 32-byte salt and
	// at least one sealed chunk with its 16-byte tag.
	vaultMagic   = "DDV1"
	minVaultFile = len(vaultMagic) + 32 + 16
)

// vaultName is an encrypted name: base64url without padding of a 12-byte
// nonce, the name and a 16-byte tag. 255 characters is the most a file
// system takes.
var vaultName = regexp.MustCompile(`^[A-Za-z0-9_-]{39,255}$`)

// vaultKeys is what vaultKeysFile holds.
type vaultKeys struct {
	// Keys are the wrapped vault keys, as the browser sent them.
	Keys json.RawMessage `json:"keys"`
	// Verifier is the hex SHA-256 of the proof.
	Verifier string `json:"verifier"`
}

// VaultEntry is one file or folder in the vault, under its encrypted name.
type VaultEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

func (f *Files) registerVault(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	own := func(h http.HandlerFunc) http.HandlerFunc {
		return require(func(w http.ResponseWriter, r *http.Request) {
			// Only ever the account's own vault, never through a shared folder.
			if byGrant(r) {
				writeError(w, http.StatusForbidden, "In geteilten Ordnern nicht möglich")
				return
			}
			h(w, r)
		})
	}
	mux.HandleFunc("GET /api/vault", own(f.vaultState))
	mux.HandleFunc("POST /api/vault/keys", own(f.vaultSetKeys))
	mux.HandleFunc("GET /api/vault/files", own(f.vaultList))
	mux.HandleFunc("POST /api/vault/folders", own(f.vaultFolder))
	mux.HandleFunc("PUT /api/vault/file", own(f.vaultUpload))
	mux.HandleFunc("GET /api/vault/file", own(f.vaultDownload))
	mux.HandleFunc("POST /api/vault/move", own(f.vaultMove))
	mux.HandleFunc("POST /api/vault/delete", own(f.vaultDelete))
}

// readVaultKeys returns the stored keys; fs.ErrNotExist when there is no
// vault yet.
func (f *Files) readVaultKeys() (vaultKeys, error) {
	var k vaultKeys
	data, err := f.root.ReadFile(vaultKeysFile)
	if err != nil {
		return k, err
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return k, err
	}
	return k, nil
}

func (f *Files) vaultState(w http.ResponseWriter, _ *http.Request) {
	k, err := f.readVaultKeys()
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"keys": nil})
		return
	}
	if err != nil {
		slog.Error("reading the vault keys", "account", f.account, "err", err)
		writeError(w, http.StatusInternalServerError, "Tresor nicht lesbar")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": k.Keys})
}

func (f *Files) vaultSetKeys(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON erwartet")
		return
	}
	var body struct {
		Keys  json.RawMessage `json:"keys"`
		Proof string          `json:"proof"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxVaultKeys+256)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	keys := bytes.TrimSpace(body.Keys)
	if len(keys) == 0 || len(keys) > maxVaultKeys || keys[0] != '{' || !json.Valid(keys) {
		writeError(w, http.StatusBadRequest, "Ungültige Schlüssel")
		return
	}
	proof, err := base64.StdEncoding.DecodeString(body.Proof)
	if err != nil || len(proof) != vaultProofSize {
		writeError(w, http.StatusBadRequest, "Ungültiger Nachweis")
		return
	}
	sum := sha256.Sum256(proof)
	verifier := hex.EncodeToString(sum[:])

	f.vaultMu.Lock()
	defer f.vaultMu.Unlock()
	old, err := f.readVaultKeys()
	setUp := errors.Is(err, fs.ErrNotExist)
	switch {
	case setUp:
	case err != nil:
		slog.Error("reading the vault keys", "account", f.account, "err", err)
		writeError(w, http.StatusInternalServerError, "Tresor nicht lesbar")
		return
	case subtle.ConstantTimeCompare([]byte(old.Verifier), []byte(verifier)) != 1:
		writeError(w, http.StatusForbidden, "Der Tresor ist nicht entsperrt")
		return
	}
	data, err := json.Marshal(vaultKeys{Keys: keys, Verifier: verifier})
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ungültige Schlüssel")
		return
	}
	if err := f.root.MkdirAll(vaultFilesDir, 0o750); err != nil {
		f.diskError(w, "creating the vault", err)
		return
	}
	if err := f.writeAtomic(vaultKeysFile, data); err != nil {
		f.diskError(w, "saving the vault keys", err)
		return
	}
	if setUp {
		slog.Info("vault set up", "account", f.account)
		f.note(r, "vault-setup", "")
	} else {
		slog.Info("vault keys changed", "account", f.account)
		f.note(r, "vault-keys", "")
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": json.RawMessage(keys)})
}

// writeAtomic replaces name with data, through a temporary file, so a
// crash never leaves half of it.
func (f *Files) writeAtomic(name string, data []byte) error {
	tmp := name + ".tmp"
	file, err := f.root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return f.root.Rename(tmp, name)
}

// vaultPath turns a path of encrypted names, such as "/<name>/<name>", into
// a name within the root; "/" or "" is the vault itself, only where root is
// true. It answers the request itself when the path is not acceptable or
// there is no vault.
func (f *Files) vaultPath(w http.ResponseWriter, p string, root bool) (string, bool) {
	if _, err := f.root.Stat(vaultKeysFile); err != nil {
		writeError(w, http.StatusNotFound, "Kein Tresor eingerichtet")
		return "", false
	}
	p = strings.Trim(p, "/")
	if p == "" {
		if !root {
			writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
			return "", false
		}
		return vaultFilesDir, true
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if !vaultName.MatchString(part) {
			writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
			return "", false
		}
	}
	return path.Join(vaultFilesDir, strings.Join(parts, "/")), true
}

func (f *Files) vaultList(w http.ResponseWriter, r *http.Request) {
	name, ok := f.vaultPath(w, r.URL.Query().Get("path"), true)
	if !ok {
		return
	}
	dir, err := f.root.Open(name)
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = dir.Close() }()
	infos, err := dir.Readdir(-1)
	if err != nil {
		if info, statErr := dir.Stat(); statErr == nil && !info.IsDir() {
			writeError(w, http.StatusBadRequest, "Kein Ordner")
			return
		}
		slog.Error("listing the vault", "account", f.account, "err", err)
		writeError(w, http.StatusInternalServerError, "Ordner nicht lesbar")
		return
	}
	entries := make([]VaultEntry, 0, len(infos))
	for _, info := range infos {
		if (!info.Mode().IsDir() && !info.Mode().IsRegular()) || !vaultName.MatchString(info.Name()) {
			continue
		}
		entries = append(entries, VaultEntry{
			Name: info.Name(), Dir: info.IsDir(), Size: info.Size(),
			Modified: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (f *Files) vaultFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := f.vaultPath(w, body.Path, false)
	if !ok {
		return
	}
	if err := f.root.Mkdir(name, 0o750); err != nil {
		switch {
		case errors.Is(err, fs.ErrExist):
			writeError(w, http.StatusConflict, "Existiert bereits")
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, "Übergeordneter Ordner nicht gefunden")
		default:
			f.diskError(w, "creating a vault folder", err)
		}
		return
	}
	f.note(r, "vault-folder", "")
	writeJSON(w, http.StatusCreated, map[string]string{})
}

// vaultUpload stores one encrypted file. It must not exist yet, and it must
// start like one, so a client that does not encrypt cannot put plain files
// into the vault by mistake.
func (f *Files) vaultUpload(w http.ResponseWriter, r *http.Request) {
	name, ok := f.vaultPath(w, r.URL.Query().Get("path"), false)
	if !ok {
		return
	}
	size := r.ContentLength
	if size < int64(minVaultFile) {
		writeError(w, http.StatusBadRequest, "Keine verschlüsselte Datei")
		return
	}
	if size > MaxVaultFile {
		writeError(w, http.StatusRequestEntityTooLarge, "Höchstens 1 GB je Datei im Tresor")
		return
	}
	if dir, err := f.root.Stat(path.Dir(name)); err != nil || !dir.IsDir() {
		writeError(w, http.StatusNotFound, "Zielordner nicht gefunden")
		return
	}
	if _, err := f.root.Lstat(name); err == nil {
		writeError(w, http.StatusConflict, "Existiert bereits")
		return
	}
	release, fits, err := f.reserve(size)
	if err != nil {
		f.diskError(w, "counting the usage", err)
		return
	} else if !fits {
		writeError(w, http.StatusInsufficientStorage, "Nicht genug Platz im eigenen Kontingent")
		return
	}
	defer release()
	body := http.MaxBytesReader(w, r.Body, size)
	head := make([]byte, len(vaultMagic))
	if _, err := io.ReadFull(body, head); err != nil || string(head) != vaultMagic {
		writeError(w, http.StatusBadRequest, "Keine verschlüsselte Datei")
		return
	}
	id, err := newID()
	if err != nil {
		f.diskError(w, "uploading to the vault", err)
		return
	}
	if err := f.root.MkdirAll(uploadDir, 0o750); err != nil {
		f.diskError(w, "uploading to the vault", err)
		return
	}
	tmp := path.Join(uploadDir, id)
	file, err := f.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		f.diskError(w, "uploading to the vault", err)
		return
	}
	defer func() { _ = f.root.Remove(tmp) }()
	n, err := io.Copy(file, io.MultiReader(bytes.NewReader(head), body))
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil || n != size {
		writeError(w, http.StatusBadRequest, "Upload unvollständig")
		return
	}
	// A link fails when the name was taken in the meantime.
	if err := f.root.Link(tmp, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Existiert bereits")
			return
		}
		f.diskError(w, "uploading to the vault", err)
		return
	}
	f.addUsage(size)
	f.note(r, "vault-upload", "")
	writeJSON(w, http.StatusCreated, map[string]int64{"size": size})
}

func (f *Files) vaultDownload(w http.ResponseWriter, r *http.Request) {
	name, ok := f.vaultPath(w, r.URL.Query().Get("path"), false)
	if !ok {
		return
	}
	file, err := f.root.Open(name)
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="tresor.bin"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

func (f *Files) vaultMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	from, ok := f.vaultPath(w, body.From, false)
	if !ok {
		return
	}
	to, ok := f.vaultPath(w, body.To, false)
	if !ok {
		return
	}
	if from == to {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	if strings.HasPrefix(to, from+"/") {
		writeError(w, http.StatusBadRequest, "Ein Ordner kann nicht in sich selbst verschoben werden")
		return
	}
	info, err := f.root.Lstat(from)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.Mode().IsDir() && !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return
	}
	if dir, err := f.root.Stat(path.Dir(to)); err != nil || !dir.IsDir() {
		writeError(w, http.StatusNotFound, "Zielordner nicht gefunden")
		return
	}
	if _, err := f.root.Lstat(to); err == nil {
		writeError(w, http.StatusConflict, "Existiert bereits")
		return
	}
	if err := f.rename(from, to, info.IsDir()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Existiert bereits")
			return
		}
		f.diskError(w, "moving in the vault", err)
		return
	}
	f.note(r, "vault-move", "")
	writeJSON(w, http.StatusOK, map[string]string{})
}

// vaultDelete deletes a file or folder for good: the recycle bin would show
// only encrypted names.
func (f *Files) vaultDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := f.vaultPath(w, body.Path, false)
	if !ok {
		return
	}
	if _, err := f.root.Lstat(name); err != nil {
		openError(w, err)
		return
	}
	if err := f.root.RemoveAll(name); err != nil {
		f.diskError(w, "deleting in the vault", err)
		return
	}
	f.forgetUsage()
	f.note(r, "vault-delete", "")
	writeJSON(w, http.StatusOK, map[string]string{})
}
