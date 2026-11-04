package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// browser is the receiving side of a push subscription.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) browser {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return browser{key, auth}
}

func (b browser) subscription(endpoint string) Subscription {
	var s Subscription
	s.Endpoint = endpoint
	s.Keys.P256dh = base64.RawURLEncoding.EncodeToString(b.key.PublicKey().Bytes())
	s.Keys.Auth = base64.RawURLEncoding.EncodeToString(b.auth)
	return s
}

// decrypt does what the browser does with a push message (RFC 8291).
func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idLen != 65 {
		t.Fatalf("header: rs %d, idlen %d", rs, idLen)
	}
	asPublic, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := b.key.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	info := slices.Concat([]byte("WebPush: info\x00"), b.key.PublicKey().Bytes(), asPublic.Bytes())
	ikm, _ := hkdf.Key(sha256.New, shared, b.auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatal("decrypting:", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter: %x", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

func TestEncrypt(t *testing.T) {
	b := newBrowser(t)
	sub := b.subscription("https://fcm.googleapis.com/fcm/send/abc")
	body, err := encrypt(sub, []byte(`{"title":"Hallo"}`), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.decrypt(t, body); string(got) != `{"title":"Hallo"}` {
		t.Errorf("decrypted %q", got)
	}
}

func TestSubscriptionCheck(t *testing.T) {
	b := newBrowser(t)
	for endpoint, ok := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":                true,
		"https://updates.push.services.mozilla.com/wpush/v2/abc": true,
		"https://web.push.apple.com/abc":                         true,
		"https://wns2-par02p.notify.windows.com/w/?token=abc":    true,
		"http://fcm.googleapis.com/fcm/send/abc":                 false,
		"https://fcm.googleapis.com:8443/fcm/send/abc":           false,
		"https://evil.example/fcm.googleapis.com":                false,
		"https://fcm.googleapis.com.evil.example/abc":            false,
		"https://user@fcm.googleapis.com/abc":                    false,
		"https://192.168.1.10/abc":                               false,
	} {
		if err := b.subscription(endpoint).check(); (err == nil) != ok {
			t.Errorf("%s: %v", endpoint, err)
		}
	}
	sub := b.subscription("https://fcm.googleapis.com/abc")
	sub.Keys.Auth = "AAAA"
	if sub.check() == nil {
		t.Error("short auth secret accepted")
	}
	sub = b.subscription("https://fcm.googleapis.com/abc")
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
	if sub.check() == nil {
		t.Error("invalid key accepted")
	}
}

func TestVAPID(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vapid.pem")
	key, err := loadKey(file)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadKey(file)
	if err != nil || !again.Equal(key) {
		t.Fatalf("key not kept: %v", err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	header, err := vapidHeader(key, "https://fcm.googleapis.com/fcm/send/abc", "mailto:nas@example.org", now)
	if err != nil {
		t.Fatal(err)
	}
	token, k, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || k != base64.RawURLEncoding.EncodeToString(publicKeyBytes(key)) {
		t.Fatalf("header %q", header)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != "mailto:nas@example.org" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("claims %+v", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Error("signature does not verify")
	}
	if key.Curve != elliptic.P256() {
		t.Error("not P-256")
	}
}

func TestPushPayloadCut(t *testing.T) {
	long := strings.Repeat("ä", 5000)
	data := pushPayload(Message{Kind: KindCorrupt, Title: "T", Body: long})
	if len(data) > maxPayload {
		t.Errorf("payload %d bytes", len(data))
	}
	var p map[string]string
	if err := json.Unmarshal(data, &p); err != nil || p["tag"] != "corrupt" || !strings.HasSuffix(p["body"], "…") {
		t.Errorf("payload %v, %v", p, err)
	}
}
