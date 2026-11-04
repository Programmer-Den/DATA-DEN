package files

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const discoveryXML = `<?xml version="1.0" encoding="utf-8"?>
<wopi-discovery><net-zone name="external-http">
<app name="writer"><action name="edit" ext="docx" urlsrc="http://127.0.0.1:9980/browser/abc123/cool.html?"/>
<action name="edit" ext="odt" urlsrc="http://127.0.0.1:9980/browser/abc123/cool.html?"/>
<action name="view" ext="pdf" urlsrc="http://127.0.0.1:9980/browser/abc123/cool.html?"/></app>
<app name="calc"><action name="edit" ext="xlsx" urlsrc="http://127.0.0.1:9980/browser/abc123/cool.html?"/>
<action name="edit" ext="csv" urlsrc="http://127.0.0.1:9980/browser/abc123/cool.html?"/></app>
</net-zone></wopi-discovery>`

// officeSetup runs a fake Collabora and returns the API (as the account in
// X-User), the WOPI listener and the owner's data directory.
func officeSetup(t *testing.T) (api, wopi http.Handler, office *Office, data string, collabora *httptest.Server) {
	t.Helper()
	s, _, data := spacesSetup(t)
	collabora = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hosting/discovery":
			_, _ = io.WriteString(w, discoveryXML)
		default:
			w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors http://127.0.0.1:9981")
			w.Header().Set("X-Seen", r.Host+" "+r.Header.Get("X-Forwarded-Proto")+" cookie="+r.Header.Get("Cookie"))
			w.Header().Set("X-Path", r.URL.RequestURI())
			_, _ = io.WriteString(w, "collabora")
		}
	}))
	t.Cleanup(collabora.Close)
	u, _ := url.Parse(collabora.URL)
	office = s.Office(OfficeConfig{URL: u, WOPI: "http://127.0.0.1:9981"})
	mux := http.NewServeMux()
	office.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
		}
	})
	if err := os.WriteFile(filepath.Join(data, "brief.docx"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	return mux, office.WOPI(), office, data, collabora
}

type opened struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	TTL   int64  `json:"ttl"`
}

