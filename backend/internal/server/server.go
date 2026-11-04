// Package server serves the website and the HTTP API.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/audit"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// Config is what the server needs to run.
type Config struct {
	// Files is the built website, with index.html at its root.
	Files fs.FS
	// Version is reported by /api/health.
	Version string
	// Auth adds the login API; nil leaves it out.
	Auth *auth.Auth
	// Data is the data directory, each account's space served behind the
	// login. Nil with Auth set means the directory is missing: its API
	// answers 503.
	Data *files.Spaces
	// Status adds the status page's API behind the login; nil leaves it out.
	Status *status.Status
	// Audit adds the audit log's API behind the login; nil leaves it out.
	Audit *audit.Log
}

// New returns the handler for the website and the API.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": cfg.Version})
	})
	if cfg.Auth != nil {
		cfg.Auth.Register(mux)
		if cfg.Status != nil {
			cfg.Status.Register(mux, cfg.Auth.Require)
		}
		if cfg.Audit != nil {
			cfg.Audit.Register(mux, cfg.Auth.Require)
		}
		if cfg.Data != nil {
			cfg.Data.Register(mux, cfg.Auth.Require)
			cfg.Data.RegisterShares(mux, cfg.Auth.Require, cfg.Auth)
			cfg.Data.RegisterDAV(mux, cfg.Auth.RequireToken)
		} else {
			missing := func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Datenordner nicht verfügbar"})
			}
			for _, route := range slices.Concat(files.Routes, files.ShareRoutes) {
				mux.HandleFunc(route, cfg.Auth.Require(missing))
			}
			mux.HandleFunc("/api/s/", missing)
			mux.HandleFunc(files.DavPrefix+"/", cfg.Auth.RequireToken(missing))
		}
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("/", site(cfg.Files))
	return securityHeaders(mux)
}

// site serves the built website. Paths without a file get index.html, so the
// Angular router handles them.
func site(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, "index.html"); err != nil {
			http.Error(w, "The website is not built: run npm run build in frontend/.", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(files, name); errors.Is(err, fs.ErrNotExist) {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
