package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// fixture is testdata/web.json, made by the website's vault-crypto.ts (with
// 100 000 PBKDF2 rounds to keep the test quick), so the client opens what
// the browser sealed.
type fixture struct {
	Passphrase string            `json:"passphrase"`
	Recovery   string            `json:"recovery"`
	Keys       Keys              `json:"keys"`
	Names      map[string]string `json:"names"`
	Files      map[string]struct {
		Plain  string `json:"plain"`
		Sealed string `json:"sealed"`
	} `json:"files"`
}

func load(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/web.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func open(t *testing.T) (*Vault, fixture) {
	t.Helper()
	f := load(t)
	v, err := Open(f.Keys, f.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return v, f
}

func TestOpenWithPassphraseAndRecovery(t *testing.T) {
	f := load(t)
	byPass, err := Open(f.Keys, f.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	// Typed in lower case, with spaces instead of dashes.
	typed := strings.ToLower(strings.ReplaceAll(f.Recovery, "-", " "))
	byRecovery, err := Open(f.Keys, typed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(byPass.master, byRecovery.master) {
		t.Error("passphrase and recovery key open different keys")
	}
	for _, wrong := range []string{"", "falsch", f.Passphrase + " ", strings.Replace(f.Recovery, "N", "M", 1)} {
		if _, err := Open(f.Keys, wrong); !errors.Is(err, ErrWrongSecret) {
			t.Errorf("%q: %v, want ErrWrongSecret", wrong, err)
		}
	}
}

func TestOpenRefusesStrangeKeys(t *testing.T) {
	f := load(t)
	for name, change := range map[string]func(*Keys){
		"version":    func(k *Keys) { k.Version = 2 },
		"kdf":        func(k *Keys) { k.Passphrase.KDF = "scrypt" },
		"few rounds": func(k *Keys) { k.Passphrase.Iterations = 1000 },
		"many":       func(k *Keys) { k.Passphrase.Iterations = 1 << 30 },
	} {
		keys := f.Keys
		change(&keys)
		if _, err := Open(keys, f.Passphrase); err == nil || errors.Is(err, ErrWrongSecret) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNamesFromTheBrowser(t *testing.T) {
	v, f := open(t)
	for name, sealed := range f.Names {
		got, ok := v.OpenName(sealed)
		if !ok || got != name {
			t.Errorf("%q: %q, %v", sealed, got, ok)
		}
		again, err := v.SealName(name)
		if err != nil {
			t.Fatal(err)
		}
		if again == sealed {
			t.Error("the same name sealed twice looks the same")
		}
		if got, ok := v.OpenName(again); !ok || got != name {
			t.Errorf("round trip of %q: %q", name, got)
		}
	}
	for _, bad := range []string{"", "!!", f.Names["Steuer 2025.pdf"][:30], f.Names["Steuer 2025.pdf"] + "A"} {
		if got, ok := v.OpenName(bad); ok {
			t.Errorf("%q opened as %q", bad, got)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, strings.Repeat("x", MaxNameBytes+1)} {
		if _, err := v.SealName(bad); err == nil {
			t.Errorf("%q sealed", bad)
		}
	}
	if sealed, err := v.SealName(strings.Repeat("ä", MaxNameBytes/2)); err != nil || len(sealed) > 255 {
		t.Errorf("longest name: %d characters, %v", len(sealed), err)
	}
}

func TestFilesFromTheBrowser(t *testing.T) {
	v, f := open(t)
	for name, file := range f.Files {
		plain, _ := base64.StdEncoding.DecodeString(file.Plain)
		sealed, _ := base64.StdEncoding.DecodeString(file.Sealed)
		var out bytes.Buffer
		if err := v.Unseal(&out, bytes.NewReader(sealed)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Errorf("%s: %q, want %q", name, out.Bytes(), plain)
		}
		if got := PlainSize(int64(len(sealed))); got != int64(len(plain)) {
			t.Errorf("%s: PlainSize %d, want %d", name, got, len(plain))
		}
		if got := SealedSize(int64(len(plain))); got != int64(len(sealed)) {
			t.Errorf("%s: SealedSize %d, want %d", name, got, len(sealed))
		}
	}
}

func TestSealAndUnseal(t *testing.T) {
	v, _ := open(t)
	for _, size := range []int{0, 1, Chunk - 1, Chunk, Chunk + 1, 2 * Chunk, 2*Chunk + Chunk/2} {
		plain := make([]byte, size)
		for i := range plain {
			plain[i] = byte(i*7 + i>>11)
		}
		var sealed bytes.Buffer
		if err := v.Seal(&sealed, bytes.NewReader(plain), int64(size)); err != nil {
			t.Fatal(err)
		}
		if int64(sealed.Len()) != SealedSize(int64(size)) || PlainSize(int64(sealed.Len())) != int64(size) {
			t.Errorf("%d bytes: sealed %d, SealedSize %d", size, sealed.Len(), SealedSize(int64(size)))
		}
		var out bytes.Buffer
		if err := v.Unseal(&out, bytes.NewReader(sealed.Bytes())); err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Errorf("%d bytes: round trip differs", size)
		}
	}
}

func TestUnsealNoticesDamage(t *testing.T) {
	v, _ := open(t)
	plain := bytes.Repeat([]byte("x"), 2*Chunk+100)
	var buf bytes.Buffer
	if err := v.Seal(&buf, bytes.NewReader(plain), int64(len(plain))); err != nil {
		t.Fatal(err)
	}
	sealed := buf.Bytes()
	chunk := Chunk + tagSize
	for name, damaged := range map[string][]byte{
		"cut after a chunk": sealed[:headerSize+chunk],
		"cut in a chunk":    sealed[:len(sealed)-1],
		"last chunk gone":   sealed[:headerSize+2*chunk],
		"flipped bit":       flip(sealed, headerSize+chunk+5),
		"other salt":        flip(sealed, 10),
		"no magic":          flip(sealed, 0),
		"only the header":   sealed[:headerSize],
		"swapped chunks": join(sealed[:headerSize], sealed[headerSize+chunk:headerSize+2*chunk],
			sealed[headerSize:headerSize+chunk], sealed[headerSize+2*chunk:]),
	} {
		if err := v.Unseal(&bytes.Buffer{}, bytes.NewReader(damaged)); !errors.Is(err, ErrDamaged) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other, err := fromKey(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Unseal(&bytes.Buffer{}, bytes.NewReader(sealed)); !errors.Is(err, ErrDamaged) {
		t.Errorf("another vault: %v", err)
	}
}

func join(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func flip(b []byte, i int) []byte {
	c := append([]byte{}, b...)
	c[i] ^= 1
	return c
}

func TestParseRecovery(t *testing.T) {
	f := load(t)
	key := ParseRecovery(f.Recovery)
	if len(key) != 32 {
		t.Fatalf("%q: %v", f.Recovery, key)
	}
	// O for 0 and I or L for 1, as people type them.
	typed := strings.NewReplacer("0", "o", "1", "l").Replace(f.Recovery)
	if !bytes.Equal(ParseRecovery(typed), key) {
		t.Errorf("%q differs", typed)
	}
	for _, bad := range []string{"", f.Recovery[:len(f.Recovery)-1], f.Recovery + "0", strings.Replace(f.Recovery, "N", "U", 1), f.Recovery[:len(f.Recovery)-1] + "1"} {
		if ParseRecovery(bad) != nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
