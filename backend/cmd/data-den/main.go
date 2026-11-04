// Command data-den serves the DATA-DEN website and its API.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/server"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/version"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/web"
)

func main() {
	if len(os.Args) > 1 {
		commands := map[string]func() error{"hash-password": hashPassword, "healthcheck": healthcheck}
		command, ok := commands[os.Args[1]]
		if !ok {
			fmt.Fprintln(os.Stderr, "Usage: data-den [hash-password | healthcheck]")
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

// setTrashDays applies DATA_DEN_TRASH_DAYS, if set, to data.
func setTrashDays(data *files.Files) error {
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

func run() error {
	addr := listenAddr()
	certFile, keyFile := tlsFiles()
	if (certFile == "") != (keyFile == "") {
		return errors.New("set both DATA_DEN_TLS_CERT and DATA_DEN_TLS_KEY, or neither")
	}
	login, err := auth.New(auth.Config{
		Username:     os.Getenv("DATA_DEN_USERNAME"),
		PasswordHash: os.Getenv("DATA_DEN_PASSWORD_HASH"),
		TrustedProxy: os.Getenv("DATA_DEN_TRUSTED_PROXY"),
	})
	if err != nil {
		return err
	}
	if os.Getenv("DATA_DEN_PASSWORD_HASH") == "" {
		slog.Warn("no DATA_DEN_PASSWORD_HASH set, so nobody can log in; create one with: data-den hash-password")
	}
	warnPercent, err := status.WarnPercent(os.Getenv("DATA_DEN_DISK_WARN_PERCENT"))
	if err != nil {
		return err
	}
	dataDir := os.Getenv("DATA_DEN_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	data, err := files.Open(dataDir)
	if err != nil {
		slog.Warn("the data directory cannot be opened, so no files are served; set DATA_DEN_DATA_DIR", "dir", dataDir, "err", err)
	} else {
		defer func() { _ = data.Close() }()
		if err := setTrashDays(data); err != nil {
			return err
		}
	}
	stat := status.New(status.Config{
		DataDir:     dataDir,
		WarnPercent: warnPercent,
		MDStat:      "/proc/mdstat",
		Uptime:      "/proc/uptime",
		Version:     version.Version,
		Started:     time.Now(),
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(server.Config{Files: web.Files(), Version: version.Version, Auth: login, Data: data, Status: stat}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if data != nil {
		// The search index, built in the background so the server answers
		// at once.
		go data.Index(ctx.Done())
		go data.PurgeTrashEvery(ctx, time.Hour)
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
