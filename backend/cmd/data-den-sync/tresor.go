package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/desktop"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/vault"
)

// idleLock is how long the vault stays open without a command, as on the
// website.
const idleLock = 15 * time.Minute

const tresorHelp = `Befehle im Tresor:
  ls                   zeigt den Ordner
  cd <Ordner>          wechselt in einen Ordner; cd .. eine Ebene hoch, cd / zum Anfang
  get <Name> [Ziel]    lädt eine Datei entschlüsselt auf diesen PC (Ziel: Ordner oder neuer Dateiname)
  put <Datei> [Name]   lädt eine Datei von diesem PC verschlüsselt in den Ordner
  open <Name>          öffnet eine entschlüsselte Kopie mit dem passenden Programm
  mkdir <Name>         legt einen Ordner an
  mv <Name> <Neu>      benennt um
  rm <Name>            löscht endgültig, nach einer Rückfrage
  lock                 sperrt den Tresor
  exit                 sperrt und beendet
Namen mit Leerzeichen in Anführungszeichen: get "Steuer 2025.pdf"`

// tresor is the vault (Tresor) at a prompt: the passphrase or recovery key
// opens it on this PC, files are encrypted and decrypted here, and the key
// is never stored. Nothing of the vault is synced.
type tresor struct {
	client *desktop.Client
	in     *bufio.Reader
	out    io.Writer
	// secret asks for the passphrase or recovery key without showing it.
	secret func(prompt string) (string, error)
	// open shows a file with the program the system has for it.
	open func(name string) error
	now  func() time.Time

	keys  vault.Keys
	vault *vault.Vault
	used  time.Time
	// dir is the path from the vault's top, as sealed and plain names.
	dir []folder
	// temp holds the copies "open" decrypted; removed when the vault locks.
	temp string
}

type folder struct{ sealed, name string }

// item is an entry of the current folder with its name opened.
type item struct {
	desktop.VaultEntry
	plain string
}

func runTresor(ctx context.Context) error {
	cfg, token, err := loadConfig()
	if err != nil {
		return err
	}
	t := &tresor{
		// No time limit for a whole file: Ctrl+C stops a slow one.
		client: &desktop.Client{Server: cfg.Server, Token: token, HTTP: &http.Client{}},
		in:     bufio.NewReader(os.Stdin),
		out:    os.Stdout,
		open:   openFile,
		now:    time.Now,
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		// Ctrl+C while the passphrase is typed must not leave the echo off.
		if state, err := term.GetState(fd); err == nil {
			defer func() { _ = term.Restore(fd, state) }()
		}
	}
	t.secret = func(prompt string) (string, error) {
		_, _ = fmt.Fprint(t.out, prompt)
		if term.IsTerminal(fd) {
			b, err := term.ReadPassword(fd)
			_, _ = fmt.Fprintln(t.out)
			return string(b), err
		}
		return readLine(t.in)
	}
	return t.run(ctx)
}

// readLine reads one line without its line break; io.EOF only when there
// is none left.
func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// wait runs read, a wait for the keyboard, until it is done or Ctrl+C
// cancels ctx.
func wait(ctx context.Context, read func() (string, error)) (string, error) {
	type result struct {
		s   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := read()
		done <- result{s, err}
	}()
	select {
	case r := <-done:
		return r.s, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t *tresor) run(ctx context.Context) error {
	keys, err := t.client.VaultKeys(ctx)
	if err != nil {
		return err
	}
	if keys == nil {
		return errors.New("noch kein Tresor: zuerst auf der Website im Tab Tresor einrichten")
	}
	t.keys = *keys
	defer t.lock()
	if err := t.unlock(ctx); err != nil {
		return quiet(ctx, err)
	}
	_, _ = fmt.Fprintln(t.out, "Tresor offen. help zeigt die Befehle.")
	for {
		_, _ = fmt.Fprintf(t.out, "Tresor %s> ", t.where())
		line, err := wait(ctx, func() (string, error) { return readLine(t.in) })
		if errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintln(t.out)
			return nil
		}
		if err != nil {
			return quiet(ctx, err)
		}
		args, err := splitArgs(line)
		if err != nil {
			_, _ = fmt.Fprintln(t.out, "Fehler:", err)
			continue
		}
		if len(args) == 0 {
			continue
		}
		if args[0] == "exit" || args[0] == "quit" {
			return nil
		}
		if t.vault == nil || t.now().Sub(t.used) > idleLock {
			if t.vault != nil {
				_, _ = fmt.Fprintln(t.out, "Nach 15 Minuten ohne Befehl gesperrt.")
				t.lock()
			}
			if err := t.unlock(ctx); err != nil {
				return quiet(ctx, err)
			}
		}
		if err := t.command(ctx, args); err != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintln(t.out, "Fehler:", err)
		}
		t.used = t.now()
		if ctx.Err() != nil {
			_, _ = fmt.Fprintln(t.out, "\nAbgebrochen, Tresor gesperrt.")
			return nil
		}
	}
}

