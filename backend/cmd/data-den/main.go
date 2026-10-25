// Command data-den serves the DATA-DEN website and its API.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/server"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/version"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(); err != nil {
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

func run() error {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
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
	dataDir := os.Getenv("DATA_DEN_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	data, err := files.Open(dataDir)
	if err != nil {
		slog.Warn("the data directory cannot be opened, so no files are served; set DATA_DEN_DATA_DIR", "dir", dataDir, "err", err)
	} else {
		defer func() { _ = data.Close() }()
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(server.Config{Files: web.Files(), Version: version.Version, Auth: login, Data: data}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("DATA-DEN listening", "addr", addr, "version", version.Version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