func openOffice(t *testing.T, api http.Handler, user, p string) (opened, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/office", strings.NewReader(`{"path":"`+p+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", user)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	var o opened
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return o, rec.Code
}

// wopiID is the file id within the WOPISrc of an editor's address.
func wopiID(t *testing.T, editor string) string {
	t.Helper()
	u, err := url.Parse(editor)
	if err != nil {
		t.Fatal(err)
	}
	src := u.Query().Get("WOPISrc")
	id, ok := strings.CutPrefix(src, "http://127.0.0.1:9981/wopi/files/")
	if !ok {
		t.Fatalf("WOPISrc %q", src)
	}
	return id
}

func wopiDo(h http.Handler, method, target string, body string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOfficeFormats(t *testing.T) {
	api, _, _, _, _ := officeSetup(t)
	rec := wopiDo(api, http.MethodGet, "/api/office", "", as("chef"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	// Only office documents: CSV has the text editor, PDF is only viewed.
	if got := strings.TrimSpace(rec.Body.String()); got != `{"extensions":[".docx",".odt",".xlsx"]}` {
		t.Errorf("body %s", got)
	}
}

func TestOfficeEdit(t *testing.T) {
	api, wopi, _, data, _ := officeSetup(t)
	o, code := openOffice(t, api, "chef", "/brief.docx")
	if code != http.StatusOK {
		t.Fatalf("open: status %d", code)
	}
	if !strings.HasPrefix(o.URL, "/browser/abc123/cool.html?WOPISrc=") || !strings.HasSuffix(o.URL, "&lang=de") {
		t.Errorf("url %q", o.URL)
	}
	id := wopiID(t, o.URL)
	file := "/wopi/files/" + id + "?access_token=" + o.Token
	contents := "/wopi/files/" + id + "/contents?access_token=" + o.Token

	rec := wopiDo(wopi, http.MethodGet, file, "", nil)
	var info map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if rec.Code != http.StatusOK || info["BaseFileName"] != "brief.docx" || info["UserCanWrite"] != true || info["Size"] != float64(3) {
		t.Fatalf("CheckFileInfo: status %d, %v", rec.Code, info)
	}
	if rec := wopiDo(wopi, http.MethodGet, contents, "", nil); rec.Body.String() != "alt" {
		t.Errorf("GetFile: status %d, body %q", rec.Code, rec.Body)
	}

	// The first save keeps the old content as a version, later ones do
	// not.
	put := http.Header{"X-Wopi-Override": {"PUT"}, "X-Cool-Wopi-Timestamp": {info["LastModifiedTime"].(string)}}
	rec = wopiDo(wopi, http.MethodPost, contents, "neu", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PutFile: status %d, body %s", rec.Code, rec.Body)
	}
	var saved struct{ LastModifiedTime string }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	put.Set("X-Cool-Wopi-Timestamp", saved.LastModifiedTime)
	if rec := wopiDo(wopi, http.MethodPost, contents, "neuer", put); rec.Code != http.StatusOK {
		t.Fatalf("second PutFile: status %d, body %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(filepath.Clean(filepath.Join(data, "brief.docx"))); string(got) != "neuer" {
		t.Errorf("file %q", got)
	}
	versions, _ := os.ReadDir(filepath.Join(data, versionFolder("brief.docx")))
	if len(versions) != 2 { // the content and its info
		t.Errorf("%d files in the versions, want 2", len(versions))
	}

	// A change since Collabora opened the file is not overwritten.
	put.Set("X-Cool-Wopi-Timestamp", info["LastModifiedTime"].(string))
	rec = wopiDo(wopi, http.MethodPost, contents, "alt", put)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "1010") {
		t.Errorf("PutFile from an old state: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestOfficeTokens(t *testing.T) {
	api, wopi, _, _, _ := officeSetup(t)
	o, _ := openOffice(t, api, "chef", "/brief.docx")
	id := wopiID(t, o.URL)
	for _, target := range []string{
		"/wopi/files/" + id,
		"/wopi/files/" + id + "?access_token=falsch",
		// A token only opens its own file.
		"/wopi/files/" + officeID("chef", "anders.docx") + "?access_token=" + o.Token,
	} {
		if rec := wopiDo(wopi, http.MethodGet, target, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", target, rec.Code)
		}
	}
	// Another account opening the same name gets its own file.
	if _, code := openOffice(t, api, "anna", "/brief.docx"); code != http.StatusNotFound {
		t.Errorf("anna opens chef's file: status %d", code)
	}
	for p, want := range map[string]int{
		"/fehlt.docx":            http.StatusNotFound,
		"/notizen.txt":           http.StatusUnsupportedMediaType,
		"/../brief.docx":         http.StatusBadRequest,
		"/.data-den-trash/a.odt": http.StatusBadRequest,
	} {
		if _, code := openOffice(t, api, "chef", p); code != want {
			t.Errorf("open %s: status %d, want %d", p, code, want)
		}
	}
	// Not in a shared folder: the path would be taken in the own space.
	req := httptest.NewRequest(http.MethodPost, "/api/office?grant=abc", strings.NewReader(`{"path":"/brief.docx"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "chef")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("open in a shared folder: status %d", rec.Code)
	}
	// Without the override header PutFile is something else WOPI knows.
	if rec := wopiDo(wopi, http.MethodPost, "/wopi/files/"+id+"/contents?access_token="+o.Token, "x", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("POST without X-WOPI-Override: status %d", rec.Code)
	}
}

func TestOfficeProxy(t *testing.T) {
	_, _, office, _, _ := officeSetup(t)
	proxy := office.Proxy()
	req := httptest.NewRequest(http.MethodGet, "https://data.example.de/browser/abc123/cool.html?WOPISrc=x", nil)
	req.Header.Set("Cookie", "data-den-session=geheim")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Path") != "/browser/abc123/cool.html?WOPISrc=x" {
		t.Fatalf("status %d, path %q", rec.Code, rec.Header().Get("X-Path"))
	}
	if got := rec.Header().Get("X-Seen"); got != "data.example.de https cookie=" {
		t.Errorf("Collabora saw %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'; frame-ancestors 'self'" {
		t.Errorf("Content-Security-Policy %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options %q", got)
	}
	for _, admin := range []string{"/browser/dist/admin/admin.html", "/cool/adminws", "/cool/getMetrics"} {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, admin, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", admin, rec.Code)
		}
	}
}

func TestOfficeUnreachable(t *testing.T) {
	api, _, _, _, collabora := officeSetup(t)
	collabora.Close()
	if rec := wopiDo(api, http.MethodGet, "/api/office", "", as("chef")); rec.Code != http.StatusBadGateway {
		t.Errorf("status %d", rec.Code)
	}
}

func TestFrameSelf(t *testing.T) {
	for in, want := range map[string]string{
		"default-src 'self'; frame-ancestors a b; img-src *": "default-src 'self'; frame-ancestors 'self'; img-src *",
		"default-src 'self';":                                "default-src 'self'; frame-ancestors 'self'",
	} {
		if got := frameSelf(in); got != want {
			t.Errorf("frameSelf(%q) = %q, want %q", in, got, want)
		}
	}
}
