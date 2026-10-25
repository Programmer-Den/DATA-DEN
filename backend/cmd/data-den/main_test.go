package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveHealth(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	})
}

// listenOn points LISTEN_ADDR at the port of srv, as ":port".
func listenOn(t *testing.T, srv *httptest.Server) {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LISTEN_ADDR", ":"+port)
}

func TestHealthcheck(t *testing.T) {
	t.Setenv("DATA_DEN_TLS_CERT", "")

	ok := httptest.NewServer(serveHealth(http.StatusOK))
	defer ok.Close()
	listenOn(t, ok)
	if err := healthcheck(); err != nil {
		t.Errorf("healthy server: %v", err)
	}

	sick := httptest.NewServer(serveHealth(http.StatusServiceUnavailable))
	defer sick.Close()
	listenOn(t, sick)
	if err := healthcheck(); err == nil {
		t.Error("server answering 503: no error")
	}
}

func TestHealthcheckHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(serveHealth(http.StatusOK))
	defer srv.Close()
	listenOn(t, srv)

	t.Setenv("DATA_DEN_TLS_CERT", "")
	if err := healthcheck(); err == nil {
		t.Error("plain HTTP to an HTTPS server: no error")
	}
	t.Setenv("DATA_DEN_TLS_CERT", "/tls/cert.pem")
	if err := healthcheck(); err != nil {
		t.Errorf("self-signed certificate: %v", err)
	}
}

func TestHealthcheckNothingListening(t *testing.T) {
	srv := httptest.NewServer(serveHealth(http.StatusOK))
	listenOn(t, srv)
	srv.Close()
	t.Setenv("DATA_DEN_TLS_CERT", "")
	if err := healthcheck(); err == nil {
		t.Error("closed port: no error")
	}
}
