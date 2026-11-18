// Package vault opens the vault (Tresor) outside the browser, in the same
// format as frontend/src/app/vault/vault-crypto.ts, so the sync client can
// read and add files the website encrypted and the other way round. It
// uses only the standard library's crypto (PBKDF2, HKDF, AES-256-GCM).
//
//   - The vault key is 32 random bytes, stored on the server wrapped with
//     AES-256-GCM: once under a key from the passphrase (PBKDF2-SHA256) and
//     once under one from the recovery key (HKDF-SHA256).
//   - Names are sealed with AES-256-GCM under a key derived from the vault
//     key: base64url(nonce || ciphertext || tag).
//   - Files use the STREAM construction: a key per file from HKDF-SHA256
//     with a random 32-byte salt, then 1 MiB chunks sealed with AES-256-GCM
//     under the nonce counter (11 bytes, big endian) || last flag (1 byte).
//     Layout: "DDV1" || salt || chunks.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Chunk is how much of a file one sealed chunk holds.
	Chunk = 1 << 20
	// MaxNameBytes is the longest name in UTF-8 bytes, so the sealed name
	// fits in 255 characters.
	MaxNameBytes = 160
	// MaxPlain is the largest file the server takes into the vault.
	MaxPlain = 1 << 30

	magic    = "DDV1"
	saltSize = 32
	tagSize  = 16
	// headerSize is the magic and the salt before the first chunk.
	headerSize = len(magic) + saltSize
	// minIterations is the fewest PBKDF2 rounds a stored key may ask for, as
	// in the browser.
	minIterations = 100_000
	// maxIterations keeps a forged keys.json from stalling the client.
	maxIterations = 10_000_000
)

// ErrWrongSecret means the passphrase or the recovery key does not open the
// vault.
var ErrWrongSecret = errors.New("die Passphrase oder der Wiederherstellungsschlüssel stimmt nicht")

// ErrDamaged means a file does not decrypt in full: damaged, cut off or from
// another vault.
var ErrDamaged = errors.New("die Datei lässt sich nicht entschlüsseln: beschädigt oder aus einem anderen Tresor")

// Keys is what the server keeps: the vault key, wrapped twice.
type Keys struct {
	Version    int `json:"version"`
	Passphrase struct {
		KDF        string `json:"kdf"`
		Iterations int    `json:"iterations"`
		Salt       string `json:"salt"`
		IV         string `json:"iv"`
		Wrapped    string `json:"wrapped"`
	} `json:"passphrase"`
	Recovery struct {
		KDF     string `json:"kdf"`
		Salt    string `json:"salt"`
		IV      string `json:"iv"`
		Wrapped string `json:"wrapped"`
	} `json:"recovery"`
}

// Vault is an open vault. Its keys stay in memory only.
type Vault struct {
	master []byte
	names  cipher.AEAD
}

// Open opens the vault with secret: a recovery key when it reads as one,
// otherwise, or when the recovery key does not fit, the passphrase.
func Open(keys Keys, secret string) (*Vault, error) {
	if keys.Version != 1 {
		return nil, fmt.Errorf("unbekanntes Schlüsselformat %d", keys.Version)
	}
	if recovery := ParseRecovery(secret); recovery != nil {
		if raw, err := openRecovery(keys, recovery); err == nil {
			return fromKey(raw)
		}
	}
	raw, err := openPassphrase(keys, secret)
	if err != nil {
		return nil, err
	}
	return fromKey(raw)
}

func openPassphrase(keys Keys, passphrase string) ([]byte, error) {
	p := keys.Passphrase
	if p.KDF != "PBKDF2-SHA256" || p.Iterations < minIterations || p.Iterations > maxIterations {
		return nil, errors.New("unbekanntes Schlüsselformat")
	}
	salt, err := base64.StdEncoding.DecodeString(p.Salt)
	if err != nil {
		return nil, errors.New("unbekanntes Schlüsselformat")
	}
	kek, err := pbkdf2.Key(sha256.New, passphrase, salt, p.Iterations, 32)
	if err != nil {
		return nil, err
	}
	return unwrap(kek, p.IV, p.Wrapped)
}

func openRecovery(keys Keys, recovery []byte) ([]byte, error) {
	r := keys.Recovery
	if r.KDF != "HKDF-SHA256" {
		return nil, errors.New("unbekanntes Schlüsselformat")
	}
	salt, err := base64.StdEncoding.DecodeString(r.Salt)
	if err != nil {
		return nil, errors.New("unbekanntes Schlüsselformat")
	}
	kek, err := hkdf.Key(sha256.New, recovery, salt, "data-den vault recovery", 32)
	if err != nil {
		return nil, err
	}
	return unwrap(kek, r.IV, r.Wrapped)
}

func unwrap(kek []byte, iv, wrapped string) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(iv)
	if err != nil {
		return nil, ErrWrongSecret
	}
	sealed, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, ErrWrongSecret
	}
	aead, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, ErrWrongSecret
	}
	raw, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil || len(raw) != 32 {
		return nil, ErrWrongSecret
	}
	return raw, nil
}

