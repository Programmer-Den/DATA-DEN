// Command data-den-sync keeps a folder on a PC and a folder in DATA-DEN the
// same, both ways. It logs in with an app password and runs in the
// background, one round every minute.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/desktop"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/version"
)

const usage = `Usage: data-den-sync <command>

  setup            set up the server, app password and folders
  run [--hidden]   sync now and then every minute; --hidden closes the window (Windows)
  once             sync once
  autostart on|off start "run --hidden" at login (Windows)
  version          print the version`

// Config is what setup asks for, kept in config.json.
type Config struct {
	// Server is the address, such as "https://nas.example.org".
	Server string `json:"server"`
	// Folder is the folder on the PC.
	Folder string `json:"folder"`
	// Remote is the folder in DATA-DEN, such as "/" or "/Dokumente".
	Remote string `json:"remote"`
	// Interval is the time between rounds in seconds.
	Interval int `json:"interval"`
}

func main() {
	command := "run"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "setup":
		err = setup(bufio.NewReader(os.Stdin), os.Stdout)
	case "run":
		err = run(len(os.Args) > 2 && os.Args[2] == "--hidden", false)
	case "once":
		err = run(false, true)
	case "autostart":
		if len(os.Args) != 3 || (os.Args[2] != "on" && os.Args[2] != "off") {
			err = errors.New("usage: data-den-sync autostart on|off")
		} else {
			err = autostart(os.Args[2] == "on")
		}
	case "version":
		fmt.Println(version.Version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

// configDir keeps the settings, the app password and the log, such as
// %AppData%\DATA-DEN-Sync on Windows.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "DATA-DEN-Sync"), nil
}

func loadConfig() (Config, string, error) {
	dir, err := configDir()
	if err != nil {
		return Config{}, "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // the user's own settings
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, "", errors.New("noch nicht eingerichtet: zuerst data-den-sync setup ausführen")
	}
	if err != nil {
		return Config{}, "", err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, "", fmt.Errorf("config.json: %w", err)
	}
	if cfg.Interval < 10 {
		cfg.Interval = 60
	}
	token := os.Getenv("DATA_DEN_SYNC_TOKEN")
	if token == "" {
		sealed, err := os.ReadFile(filepath.Join(dir, "token")) //nolint:gosec // the user's own settings
		if err != nil {
			return Config{}, "", fmt.Errorf("das App-Passwort fehlt: data-den-sync setup ausführen (%w)", err)
		}
		plain, err := unseal(sealed)
		if err != nil {
			return Config{}, "", fmt.Errorf("das App-Passwort ist nicht lesbar: data-den-sync setup erneut ausführen (%w)", err)
		}
		token = string(plain)
	}
	return cfg, token, nil
}

// checkServer accepts an HTTPS address; plain HTTP only for this PC itself.
func checkServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("die Adresse muss wie https://nas.example.org aussehen")
	}
	host := u.Hostname()
	if u.Scheme != "https" && (u.Scheme != "http" || (host != "localhost" && host != "127.0.0.1" && host != "::1")) {
		return "", errors.New("die Adresse muss mit https:// beginnen, damit die Verbindung verschlüsselt ist")
	}
	return u.String(), nil
}

// checkToken accepts an app password as the web interface creates it.
func checkToken(token string) error {
	if !strings.HasPrefix(token, "dd_") || len(token) < 20 || len(token) > 200 || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("das App-Passwort beginnt mit dd_; erstelle eins im Web-Interface unter Konto")
	}
	return nil
}

// checkRemote turns "Dokumente/2026" into "/Dokumente/2026".
func checkRemote(remote string) (string, error) {
	remote = "/" + strings.Trim(strings.TrimSpace(strings.ReplaceAll(remote, "\\", "/")), "/")
	for _, part := range strings.Split(strings.Trim(remote, "/"), "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".data-den-") {
			return "", errors.New("ungültiger Ordner in DATA-DEN")
		}
	}
	return remote, nil
}

func checkFolder(folder string) (string, error) {
	abs, err := filepath.Abs(strings.Trim(strings.TrimSpace(folder), `"`))
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s ist kein Ordner", abs)
	}
	return abs, nil
}

