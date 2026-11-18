package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

const testOrigin = "https://nas.example.de"

var b64 = base64.RawURLEncoding

// device is a passkey on a phone or security key, for the tests: one
// ES256 key for one site that always checks the user.
type device struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte
	rpID   string
	origin string
	count  uint32
}

func newDevice(t *testing.T) *device {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &device{t: t, key: key, id: id, rpID: "nas.example.de", origin: testOrigin}
}

func (d *device) clientData(kind string, options map[string]any) []byte {
	data, _ := json.Marshal(map[string]any{"type": kind, "challenge": options["challenge"], "origin": d.origin})
	return data
}

// create answers navigator.credentials.create with the options from the
// server.
func (d *device) create(options map[string]any) string {
	d.t.Helper()
	user := options["user"].(map[string]any)
	var err error
	if d.handle, err = b64.DecodeString(user["id"].(string)); err != nil {
		d.t.Fatal(err)
	}
	pub := d.key.Public().(*ecdsa.PublicKey)
	raw, err := pub.Bytes() // 0x04, x, y
	if err != nil {
		d.t.Fatal(err)
	}
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256),
		XCoord:        raw[1:33],
		YCoord:        raw[33:],
	})
	if err != nil {
		d.t.Fatal(err)
	}
	rpHash := sha256.Sum256([]byte(d.rpID))
	auth := append([]byte{}, rpHash[:]...)
	auth = append(auth, 0x45) // user present, user verified, attested data
	auth = binary.BigEndian.AppendUint32(auth, d.count)
	auth = append(auth, make([]byte, 16)...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(d.id))) //nolint:gosec // 16 bytes
	auth = append(auth, d.id...)
	auth = append(auth, cose...)
	object, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		d.t.Fatal(err)
	}
	answer, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(d.id), "rawId": b64.EncodeToString(d.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(d.clientData("webauthn.create", options)),
			"attestationObject": b64.EncodeToString(object),
		},
	})
	return string(answer)
}

// get answers navigator.credentials.get.
func (d *device) get(options map[string]any) string {
	d.t.Helper()
	d.count++
	rpHash := sha256.Sum256([]byte(d.rpID))
	auth := append([]byte{}, rpHash[:]...)
	auth = append(auth, 0x05) // user present, user verified
	auth = binary.BigEndian.AppendUint32(auth, d.count)
	client := d.clientData("webauthn.get", options)
	clientHash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		d.t.Fatal(err)
	}
	answer, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(d.id), "rawId": b64.EncodeToString(d.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(client),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(d.handle),
		},
	})
	return string(answer)
}

func passkeySetup(t *testing.T, file string) (*http.ServeMux, *http.Cookie) {
	t.Helper()
	mux := accountsSetupWith(t, Config{AccountsFile: file, Origin: testOrigin})
	return mux, sessionCookie(t, login(mux, "192.0.2.1:1", "chriszly", "richtig", nil))
}

// addPasskey registers d for the account of c.
func addPasskey(t *testing.T, mux http.Handler, c *http.Cookie, d *device, name string) map[string]any {
	t.Helper()
	code, got := call(mux, c, http.MethodPost, "/api/account/passkeys/begin", `{"password":"richtig"}`)
	if code != http.StatusOK {
		t.Fatalf("begin: %d %v", code, got)
	}
	options := got["publicKey"].(map[string]any)
	if rp := options["rp"].(map[string]any); rp["id"] != "nas.example.de" {
		t.Errorf("relying party %v, want nas.example.de", rp)
	}
	body, _ := json.Marshal(map[string]any{"name": name, "credential": json.RawMessage(d.create(options))})
	code, got = call(mux, c, http.MethodPost, "/api/account/passkeys", string(body))
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, got)
	}
	return got
}

// passkeyLogin logs in with d and returns the status and answer.
func passkeyLogin(t *testing.T, mux http.Handler, d *device) (int, map[string]any) {
	t.Helper()
	code, got := callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey/begin", `{}`)
	if code != http.StatusOK {
		t.Fatalf("begin login: %d %v", code, got)
	}
	options := got["publicKey"].(map[string]any)
	if options["userVerification"] != "required" {
		t.Errorf("user verification %v, want required", options["userVerification"])
	}
	body, _ := json.Marshal(map[string]any{"id": got["id"], "credential": json.RawMessage(d.get(options))})
	return callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey", string(body))
}

func TestPasskeyRegisterLoginRemove(t *testing.T) {
	file := filepath.Join(t.TempDir(), "accounts.json")
	mux, c := passkeySetup(t, file)
	if code, got := callReq(mux, "192.0.2.9:1", http.MethodGet, "/api/login/options", ""); code != http.StatusOK || got["passkeys"] != true {
		t.Fatalf("options: %d %v", code, got)
	}
	d := newDevice(t)
	added := addPasskey(t, mux, c, d, "Handy")
	if added["name"] != "Handy" || added["id"] != b64.EncodeToString(d.id) {
		t.Errorf("added %v", added)
	}

	code, got := passkeyLogin(t, mux, d)
	if code != http.StatusOK || got["username"] != "chriszly" {
		t.Fatalf("login: %d %v", code, got)
	}

	// Kept across a restart, with when it was last used.
	mux2 := accountsSetupWith(t, Config{AccountsFile: file, Origin: testOrigin})
	if code, got := passkeyLogin(t, mux2, d); code != http.StatusOK {
		t.Fatalf("login after restart: %d %v", code, got)
	}
	code, got = call(mux, c, http.MethodGet, "/api/account/passkeys", "")
	list := got["passkeys"].([]any)
	if code != http.StatusOK || got["available"] != true || len(list) != 1 || list[0].(map[string]any)["lastUsed"] == nil {
		t.Fatalf("list: %d %v", code, got)
	}

	if code, got := call(mux, c, http.MethodPost, "/api/account/passkeys/"+b64.EncodeToString(d.id)+"/delete", `{}`); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, got)
	}
	if code, _ := passkeyLogin(t, mux, d); code != http.StatusUnauthorized {
		t.Errorf("login with a removed passkey: %d, want 401", code)
	}
}