func fromKey(raw []byte) (*Vault, error) {
	key, err := hkdf.Key(sha256.New, raw, nil, "data-den vault names", 32)
	if err != nil {
		return nil, err
	}
	names, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return &Vault{master: raw, names: names}, nil
}

// Close forgets the vault key.
func (v *Vault) Close() {
	clear(v.master)
	v.names = nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// CheckName returns an error when name cannot go into the vault.
func CheckName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return errors.New("ungültiger Name")
	case len(name) > MaxNameBytes:
		return fmt.Errorf("im Tresor sind Namen bis %d Bytes erlaubt", MaxNameBytes)
	case !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00"):
		return errors.New("ungültiger Name")
	}
	return nil
}

// SealName is the name as the server stores it.
func (v *Vault) SealName(name string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	nonce := make([]byte, v.names.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(v.names.Seal(nonce, nonce, []byte(name), nil)), nil
}

// OpenName is the name behind a sealed one; false when it does not open.
func (v *Vault) OpenName(sealed string) (string, bool) {
	data, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(data) < v.names.NonceSize()+tagSize {
		return "", false
	}
	n := v.names.NonceSize()
	plain, err := v.names.Open(nil, data[:n], data[n:], nil)
	if err != nil || !utf8.Valid(plain) {
		return "", false
	}
	return string(plain), true
}

func (v *Vault) fileKey(salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, v.master, salt, "data-den vault file", 32)
	if err != nil {
		return nil, err
	}
	return gcm(key)
}

func chunkNonce(counter uint32, last bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint32(nonce[7:11], counter)
	if last {
		nonce[11] = 1
	}
	return nonce
}

// SealedSize is the size of a file of size bytes once sealed.
func SealedSize(size int64) int64 {
	chunks := max(1, (size+Chunk-1)/Chunk)
	return int64(headerSize) + size + tagSize*chunks
}

// PlainSize is the size of a file before it was sealed, from the sealed
// size, as the server lists it.
func PlainSize(sealed int64) int64 {
	body := sealed - int64(headerSize)
	return max(0, body-tagSize*((body+Chunk+tagSize-1)/(Chunk+tagSize)))
}

// Seal writes the file of size bytes from r to w, sealed. r must give
// exactly size bytes.
func (v *Vault) Seal(w io.Writer, r io.Reader, size int64) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	aead, err := v.fileKey(salt)
	if err != nil {
		return err
	}
	if _, err := w.Write(append([]byte(magic), salt...)); err != nil {
		return err
	}
	buf := make([]byte, Chunk, Chunk+tagSize)
	var counter uint32
	for left := size; ; {
		n := min(left, Chunk)
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return fmt.Errorf("lesen: %w", err)
		}
		left -= n
		sealed := aead.Seal(buf[:0], chunkNonce(counter, left == 0), buf[:n], nil)
		if _, err := w.Write(sealed); err != nil {
			return err
		}
		if left == 0 {
			return nil
		}
		counter++
	}
}

// Unseal writes the file sealed in r to w. Each chunk is checked before it
// is written; ErrDamaged when the file does not decrypt in full, in which
// case w holds only a part of it and must be thrown away.
func (v *Vault) Unseal(w io.Writer, r io.Reader) error {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:len(magic)]) != magic {
		return ErrDamaged
	}
	aead, err := v.fileKey(header[len(magic):])
	if err != nil {
		return err
	}
	// One byte more than a chunk shows whether another chunk follows.
	buf := make([]byte, Chunk+tagSize+1)
	n, err := io.ReadFull(r, buf)
	var counter uint32
	for {
		last := false
		switch {
		case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
			last = true
		case err != nil:
			return err
		}
		size := min(n, Chunk+tagSize)
		if size < tagSize {
			return ErrDamaged
		}
		plain, openErr := aead.Open(nil, chunkNonce(counter, last), buf[:size], nil)
		if openErr != nil {
			return ErrDamaged
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
		if last {
			return nil
		}
		counter++
		// The extra byte starts the next chunk.
		buf[0] = buf[size]
		var m int
		m, err = io.ReadFull(r, buf[1:])
		n = m + 1
	}
}

// Crockford's base32, for a recovery key that can be typed.
const base32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ParseRecovery returns the 32 bytes of a typed recovery key, such as
// "ABCD-EFGH-...", or nil when it is not one.
func ParseRecovery(text string) []byte {
	chars := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r) || r == '-':
			return -1
		case r == 'I' || r == 'L':
			return '1'
		case r == 'O':
			return '0'
		}
		return r
	}, strings.ToUpper(text))
	if len(chars) != 52 {
		return nil
	}
	out := make([]byte, 0, 32)
	var value, bits uint
	for _, c := range chars {
		v := strings.IndexRune(base32, c)
		if v < 0 {
			return nil
		}
		value = (value<<5 | uint(v)) & 0xffff
		bits += 5
		if bits >= 8 {
			if len(out) == 32 {
				return nil
			}
			out = append(out, byte(value>>(bits-8)&0xff))
			bits -= 8
		}
	}
	// 52 characters hold 260 bits: the last 4 must be zero.
	if len(out) != 32 || value&(1<<bits-1) != 0 {
		return nil
	}
	return out
}
