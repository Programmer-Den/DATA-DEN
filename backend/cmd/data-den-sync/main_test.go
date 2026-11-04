package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckServer(t *testing.T) {
	for raw, want := range map[string]string{
		"https://nas.example.org/": "https://nas.example.org",
		"http://localhost:8080":    "http://localhost:8080",
		"http://nas.example.org":   "",
		"nas.example.org":          "",
		"https://user:pw@nas.org":  "",
		"ftp://nas.example.org":    "",
	} {
		got, err := checkServer(raw)
		if want == "" && err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		}
		if want != "" && got != want {
			t.Errorf("%q: %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestCheckToken(t *testing.T) {
	if err := checkToken("dd_" + strings.Repeat("a", 43)); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "geheim", "dd_kurz", "dd_" + strings.Repeat("a", 30) + " x"} {
		if checkToken(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckRemote(t *testing.T) {
	for raw, want := range map[string]string{"": "/", "/": "/", "Dokumente/2026/": "/Dokumente/2026", `Fotos\Urlaub`: "/Fotos/Urlaub", "../x": "", "/.data-den-trash": ""} {
		got, err := checkRemote(raw)
		if want == "" && err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		}
		if want != "" && got != want {
			t.Errorf("%q: %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestSetupSavesConfig(t *testing.T) {
	token := "dd_" + strings.Repeat("x", 43)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"path":"/","entries":[]}`)
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("DATA_DEN_SYNC_TOKEN", "")
	folder := filepath.Join(home, "Sync")
	local := srv.URL
	answers := strings.Join([]string{local, token, folder, "Dokumente"}, "\n") + "\n"
	if err := setup(bufio.NewReader(strings.NewReader(answers)), io.Discard); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg, got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != token || cfg.Server != local || cfg.Folder != folder || cfg.Remote != "/Dokumente" || cfg.Interval != 60 {
		t.Errorf("config %+v, token %q", cfg, got)
	}
	info, err := os.Stat(filepath.Join(home, "config", "DATA-DEN-Sync", "token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v %v", info, err)
	}

	answers = strings.Join([]string{local, "dd_" + strings.Repeat("y", 43), folder, ""}, "\n") + "\n"
	if err := setup(bufio.NewReader(strings.NewReader(answers)), io.Discard); err == nil {
		t.Error("a refused app password was saved")
	}
}
