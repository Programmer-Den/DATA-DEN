package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/auth"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/files"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/notify"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/setup"
	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
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

func TestOCRConfig(t *testing.T) {
	t.Setenv("DATA_DEN_OCR_URL", "")
	if r, err := ocrConfig(); err != nil || r != nil {
		t.Errorf("unset: %v, %v", r, err)
	}
	t.Setenv("DATA_DEN_OCR_URL", "http://127.0.0.1:9998")
	if r, err := ocrConfig(); err != nil || r == nil {
		t.Errorf("set: %v, %v", r, err)
	}
	for _, bad := range []string{"127.0.0.1:9998", "ftp://x", "http://"} {
		t.Setenv("DATA_DEN_OCR_URL", bad)
		if _, err := ocrConfig(); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestOwnerAccount(t *testing.T) {
	dir := t.TempDir()
	stat := status.New(status.Config{DataDir: dir})

	// From the settings, when the hash is set.
	t.Setenv("DATA_DEN_USERNAME", "chriszly")
	t.Setenv("DATA_DEN_PASSWORD_HASH", "$2a$04$settings")
	owner, file, err := ownerAccount(t.Context(), dir, stat)
	if err != nil || owner == nil || owner.Name != "chriszly" || owner.PasswordHash != "$2a$04$settings" || file != "" {
		t.Errorf("settings: %+v %q %v", owner, file, err)
	}

	// Else from the setup page's file.
	t.Setenv("DATA_DEN_PASSWORD_HASH", "")
	hash := "$2a$04$" + strings.Repeat("f", 53)
	if err := auth.SaveOwner(ownerFile(dir), "anna", hash); err != nil {
		t.Fatal(err)
	}
	owner, file, err = ownerAccount(t.Context(), dir, stat)
	if err != nil || owner == nil || owner.Name != "anna" || owner.PasswordHash != hash || file != ownerFile(dir) {
		t.Errorf("file: %+v %q %v", owner, file, err)
	}
}

func TestOwnerAccountSetup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DEN_PASSWORD_HASH", "")
	t.Setenv("DATA_DEN_TLS_CERT", "")
	t.Setenv("DATA_DEN_TLS_KEY", "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("LISTEN_ADDR", addr)

	type result struct {
		owner *setup.Owner
		err   error
	}
	done := make(chan result, 1)
	go func() {
		owner, _, err := ownerAccount(t.Context(), dir, status.New(status.Config{DataDir: dir}))
		done <- result{owner, err}
	}()
	var health map[string]any
	for i := 0; ; i++ {
		resp, err := http.Get("http://" + addr + "/api/health")
		if err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&health)
			_ = resp.Body.Close()
			break
		}
		if i == 100 {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health["setup"] != true {
		t.Errorf("health %v, want setup", health)
	}
	code, err := os.ReadFile(filepath.Join(dir, ".data-den-accounts", "setup-code")) //nolint:gosec // the test's own directory
	if err != nil {
		t.Fatal(err)
	}
	body := `{"code":"` + strings.TrimSpace(string(code)) + `","username":"chriszly","password":"ein-langes-passwort"}`
	resp, err := http.Post("http://"+addr+"/api/setup", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: status %d", resp.StatusCode)
	}
	r := <-done
	if r.err != nil || r.owner == nil || r.owner.Name != "chriszly" {
		t.Errorf("owner %+v, err %v", r.owner, r.err)
	}
}
