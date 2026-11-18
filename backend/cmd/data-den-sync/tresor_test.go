package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/desktop"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/vault"
)

// webVault is the vault the website made in internal/vault/testdata.
type webVault struct {
	Passphrase string            `json:"passphrase"`
	Keys       json.RawMessage   `json:"keys"`
	Names      map[string]string `json:"names"`
	Files      map[string]struct {
		Plain  string `json:"plain"`
		Sealed string `json:"sealed"`
	} `json:"files"`
}

// tresorServer runs a DATA-DEN server with the website's vault in it, one
// file of which the browser uploaded as "Steuer 2025.pdf".
func tresorServer(t *testing.T) (*desktop.Client, webVault) {
	t.Helper()
	data, err := os.ReadFile("../../internal/vault/testdata/web.json")
	if err != nil {
		t.Fatal(err)
	}
	var web webVault
	if err := json.Unmarshal(data, &web); err != nil {
		t.Fatal(err)
	}
	f, err := files.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	keys, _ := json.Marshal(map[string]any{"keys": web.Keys, "proof": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	resp, err := http.Post(srv.URL+"/api/vault/keys", "application/json", bytes.NewReader(keys)) //nolint:noctx // a test
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("setting up the vault: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	client := &desktop.Client{Server: srv.URL, Token: "dd_test"}
	sealed, _ := base64.StdEncoding.DecodeString(web.Files["small"].Sealed)
	if err := client.VaultUpload(context.Background(), "/"+web.Names["Steuer 2025.pdf"], bytes.NewReader(sealed), int64(len(sealed))); err != nil {
		t.Fatal(err)
	}
	return client, web
}

// session runs the prompt with these lines typed, the passphrase first.
func session(t *testing.T, client *desktop.Client, secret string, lines ...string) (string, []string) {
	t.Helper()
	var out bytes.Buffer
	var opened []string
	tr := &tresor{
		client: client,
		in:     bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n")),
		out:    &out,
		now:    time.Now,
		open: func(name string) error {
			opened = append(opened, name)
			return nil
		},
	}
	tr.secret = func(string) (string, error) { return secret, nil }
	if err := tr.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if tr.temp != "" || tr.vault != nil {
		t.Error("still open after the end")
	}
	return out.String(), opened
}

func TestTresorReadsWhatTheBrowserSealed(t *testing.T) {
	client, web := tresorServer(t)
	dir := t.TempDir()
	out, _ := session(t, client, web.Passphrase, "ls", `get "Steuer 2025.pdf" `+dir)
	if !strings.Contains(out, "Steuer 2025.pdf") || !strings.Contains(out, "39 B") {
		t.Errorf("listing:\n%s", out)
	}
	want, _ := base64.StdEncoding.DecodeString(web.Files["small"].Plain)
	got, err := os.ReadFile(filepath.Join(dir, "Steuer 2025.pdf")) //nolint:gosec // a test file
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("downloaded %q, %v; want %q\n%s", got, err, want, out)
	}
}

func TestTresorCommands(t *testing.T) {
	client, web := tresorServer(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "Befund.txt")
	content := bytes.Repeat([]byte("vertraulich "), 200_000) // over two chunks
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}
	out, opened := session(t, client, web.Passphrase,
		"mkdir Arzt",
		"cd Arzt",
		"put "+local,
		`put `+local+` "Befund Mai.txt"`,
		"put "+local, // there already
		"mv Befund.txt Alt.txt",
		"ls",
		"open Alt.txt",
		"get Alt.txt "+filepath.Join(dir, "zurück.txt"),
		"rm Alt.txt", "nein",
		"rm Alt.txt", "ja",
		"cd ..",
		"cd Steuer 2025.pdf",
		"frag",
	)
	for _, want := range []string{
		"Fehler: Befund.txt gibt es hier schon",
		"Fehler: falsche Anzahl Angaben für cd",
		"Fehler: unbekannter Befehl frag",
		"Nicht gelöscht.",
		"Gelöscht.",
		"Tresor /Arzt> ",
		"2.4 MB  Befund Mai.txt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	back, err := os.ReadFile(filepath.Join(dir, "zurück.txt")) //nolint:gosec // a test file
	if err != nil || !bytes.Equal(back, content) {
		t.Errorf("round trip: %d bytes, %v", len(back), err)
	}
	if len(opened) != 1 || filepath.Base(opened[0]) != "Alt.txt" {
		t.Errorf("opened %v", opened)
	} else if _, err := os.Stat(opened[0]); !os.IsNotExist(err) {
		t.Errorf("the decrypted copy is still there: %v", err)
	}

	// What is left, as the server keeps it: nothing in plain text.
	ctx := context.Background()
	top, err := client.VaultList(ctx, "/")
	if err != nil || len(top) != 2 {
		t.Fatalf("top: %v %v", top, err)
	}
	keys, _ := client.VaultKeys(ctx)
	v, err := vault.Open(*keys, web.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		if name, _ := v.OpenName(e.Name); name == "Arzt" {
			inside, err := client.VaultList(ctx, "/"+e.Name)
			if err != nil || len(inside) != 1 || strings.Contains(inside[0].Name, "Befund") {
				t.Errorf("in Arzt: %v %v", inside, err)
			}
			if name, _ := v.OpenName(inside[0].Name); name != "Befund Mai.txt" {
				t.Errorf("in Arzt: %q", name)
			}
		}
	}
}

func TestTresorWrongPassphrase(t *testing.T) {
	client, _ := tresorServer(t)
	tr := &tresor{client: client, in: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}, now: time.Now}
	tr.secret = func(string) (string, error) { return "falsch", nil }
	if err := tr.run(context.Background()); err == nil {
		t.Error("opened with a wrong passphrase")
	}
}

func TestTresorLocksWhenIdle(t *testing.T) {
	client, web := tresorServer(t)
	start := time.Now()
	// The minutes each look at the clock sees: the unlock, the first ls and
	// after it, then 20 minutes later the second ls, its unlock and after it.
	minutes := []int{0, 1, 2, 22, 23, 24}
	asked := 0
	var out bytes.Buffer
	tr := &tresor{
		client: client,
		in:     bufio.NewReader(strings.NewReader("ls\nls\n")),
		out:    &out,
		now: func() time.Time {
			m := minutes[0]
			minutes = minutes[1:]
			return start.Add(time.Duration(m) * time.Minute)
		},
	}
	tr.secret = func(string) (string, error) {
		asked++
		return web.Passphrase, nil
	}
	if err := tr.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked != 2 || !strings.Contains(out.String(), "gesperrt") {
		t.Errorf("asked %d times:\n%s", asked, out.String())
	}
}

func TestSplitArgs(t *testing.T) {
	for line, want := range map[string]string{
		"ls\r\n":                     "ls",
		`get "Steuer 2025.pdf" C:\x`: `get|Steuer 2025.pdf|C:\x`,
		`  mv  a   "b c"  `:          "mv|a|b c",
		`mkdir ""`:                   "mkdir|",
	} {
		got, err := splitArgs(line)
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("%q: %q, %v", line, got, err)
		}
	}
	if _, err := splitArgs(`get "offen`); err == nil {
		t.Error("an open quote was accepted")
	}
}