// quiet drops the error Ctrl+C caused.
func quiet(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		_, _ = fmt.Fprintln(os.Stderr, "\nAbgebrochen, Tresor gesperrt.")
		return nil
	}
	return err
}

// unlock asks for the passphrase until it opens the vault, three times at
// most.
func (t *tresor) unlock(ctx context.Context) error {
	for try := 0; try < 3; try++ {
		secret, err := wait(ctx, func() (string, error) { return t.secret("Passphrase oder Wiederherstellungsschlüssel: ") })
		if err != nil {
			return err
		}
		v, err := vault.Open(t.keys, secret)
		if errors.Is(err, vault.ErrWrongSecret) {
			_, _ = fmt.Fprintln(t.out, err)
			continue
		}
		if err != nil {
			return err
		}
		t.vault, t.used, t.dir = v, t.now(), nil
		return nil
	}
	return vault.ErrWrongSecret
}

// lock forgets the key and removes the copies "open" made.
func (t *tresor) lock() {
	if t.vault != nil {
		t.vault.Close()
		t.vault = nil
	}
	if t.temp != "" {
		if err := os.RemoveAll(t.temp); err != nil {
			_, _ = fmt.Fprintf(t.out, "Die entschlüsselten Kopien in %s ließen sich nicht ganz löschen (noch geöffnet?): %v\n", t.temp, err)
		}
		t.temp = ""
	}
}

func (t *tresor) command(ctx context.Context, args []string) error {
	want := func(least, most int) error {
		if len(args)-1 < least || len(args)-1 > most {
			return fmt.Errorf("falsche Anzahl Angaben für %s; help zeigt die Befehle", args[0])
		}
		return nil
	}
	var err error
	switch args[0] {
	case "help", "?":
		_, _ = fmt.Fprintln(t.out, tresorHelp)
	case "ls", "dir":
		if err = want(0, 0); err == nil {
			err = t.list(ctx)
		}
	case "cd":
		if err = want(1, 1); err == nil {
			err = t.cd(ctx, args[1])
		}
	case "get":
		if err = want(1, 2); err == nil {
			target := "."
			if len(args) == 3 {
				target = args[2]
			}
			err = t.get(ctx, args[1], target)
		}
	case "put":
		if err = want(1, 2); err == nil {
			name := filepath.Base(args[1])
			if len(args) == 3 {
				name = args[2]
			}
			err = t.put(ctx, args[1], name)
		}
	case "open":
		if err = want(1, 1); err == nil {
			err = t.openCopy(ctx, args[1])
		}
	case "mkdir":
		if err = want(1, 1); err == nil {
			err = t.mkdir(ctx, args[1])
		}
	case "mv", "rename":
		if err = want(2, 2); err == nil {
			err = t.rename(ctx, args[1], args[2])
		}
	case "rm", "del":
		if err = want(1, 1); err == nil {
			err = t.remove(ctx, args[1])
		}
	case "lock":
		t.lock()
		_, _ = fmt.Fprintln(t.out, "Gesperrt.")
	default:
		err = fmt.Errorf("unbekannter Befehl %s; help zeigt die Befehle", args[0])
	}
	if errors.Is(err, desktop.ErrChanged) {
		return errors.New("existiert bereits")
	}
	return err
}

