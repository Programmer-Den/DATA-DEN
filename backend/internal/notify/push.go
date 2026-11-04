package notify

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Web Push (RFC 8030) with VAPID (RFC 8292) and encrypted content (RFC 8291).
// The browser hands over a subscription: the push service's address and the
// keys to encrypt for it. The server signs each message with its own VAPID
// key, kept in the data directory, so the push service knows who sends.

// pushHosts are the push services of the browsers. A subscription for any
// other address is refused, so the server never sends requests where a user
// points it to.
var pushHosts = []string{
	"fcm.googleapis.com",        // Chrome, Edge on Android, Samsung Internet
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari, iOS home screen apps
	"notify.windows.com",        // Edge on Windows
}

// maxPayload is the most a push service must accept, before encryption adds
// its header and tag.
const maxPayload = 3800

// Subscription is one browser's push subscription, as PushSubscription.toJSON
// gives it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Created time.Time `json:"created,omitzero"`
}

// check returns why the subscription is not acceptable, or nil.
func (s Subscription) check() error {
	if len(s.Endpoint) > 1024 {
		return errors.New("address too long")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return errors.New("not an HTTPS address")
	}
	host := strings.ToLower(u.Hostname())
	known := false
	for _, h := range pushHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			known = true
		}
	}
	if !known {
		return errors.New("unknown push service")
	}
	if _, err := s.publicKey(); err != nil {
		return err
	}
	if auth, err := decodeKey(s.Keys.Auth); err != nil || len(auth) != 16 {
		return errors.New("invalid auth secret")
	}
	return nil
}

func (s Subscription) publicKey() (*ecdh.PublicKey, error) {
	raw, err := decodeKey(s.Keys.P256dh)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	key, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, errors.New("invalid p256dh key")
	}
	return key, nil
}

// decodeKey reads base64url, with or without padding, as browsers send it.
func decodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// loadKey reads the VAPID key from file, or makes one and writes it there.
func loadKey(file string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(filepath.Clean(file))
	if errors.Is(err, fs.ErrNotExist) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return nil, err
		}
		return key, os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New(file + " holds no PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New(file + " is not a P-256 key")
	}
	return key, nil
}

// publicKeyBytes is the VAPID key the browser subscribes with, uncompressed.
func publicKeyBytes(key *ecdsa.PrivateKey) []byte {
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil
	}
	return pub.Bytes()
}

// vapidHeader is the Authorization header for a message to endpoint.
func vapidHeader(key *ecdsa.PrivateKey, endpoint, contact string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims, _ := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": contact,
	})
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + unsigned + "." + enc.EncodeToString(sig) + ", k=" + enc.EncodeToString(publicKeyBytes(key)), nil
}

// encrypt encrypts payload for the subscription as one aes128gcm record
// (RFC 8291 and RFC 8188).
func encrypt(sub Subscription, payload []byte, random io.Reader) ([]byte, error) {
	uaPublic, err := sub.publicKey()
	if err != nil {
		return nil, err
	}
	authSecret, err := decodeKey(sub.Keys.Auth)
	if err != nil {
		return nil, err
	}
	asPrivate, err := ecdh.P256().GenerateKey(random)
	if err != nil {
		return nil, err
	}
	shared, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()
	keyInfo := slices.Concat([]byte("WebPush: info\x00"), uaPublic.Bytes(), asPublic)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(4096))
	out.WriteByte(65) // the length of asPublic, an uncompressed P-256 key
	out.Write(asPublic)
	// 0x02 marks the last (and only) record.
	return gcm.Seal(out.Bytes(), nonce, append(payload, 2), nil), nil
}

// errGone means the browser dropped the subscription.
var errGone = errors.New("subscription gone")

// push sends payload to the subscription.
func (n *Notifier) push(sub Subscription, payload []byte, urgent bool) error {
	body, err := encrypt(sub, payload, rand.Reader)
	if err != nil {
		return err
	}
	auth, err := vapidHeader(n.key, sub.Endpoint, n.cfg.PushContact, n.cfg.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int((4 * 24 * time.Hour).Seconds())))
	if urgent {
		req.Header.Set("Urgency", "high")
	}
	resp, err := n.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service answered %s", resp.Status)
	}
	return nil
}