func TestPasskeyNeedsPasswordAndCode(t *testing.T) {
	mux, c := passkeySetup(t, "")
	if code, _ := call(mux, c, http.MethodPost, "/api/account/passkeys/begin", `{"password":"falsch"}`); code != http.StatusForbidden {
		t.Errorf("wrong password: %d, want 403", code)
	}
	// Without a registration started, an answer is refused.
	body, _ := json.Marshal(map[string]any{"name": "x", "credential": json.RawMessage(newDevice(t).create(map[string]any{"user": map[string]any{"id": "AA"}, "challenge": "AA"}))})
	if code, _ := call(mux, c, http.MethodPost, "/api/account/passkeys", string(body)); code != http.StatusBadRequest {
		t.Errorf("finish without begin: %d, want 400", code)
	}
}

func TestPasskeyWrongOrigin(t *testing.T) {
	mux, c := passkeySetup(t, "")
	d := newDevice(t)
	addPasskey(t, mux, c, d, "Handy")
	// A look-alike site: the browser signs its own origin and domain.
	d.origin = "https://nas.example.de.evil.test"
	if code, _ := passkeyLogin(t, mux, d); code != http.StatusUnauthorized {
		t.Errorf("other origin: %d, want 401", code)
	}
	d.origin, d.rpID = testOrigin, "evil.test"
	if code, _ := passkeyLogin(t, mux, d); code != http.StatusUnauthorized {
		t.Errorf("other domain: %d, want 401", code)
	}
}

func TestPasskeyChallengeOnlyOnce(t *testing.T) {
	mux, c := passkeySetup(t, "")
	d := newDevice(t)
	addPasskey(t, mux, c, d, "Handy")
	_, got := callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey/begin", `{}`)
	body, _ := json.Marshal(map[string]any{"id": got["id"], "credential": json.RawMessage(d.get(got["publicKey"].(map[string]any)))})
	if code, _ := callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey", string(body)); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code, _ := callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey", string(body)); code != http.StatusBadRequest {
		t.Errorf("replay: %d, want 400", code)
	}
}

func TestPasskeyAdminRemovesAll(t *testing.T) {
	mux, c := passkeySetup(t, "")
	if code, got := call(mux, c, http.MethodPost, "/api/users", `{"name":"anna","password":"0123456789"}`); code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, got)
	}
	anna := sessionCookie(t, login(mux, "192.0.2.1:1", "anna", "0123456789", nil))
	d := newDevice(t)
	body := `{"password":"0123456789"}`
	code, got := call(mux, anna, http.MethodPost, "/api/account/passkeys/begin", body)
	if code != http.StatusOK {
		t.Fatalf("begin: %d %v", code, got)
	}
	add, _ := json.Marshal(map[string]any{"name": "Stick", "credential": json.RawMessage(d.create(got["publicKey"].(map[string]any)))})
	if code, got := call(mux, anna, http.MethodPost, "/api/account/passkeys", string(add)); code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, got)
	}
	_, got = call(mux, c, http.MethodGet, "/api/users", "")
	if users := got["users"].([]any); users[1].(map[string]any)["passkeys"] != 1.0 {
		t.Errorf("users %v, want anna with 1 passkey", users)
	}
	if code, got := call(mux, c, http.MethodPost, "/api/users/anna", `{"passkeys":false}`); code != http.StatusOK || got["passkeys"] != 0.0 {
		t.Fatalf("remove: %d %v", code, got)
	}
	if code, _ := passkeyLogin(t, mux, d); code != http.StatusUnauthorized {
		t.Errorf("login after removal: %d, want 401", code)
	}
}

func TestPasskeysOff(t *testing.T) {
	mux := accountsSetupWith(t, Config{})
	if _, got := callReq(mux, "192.0.2.9:1", http.MethodGet, "/api/login/options", ""); got["passkeys"] != false {
		t.Errorf("options %v, want passkeys false", got)
	}
	if code, _ := callReq(mux, "192.0.2.9:1", http.MethodPost, "/api/login/passkey/begin", `{}`); code != http.StatusNotFound {
		t.Errorf("begin: %d, want 404", code)
	}
}

func TestBadOrigin(t *testing.T) {
	for _, origin := range []string{"http://nas.example.de", "nas.example.de", "https://nas.example.de/den", "https://"} {
		if _, err := New(Config{Origin: origin}); err == nil || !strings.Contains(err.Error(), "DATA_DEN_ORIGIN") {
			t.Errorf("%q: %v, want an error about DATA_DEN_ORIGIN", origin, err)
		}
	}
}