// where is the current folder as plain names, such as "/Steuern/2025".
func (t *tresor) where() string {
	names := make([]string, len(t.dir))
	for i, f := range t.dir {
		names[i] = f.name
	}
	return "/" + strings.Join(names, "/")
}

// remote is the path of sealed names to the current folder, or to sealed
// in it.
func (t *tresor) remote(sealed string) string {
	parts := make([]string, 0, len(t.dir)+1)
	for _, f := range t.dir {
		parts = append(parts, f.sealed)
	}
	if sealed != "" {
		parts = append(parts, sealed)
	}
	return "/" + strings.Join(parts, "/")
}

// items lists the current folder with plain names, folders first.
func (t *tresor) items(ctx context.Context) ([]item, error) {
	entries, err := t.client.VaultList(ctx, t.remote(""))
	if err != nil {
		return nil, err
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		plain, ok := t.vault.OpenName(e.Name)
		if !ok {
			plain = "(unlesbarer Name " + e.Name[:12] + "...)"
		}
		items = append(items, item{VaultEntry: e, plain: plain})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir
		}
		return strings.ToLower(items[i].plain) < strings.ToLower(items[j].plain)
	})
	return items, nil
}

// find is the entry called name in the current folder.
func (t *tresor) find(ctx context.Context, name string) (item, error) {
	items, err := t.items(ctx)
	if err != nil {
		return item{}, err
	}
	for _, it := range items {
		if it.plain == name {
			return it, nil
		}
	}
	return item{}, fmt.Errorf("%s gibt es hier nicht", name)
}

func (t *tresor) list(ctx context.Context) error {
	items, err := t.items(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		_, _ = fmt.Fprintln(t.out, "(leer)")
	}
	for _, it := range items {
		when := it.Modified
		if m, err := time.Parse(time.RFC3339, it.Modified); err == nil {
			when = m.Local().Format("02.01.2006 15:04")
		}
		if it.Dir {
			_, _ = fmt.Fprintf(t.out, "%-16s %10s  %s/\n", when, "Ordner", it.plain)
		} else {
			_, _ = fmt.Fprintf(t.out, "%-16s %10s  %s\n", when, size(vault.PlainSize(it.Size)), it.plain)
		}
	}
	return nil
}

func size(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f kB", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
	}
	return fmt.Sprintf("%.2f GB", float64(n)/1000/1000/1000)
}

func (t *tresor) cd(ctx context.Context, name string) error {
	switch name {
	case "/", "\\":
		t.dir = nil
		return nil
	case "..":
		if len(t.dir) > 0 {
			t.dir = t.dir[:len(t.dir)-1]
		}
		return nil
	}
	it, err := t.find(ctx, name)
	if err != nil {
		return err
	}
	if !it.Dir {
		return fmt.Errorf("%s ist kein Ordner", name)
	}
	t.dir = append(t.dir, folder{sealed: it.Name, name: it.plain})
	return nil
}

// download decrypts the file it into target, through a temporary file
// next to it, so a failed or forged download leaves nothing behind.
func (t *tresor) download(ctx context.Context, it item, target string) error {
	if it.Dir {
		return fmt.Errorf("%s ist ein Ordner; nur Dateien lassen sich laden", it.plain)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%s gibt es schon", target)
	}
	part, err := os.CreateTemp(filepath.Dir(target), ".data-den-tresor-*.part")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(part.Name()) }()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := t.vault.Unseal(part, pr)
		_ = pr.CloseWithError(err)
		done <- err
	}()
	err = t.client.VaultDownload(ctx, t.remote(it.Name), pw)
	_ = pw.CloseWithError(err)
	unsealErr := <-done
	if err == nil {
		err = unsealErr
	}
	if closeErr := part.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%s gibt es schon", target)
	}
	return os.Rename(part.Name(), target)
}

func (t *tresor) get(ctx context.Context, name, target string) error {
	it, err := t.find(ctx, name)
	if err != nil {
		return err
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		if !localName(it.plain) {
			return fmt.Errorf("%s ist auf diesem PC kein gültiger Dateiname; get mit einem Zielnamen", it.plain)
		}
		target = filepath.Join(target, it.plain)
	}
	if err := t.download(ctx, it, target); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(t.out, "Gespeichert: %s\n", target)
	return nil
}

