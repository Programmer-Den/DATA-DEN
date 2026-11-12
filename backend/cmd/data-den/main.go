// Command data-den serves the DATA-DEN website and its API.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/audit"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/notify"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/places"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/server"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/tiles"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/version"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/vision"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/web"
)

func main() {
	if len(os.Args) > 1 {
		commands := map[string]func() error{"hash-password": hashPassword, "healthcheck": healthcheck, "reset-2fa": resetTwoFactor}
		command, ok := commands[os.Args[1]]
		if !ok {
			fmt.Fprintln(os.Stderr, "Usage: data-den [hash-password | healthcheck | reset-2fa <username>]")
			os.Exit(2)
		}
		if err := command(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// hashPassword reads a password from the first line of stdin and prints the
// hash for DATA_DEN_PASSWORD_HASH.
func hashPassword() error {
	fmt.Fprintln(os.Stderr, "Password (one line):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	hash, err := auth.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

// resetTwoFactor turns the two-factor login of an account off, for an
// owner who lost the phone and the recovery codes. Stop the server first.
func resetTwoFactor() error {
	if len(os.Args) != 3 {
		return errors.New("usage: data-den reset-2fa <username>")
	}
	if err := auth.ResetTwoFactor(accountsFile(dataDir()), os.Args[2]); err != nil {
		return err
	}
	fmt.Println("Two-factor login turned off for", os.Args[2])
	return nil
}

func dataDir() string {
	if dir := os.Getenv("DATA_DEN_DATA_DIR"); dir != "" {
		return dir
	}
	return "/data"
}

// accountsFile keeps the accounts in the data directory, next to the files,
// in a folder the API never serves.
func accountsFile(dataDir string) string {
	return filepath.Join(dataDir, ".data-den-accounts", "accounts.json")
}

// healthcheck asks the running server for its health, for Docker's
// HEALTHCHECK: the image has no shell and no curl.
func healthcheck() error {
	_, port, err := net.SplitHostPort(listenAddr())
	if err != nil {
		return err
	}
	scheme := "http"
	client := &http.Client{Timeout: 5 * time.Second}
	if certFile, _ := tlsFiles(); certFile != "" {
		scheme = "https"
		// The certificate is for the server's name, not for 127.0.0.1, and
		// often self-signed; this only checks that it answers.
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // local health check only
	}
	resp, err := client.Get(scheme + "://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: status %d", resp.StatusCode)
	}
	return nil
}

func listenAddr() string {
	if addr := os.Getenv("LISTEN_ADDR"); addr != "" {
		return addr
	}
	return ":8080"
}

// tlsFiles are the certificate and key to serve HTTPS with; both empty
// means plain HTTP.
func tlsFiles() (certFile, keyFile string) {
	return os.Getenv("DATA_DEN_TLS_CERT"), os.Getenv("DATA_DEN_TLS_KEY")
}

// defaultWOPIAddr is where the WOPI listener for Collabora listens, unless
// DATA_DEN_WOPI_ADDR says otherwise: only on the VM itself.
const defaultWOPIAddr = "127.0.0.1:9981"

// officeConfig reads DATA_DEN_OFFICE_URL, where Collabora runs, and
// DATA_DEN_WOPI_ADDR. Without DATA_DEN_OFFICE_URL office editing is off and
// addr is "".
func officeConfig() (cfg files.OfficeConfig, addr string, err error) {
	value := os.Getenv("DATA_DEN_OFFICE_URL")
	if value == "" {
		return cfg, "", nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, "", fmt.Errorf("DATA_DEN_OFFICE_URL must be Collabora's address, such as http://127.0.0.1:9980, not %q", value)
	}
	addr = os.Getenv("DATA_DEN_WOPI_ADDR")
	if addr == "" {
		addr = defaultWOPIAddr
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		return cfg, "", fmt.Errorf("DATA_DEN_WOPI_ADDR must be an address Collabora can reach, such as %s, not %q", defaultWOPIAddr, addr)
	}
	return files.OfficeConfig{
		URL:    u,
		WOPI:   "http://" + addr,
		Client: &http.Client{Timeout: 10 * time.Second},
	}, addr, nil
}

// clientsDir holds the sync client's programs: /clients in the image, or
// DATA_DEN_CLIENTS_DIR. Nil when there is none.
func clientsDir() fs.FS {
	dir := os.Getenv("DATA_DEN_CLIENTS_DIR")
	if dir == "" {
		dir = "/clients"
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() { //nolint:gosec // the server's own setting
		return nil
	}
	return os.DirFS(dir)
}

// setTrashDays applies DATA_DEN_TRASH_DAYS, if set, to data.
func setTrashDays(data *files.Spaces) error {
	value := os.Getenv("DATA_DEN_TRASH_DAYS")
	if value == "" {
		return nil
	}
	days, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("DATA_DEN_TRASH_DAYS must be a number of days, not %q", value)
	}
	if err := data.SetTrashDays(days); err != nil {
		return fmt.Errorf("DATA_DEN_TRASH_DAYS: %w", err)
	}
	return nil
}

// damagedMessage tells the admins about files a check found damaged in the
// space of account.
// sharedMessage tells an account about a folder shared with it.
func sharedMessage(g files.Grant) notify.Message {
	what := "zum Lesen"
	if g.Write {
		what = "zum Lesen und Schreiben"
	}
	if g.Team {
		return notify.Message{
			Kind:  notify.KindShare,
			Title: "Team-Ordner „" + g.Name + "“",
			Body:  "Der Team-Ordner „" + g.Name + "“ ist " + what + " für dich freigegeben. Du findest ihn unter „Geteilt mit mir“.",
		}
	}
	return notify.Message{
		Kind:  notify.KindShare,
		Title: g.Owner + " teilt „" + g.Name + "“ mit dir",
		Body:  "Der Ordner „" + g.Name + "“ ist " + what + " für dich freigegeben. Du findest ihn unter „Geteilt mit mir“.",
	}
}

func damagedMessage(account string, paths []string) notify.Message {
	const show = 20
	body := fmt.Sprintf("Die Prüfung hat im Bereich von %s %d beschädigte Dateien gefunden:\n\n", account, len(paths))
	if len(paths) == 1 {
		body = fmt.Sprintf("Die Prüfung hat im Bereich von %s eine beschädigte Datei gefunden:\n\n", account)
	}
	for _, p := range paths[:min(len(paths), show)] {
		body += p + "\n"
	}
	if len(paths) > show {
		body += fmt.Sprintf("… und %d weitere\n", len(paths)-show)
	}
	body += "\nDetails und was zu tun ist stehen in DATA-DEN unter Prüfung."
	return notify.Message{Kind: notify.KindCorrupt, Title: "Beschädigte Dateien gefunden", Body: body}
}

// userAgent names DATA-DEN to OpenStreetMap, as its usage policies ask.
var userAgent = "DATA-DEN/" + version.Version + " (self-hosted NAS, github.com/Programmer-Den/DATA-DEN)"

// newGeocoder names the towns of the photos with Nominatim at
// DATA_DEN_GEOCODER (OpenStreetMap's by default), keeping the answers in the
// data directory; "off" names none. It returns a nil Namer when off.
func newGeocoder(dataDir string) (files.Namer, *places.Geocoder, error) {
	base := os.Getenv("DATA_DEN_GEOCODER")
	switch base {
	case "off":
		return nil, nil, nil
	case "":
		base = places.DefaultURL
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, fmt.Errorf("DATA_DEN_GEOCODER must be a Nominatim address such as %s, or off, not %q", places.DefaultURL, base)
	}
	g := places.New(places.Config{
		URL:       strings.TrimRight(base, "/"),
		UserAgent: userAgent,
		File:      filepath.Join(dataDir, ".data-den-places", "names.json"),
	})
	return g, g, nil
}

// newTiles serves the map of the photos with the tiles of DATA_DEN_TILES
// (OpenStreetMap's by default), keeping them in the data directory; "off"
// draws no map. It returns nil when off.
func newTiles(dataDir string) (*tiles.Tiles, error) {
	base := os.Getenv("DATA_DEN_TILES")
	switch base {
	case "off":
		return nil, nil
	case "":
		base = tiles.DefaultURL
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("DATA_DEN_TILES must be a tile server such as %s, or off, not %q", tiles.DefaultURL, base)
	}
	return tiles.New(tiles.Config{
		URL:       strings.TrimRight(base, "/"),
		UserAgent: userAgent,
		Dir:       filepath.Join(dataDir, ".data-den-tiles"),
	})
}

// newVision finds faces and things in the photos with the machine learning
// service at DATA_DEN_VISION, such as http://127.0.0.1:3003; without it none
// are found and it returns nil.
func newVision() (files.Recognizer, error) {
	base := os.Getenv("DATA_DEN_VISION")
	if base == "" {
		return nil, nil
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("DATA_DEN_VISION must be the machine learning service's address, such as http://127.0.0.1:3003, not %q", base)
	}
	return vision.New(vision.Config{URL: strings.TrimRight(base, "/")}), nil
}

func run() error {
	addr := listenAddr()
	certFile, keyFile := tlsFiles()
	if (certFile == "") != (keyFile == "") {
		return errors.New("set both DATA_DEN_TLS_CERT and DATA_DEN_TLS_KEY, or neither")
	}
	dataDir := dataDir()
	var login *auth.Auth
	// The audit log only writes once the data directory is known to be there.
	auditCfg := audit.Config{
		IP:    func(r *http.Request) string { return login.ClientIP(r) },
		User:  auth.Username,
		Admin: auth.IsAdmin,
	}
	var auditLog *audit.Log
	var notifier *notify.Notifier
	namer, geocoder, err := newGeocoder(dataDir)
	if err != nil {
		return err
	}
	recognizer, err := newVision()
	if err != nil {
		return err
	}
	data, err := files.OpenSpaces(dataDir, files.SpacesConfig{
		Owner: os.Getenv("DATA_DEN_USERNAME"),
		User:  auth.Username,
		Quota: func(name string) int64 { return login.Quota(name) },
		Audit: func(r *http.Request, user, action, target string) { auditLog.Record(r, user, action, target) },
		Damaged: func(account string, paths []string) {
			notifier.NotifySystem(damagedMessage(account, paths))
		},
		Places:   namer,
		Vision:   recognizer,
		Accounts: func() []string { return login.Names() },
		Shared: func(g files.Grant) {
			notifier.Notify(g.To, sharedMessage(g))
		},
	})
	accounts, notifyDir, eventsDir := "", "", ""
	if err != nil {
		slog.Warn("the data directory cannot be opened, so no files are served; set DATA_DEN_DATA_DIR", "dir", dataDir, "err", err)
	} else {
		defer func() { _ = data.Close() }()
		if err := setTrashDays(data); err != nil {
			return err
		}
		accounts = accountsFile(dataDir)
		auditCfg.File = filepath.Join(dataDir, ".data-den-audit", "audit.log")
		notifyDir = filepath.Join(dataDir, ".data-den-notify")
		eventsDir = filepath.Join(dataDir, ".data-den-events")
	}
	auditLog = audit.New(auditCfg)
	mail, err := notify.MailFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	pushContact, err := notify.PushContact(os.Getenv("DATA_DEN_PUSH_CONTACT"), mail)
	if err != nil {
		return err
	}
	notifier, err = notify.New(notify.Config{
		Dir:         notifyDir,
		Mail:        mail,
		PushContact: pushContact,
		User:        auth.Username,
		System:      func(name string) bool { return login.Admin(name) },
	})
	if err != nil {
		return err
	}
	warnPercent, err := status.WarnPercent(os.Getenv("DATA_DEN_DISK_WARN_PERCENT"))
	if err != nil {
		return err
	}
	stat := status.New(status.Config{
		DataDir:     dataDir,
		WarnPercent: warnPercent,
		MDStat:      "/proc/mdstat",
		Uptime:      "/proc/uptime",
		Version:     version.Version,
		Started:     time.Now(),
	})
	login, err = auth.New(auth.Config{
		Username:     os.Getenv("DATA_DEN_USERNAME"),
		PasswordHash: os.Getenv("DATA_DEN_PASSWORD_HASH"),
		TrustedProxy: os.Getenv("DATA_DEN_TRUSTED_PROXY"),
		AccountsFile: accounts,
		Usage: func(name string) (int64, error) {
			if data == nil {
				return 0, errors.New("no data directory")
			}
			return data.Usage(name)
		},
		Removed: func(name string) error {
			notifier.Forget(name)
			if data == nil {
				return nil
			}
			return data.Retire(name)
		},
		Audit: auditLog.Record,
	})
	if err != nil {
		return err
	}
	if os.Getenv("DATA_DEN_PASSWORD_HASH") == "" {
		slog.Warn("no DATA_DEN_PASSWORD_HASH set, so nobody can log in; create one with: data-den hash-password")
	}
	var mapTiles *tiles.Tiles
	if data != nil {
		if mapTiles, err = newTiles(dataDir); err != nil {
			return err
		}
	}
	officeCfg, wopiAddr, err := officeConfig()
	if err != nil {
		return err
	}
	var office *files.Office
	var wopi *http.Server
	if wopiAddr != "" && data != nil {
		office = data.Office(officeCfg)
		wopi = &http.Server{
			Addr:              wopiAddr,
			Handler:           office.WOPI(),
			ReadHeaderTimeout: 10 * time.Second,
		}
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(server.Config{Files: web.Files(), Version: version.Version, Auth: login, Data: data, Status: stat, Audit: auditLog, Notify: notifier, Office: office, Clients: clientsDir(), Tiles: mapTiles}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Runs before data.Close, which waits for the checks to stop.
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		if wopi != nil {
			_ = wopi.Shutdown(shutdown)
		}
	}()
	if geocoder != nil && data != nil {
		go geocoder.Run(ctx)
	}
	if data != nil {
		// The checks of the RAID, the free space and the VM's events.
		go (&notify.Watcher{Report: stat.Report, EventsDir: eventsDir}).Run(ctx, notifier)
		// The search indexes, built in the background so the server answers
		// at once, and the recycle bins emptied on time.
		data.Run(ctx)
	}

	if wopi != nil {
		// Only Collabora talks to it, to load and save documents.
		listener, err := net.Listen("tcp", wopiAddr)
		if err != nil {
			return fmt.Errorf("the WOPI listener for Collabora (DATA_DEN_WOPI_ADDR): %w", err)
		}
		go func() {
			if err := wopi.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("the WOPI listener stopped", "err", err)
			}
		}()
		slog.Info("office editing with Collabora", "collabora", officeCfg.URL.String(), "wopi", wopiAddr)
	}

	slog.Info("DATA-DEN listening", "addr", addr, "https", certFile != "", "version", version.Version)
	if certFile != "" {
		err = srv.ListenAndServeTLS(certFile, keyFile)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