func ask(in *bufio.Reader, out io.Writer, question, fallback string) (string, error) {
	if fallback != "" {
		_, _ = fmt.Fprintf(out, "%s [%s]: ", question, fallback)
	} else {
		_, _ = fmt.Fprintf(out, "%s: ", question)
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		if fallback == "" && errors.Is(err, io.EOF) {
			return "", errors.New("abgebrochen")
		}
		return fallback, nil
	}
	return line, nil
}

func setup(in *bufio.Reader, out io.Writer) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	old, _, _ := loadConfig()
	raw, err := ask(in, out, "Adresse von DATA-DEN", old.Server)
	if err != nil {
		return err
	}
	server, err := checkServer(raw)
	if err != nil {
		return err
	}
	token, err := ask(in, out, "App-Passwort (im Web-Interface unter Konto erstellen)", "")
	if err != nil {
		return err
	}
	if err := checkToken(token); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	raw, err = ask(in, out, "Ordner auf diesem PC", cmpOr(old.Folder, filepath.Join(home, "DATA-DEN")))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(strings.Trim(raw, `"`), 0o750); err != nil {
		return err
	}
	folder, err := checkFolder(raw)
	if err != nil {
		return err
	}
	raw, err = ask(in, out, "Ordner in DATA-DEN", cmpOr(old.Remote, "/"))
	if err != nil {
		return err
	}
	remote, err := checkRemote(raw)
	if err != nil {
		return err
	}

	client := &desktop.Client{Server: server, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.Tree(ctx, remote); err != nil {
		return fmt.Errorf("keine Verbindung zu %s: %w", server, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sealed, err := seal([]byte(token))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), sealed, 0o600); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Config{Server: server, Folder: folder, Remote: remote, Interval: cmpOr(old.Interval, 60)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Eingerichtet: %s <-> %s%s\nStarten mit: data-den-sync run, automatisch bei der Anmeldung: data-den-sync autostart on\n", folder, server, remote)
	return nil
}

func cmpOr[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// openLog writes the log to sync.log in the settings folder and, unless
// hidden, to the window. A log over 5 MB is moved to sync.log.old.
func openLog(hidden bool) (*slog.Logger, func(), error) {
	dir, err := configDir()
	if err != nil {
		return nil, nil, err
	}
	name := filepath.Join(dir, "sync.log")
	if info, err := os.Stat(name); err == nil && info.Size() > 5<<20 {
		_ = os.Rename(name, name+".old")
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the user's own log
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = file
	if !hidden {
		w = io.MultiWriter(os.Stderr, file)
	}
	return slog.New(slog.NewTextHandler(w, nil)), func() { _ = file.Close() }, nil
}

func run(hidden, once bool) error {
	cfg, token, err := loadConfig()
	if err != nil {
		return err
	}
	if _, err := checkFolder(cfg.Folder); err != nil {
		return err
	}
	logger, closeLog, err := openLog(hidden)
	if err != nil {
		return err
	}
	defer closeLog()
	if hidden {
		hideWindow()
	}
	host, _ := os.Hostname()
	syncer := &desktop.Syncer{
		Client:    &desktop.Client{Server: cfg.Server, Token: token, HTTP: &http.Client{Timeout: 10 * time.Minute}},
		Local:     cfg.Folder,
		Remote:    cfg.Remote,
		Host:      host,
		FoldCase:  runtime.GOOS == "windows" || runtime.GOOS == "darwin",
		Log:       logger,
		LocalName: localName,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("sync started", "folder", cfg.Folder, "server", cfg.Server, "remote", cfg.Remote, "version", version.Version)
	for {
		changes, err := syncer.Round(ctx)
		if once {
			logger.Info("round done", "changes", changes)
			return err
		}
		switch {
		case errors.Is(err, desktop.ErrLogin):
			logger.Error("the app password was refused; create a new one and run data-den-sync setup", "err", err)
		case err != nil && ctx.Err() == nil:
			logger.Error("round failed", "err", err)
		case changes > 0:
			logger.Info("round done", "changes", changes)
			// A round with changes is followed by another at once: a
			// conflict copy, for example, goes up in the next one.
			continue
		}
		select {
		case <-ctx.Done():
			logger.Info("sync stopped")
			return nil
		case <-time.After(time.Duration(cfg.Interval) * time.Second):
		}
	}
}