func (t *tresor) openCopy(ctx context.Context, name string) error {
	it, err := t.find(ctx, name)
	if err != nil {
		return err
	}
	if !localName(it.plain) {
		return fmt.Errorf("%s ist auf diesem PC kein gültiger Dateiname; get mit einem Zielnamen", it.plain)
	}
	if t.temp == "" {
		if t.temp, err = os.MkdirTemp("", "data-den-tresor-"); err != nil {
			return err
		}
	}
	// A folder of its own, so two files of the same name can be open.
	dir, err := os.MkdirTemp(t.temp, "")
	if err != nil {
		return err
	}
	target := filepath.Join(dir, it.plain)
	if err := t.download(ctx, it, target); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(t.out, "Geöffnet: %s\nDiese Kopie wird beim Sperren gelöscht; Änderungen daran mit put wieder hochladen.\n", target)
	return t.open(target)
}

func (t *tresor) put(ctx context.Context, local, name string) error {
	if err := vault.CheckName(name); err != nil {
		return err
	}
	file, err := os.Open(local) //nolint:gosec // the file the user named
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s ist keine Datei", local)
	}
	if info.Size() > vault.MaxPlain {
		return errors.New("höchstens 1 GB je Datei im Tresor")
	}
	if _, err := t.find(ctx, name); err == nil {
		return fmt.Errorf("%s gibt es hier schon", name)
	}
	sealed, err := t.vault.SealName(name)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(t.vault.Seal(pw, file, info.Size())) }()
	err = t.client.VaultUpload(ctx, t.remote(sealed), pr, vault.SealedSize(info.Size()))
	_ = pr.CloseWithError(errors.New("upload ended"))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(t.out, "Verschlüsselt hochgeladen: %s\n", path.Join(t.where(), name))
	return nil
}

func (t *tresor) mkdir(ctx context.Context, name string) error {
	if err := vault.CheckName(name); err != nil {
		return err
	}
	if _, err := t.find(ctx, name); err == nil {
		return fmt.Errorf("%s gibt es hier schon", name)
	}
	sealed, err := t.vault.SealName(name)
	if err != nil {
		return err
	}
	return t.client.VaultMkdir(ctx, t.remote(sealed))
}

func (t *tresor) rename(ctx context.Context, name, to string) error {
	if err := vault.CheckName(to); err != nil {
		return err
	}
	it, err := t.find(ctx, name)
	if err != nil {
		return err
	}
	if _, err := t.find(ctx, to); err == nil {
		return fmt.Errorf("%s gibt es hier schon", to)
	}
	sealed, err := t.vault.SealName(to)
	if err != nil {
		return err
	}
	return t.client.VaultMove(ctx, t.remote(it.Name), t.remote(sealed))
}

func (t *tresor) remove(ctx context.Context, name string) error {
	it, err := t.find(ctx, name)
	if err != nil {
		return err
	}
	what := "die Datei"
	if it.Dir {
		what = "den Ordner mit allem darin"
	}
	answer, err := wait(ctx, func() (string, error) {
		return ask(t.in, t.out, fmt.Sprintf("%s %s endgültig löschen? Das lässt sich nicht rückgängig machen (ja/nein)", it.plain, what), "nein")
	})
	if err != nil {
		return err
	}
	if answer != "ja" && answer != "j" {
		_, _ = fmt.Fprintln(t.out, "Nicht gelöscht.")
		return nil
	}
	if err := t.client.VaultDelete(ctx, t.remote(it.Name)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(t.out, "Gelöscht.")
	return nil
}

// splitArgs splits a command line at spaces; "..." keeps spaces in a name.
func splitArgs(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote, started := false, false
	for _, r := range strings.TrimRight(line, "\r\n") {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case (r == ' ' || r == '\t') && !inQuote:
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, errors.New("ein Anführungszeichen fehlt")
	}
	if started {
		args = append(args, cur.String())
	}
	return args, nil
}
