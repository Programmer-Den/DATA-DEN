package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/notify"
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

func TestSetTrashDays(t *testing.T) {
	data, err := files.OpenSpaces(t.TempDir(), files.SpacesConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = data.Close() }()

	t.Setenv("DATA_DEN_TRASH_DAYS", "")
	if err := setTrashDays(data); err != nil || data.TrashDays() != files.DefaultTrashDays {
		t.Errorf("unset: %v, %d days", err, data.TrashDays())
	}
	for _, bad := range []string{"abc", "0", "7d", "99999"} {
		t.Setenv("DATA_DEN_TRASH_DAYS", bad)
		if setTrashDays(data) == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	t.Setenv("DATA_DEN_TRASH_DAYS", "14")
	if err := setTrashDays(data); err != nil || data.TrashDays() != 14 {
		t.Errorf("14: %v, %d days", err, data.TrashDays())
	}
}

func TestDamagedMessage(t *testing.T) {
	m := damagedMessage("anna", []string{"/a.jpg"})
	if m.Kind != notify.KindCorrupt || !strings.Contains(m.Body, "eine beschädigte Datei") || !strings.Contains(m.Body, "/a.jpg") {
		t.Errorf("one file: %+v", m)
	}
	var paths []string
	for i := range 25 {
		paths = append(paths, fmt.Sprintf("/%d.jpg", i))
	}
	m = damagedMessage("anna", paths)
	if !strings.Contains(m.Body, "25 beschädigte Dateien") || !strings.Contains(m.Body, "/19.jpg") || strings.Contains(m.Body, "/20.jpg") || !strings.Contains(m.Body, "5 weitere") {
		t.Errorf("many files: %s", m.Body)
	}
}

func TestNewGeocoder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DEN_GEOCODER", "off")
	if namer, g, err := newGeocoder(dir); namer != nil || g != nil || err != nil {
		t.Errorf("off: %v, %v, %v", namer, g, err)
	}
	for _, value := range []string{"", "https://nominatim.example.org/"} {
		t.Setenv("DATA_DEN_GEOCODER", value)
		if namer, g, err := newGeocoder(dir); namer == nil || g == nil || err != nil {
			t.Errorf("%q: %v, %v, %v", value, namer, g, err)
		}
	}
	for _, value := range []string{"nominatim", "ftp://x", "https://"} {
		t.Setenv("DATA_DEN_GEOCODER", value)
		if _, _, err := newGeocoder(dir); err == nil {
			t.Errorf("%q: no error", value)
		}
	}
}

func TestNewTiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DEN_TILES", "off")
	if got, err := newTiles(dir); got != nil || err != nil {
		t.Errorf("off: %v, %v", got, err)
	}
	for _, value := range []string{"", "https://tiles.example.org/"} {
		t.Setenv("DATA_DEN_TILES", value)
		if got, err := newTiles(dir); got == nil || err != nil {
			t.Errorf("%q: %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"tiles", "ftp://x", "https://"} {
		t.Setenv("DATA_DEN_TILES", value)
		if _, err := newTiles(dir); err == nil {
			t.Errorf("%q: no error", value)
		}
	}
}

func TestNewVision(t *testing.T) {
	t.Setenv("DATA_DEN_VISION", "")
	if got, err := newVision(); got != nil || err != nil {
		t.Errorf("off: %v, %v", got, err)
	}
	t.Setenv("DATA_DEN_VISION", "http://127.0.0.1:3003/")
	if got, err := newVision(); got == nil || err != nil {
		t.Errorf("on: %v, %v", got, err)
	}
	for _, value := range []string{"127.0.0.1:3003", "ftp://x", "http://"} {
		t.Setenv("DATA_DEN_VISION", value)
		if _, err := newVision(); err == nil {
			t.Errorf("%q: no error", value)
		}
	}
}

func TestOfficeConfig(t *testing.T) {
	t.Setenv("DATA_DEN_OFFICE_URL", "")
	t.Setenv("DATA_DEN_WOPI_ADDR", "")
	if _, addr, err := officeConfig(); err != nil || addr != "" {
		t.Errorf("unset: %q, %v", addr, err)
	}
	t.Setenv("DATA_DEN_OFFICE_URL", "http://127.0.0.1:9980")
	cfg, addr, err := officeConfig()
	if err != nil || addr != defaultWOPIAddr || cfg.WOPI != "http://"+defaultWOPIAddr || cfg.URL.Host != "127.0.0.1:9980" {
		t.Errorf("default: %+v, %q, %v", cfg, addr, err)
	}
	for _, bad := range []string{"127.0.0.1:9980", "ftp://x", "http://"} {
		t.Setenv("DATA_DEN_OFFICE_URL", bad)
		if _, _, err := officeConfig(); err == nil {
			t.Errorf("DATA_DEN_OFFICE_URL %q: no error", bad)
		}
	}
	t.Setenv("DATA_DEN_OFFICE_URL", "http://127.0.0.1:9980")
	for _, bad := range []string{":9981", "0.0.0.0:9981", "9981"} {
		t.Setenv("DATA_DEN_WOPI_ADDR", bad)
		if _, _, err := officeConfig(); err == nil {
			t.Errorf("DATA_DEN_WOPI_ADDR %q: no error", bad)
		}
	}
}
