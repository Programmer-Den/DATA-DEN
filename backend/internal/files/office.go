package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Office documents are edited with Collabora Online (CODE), a container next
// to DATA-DEN, over WOPI:
//
//	GET  /api/office         -> {"extensions": [".docx", ...]}, what it can edit
//	POST /api/office {"path"} -> {"url", "token", "ttl"}: the page posts the
//	                            token to url in a frame, which shows the editor;
//	                            not in folders another account shares
//
// Collabora then loads and saves the file from the WOPI listener, a second
// port that only Collabora reaches:
//
//	GET  /wopi/files/{id}?access_token=...           CheckFileInfo
//	GET  /wopi/files/{id}/contents?access_token=...  GetFile
//	POST /wopi/files/{id}/contents?access_token=...  PutFile
//
// The browser reaches Collabora through DATA-DEN itself (Proxy, behind the
// login), so the Pi's gateway and the VM's firewall need nothing new.
const (
	// MaxOffice is the largest document Collabora may save.
	MaxOffice = 256 << 20
	// tokenLife is how long an opened document can be saved.
	tokenLife = 10 * time.Hour
	// discoveryLife is how long Collabora's list of formats is kept.
	discoveryLife = time.Hour
)

// officeFormats are the extensions offered for Collabora, if it can edit
// them. Text files have their own editor.
var officeFormats = []string{
	".doc", ".docx", ".odt", ".rtf",
	".xls", ".xlsx", ".ods",
	".ppt", ".pptx", ".odp",
}

// OfficePaths are the paths the browser uses on Collabora, for Proxy.
var OfficePaths = []string{"/browser/", "/cool/", "/hosting/"}

// blockedOffice are Collabora's admin pages, never passed on.
var blockedOffice = regexp.MustCompile(`^/(browser/dist/admin|cool/adminws|cool/getMetrics)`)

// OfficeConfig says where Collabora and the WOPI listener are.
type OfficeConfig struct {
	// URL is where DATA-DEN reaches Collabora, such as
	// http://127.0.0.1:9980.
	URL *url.URL
	// WOPI is where Collabora reaches the WOPI listener, such as
	// http://127.0.0.1:9981.
	WOPI string
	// Client fetches Collabora's discovery; nil is http.DefaultClient.
	Client *http.Client
}

// Office lets Collabora open and save the files of every space.
type Office struct {
	cfg    OfficeConfig
	spaces *Spaces

	mu     sync.Mutex
	tokens map[string]*officeToken

	discovery struct {
		mu sync.Mutex
		at time.Time
		// urls by extension: the path and query of Collabora's editor.
		urls map[string]string
	}
}

// officeToken is one opened document.
type officeToken struct {
	account string
	name    string // within the account's space
	id      string
	expires time.Time
	// origin is the page's, for Collabora's messages to it.
	origin string
	// saved is true after the first save, which kept the old version.
	saved bool
}

// Office returns the Office for the spaces.
func (s *Spaces) Office(cfg OfficeConfig) *Office {
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &Office{cfg: cfg, spaces: s, tokens: map[string]*officeToken{}}
}

// Register adds /api/office to mux, behind require (the login check).
func (o *Office) Register(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/office", require(o.formats))
	mux.HandleFunc("POST /api/office", require(o.open))
}

// WOPI is the handler of the WOPI listener.
func (o *Office) WOPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wopi/files/{id}", o.checkFileInfo)
	mux.HandleFunc("GET /wopi/files/{id}/contents", o.getFile)
	mux.HandleFunc("POST /wopi/files/{id}/contents", o.putFile)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not supported")
	})
	return mux
}

// Proxy passes the browser's requests for OfficePaths on to Collabora; the
// caller puts it behind the login. Collabora's frame may only be shown in
// DATA-DEN's own pages.
func (o *Office) Proxy() http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(o.cfg.URL)
			pr.SetXForwarded()
			// Collabora builds its links from the host the browser used.
			pr.Out.Host = pr.In.Host
			// The login's cookie stays with DATA-DEN.
			pr.Out.Header.Del("Cookie")
		},
		ModifyResponse: func(resp *http.Response) error {
			h := resp.Header
			h.Set("X-Frame-Options", "SAMEORIGIN")
			if csp := h.Get("Content-Security-Policy"); csp != "" {
				h.Set("Content-Security-Policy", frameSelf(csp))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Error("passing a request on to Collabora", "err", err)
			writeError(w, http.StatusBadGateway, "Office nicht erreichbar")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockedOffice.MatchString(r.URL.Path) {
			writeError(w, http.StatusNotFound, "Nicht gefunden")
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

var frameAncestors = regexp.MustCompile(`frame-ancestors[^;]*`)

// frameSelf sets frame-ancestors of a Content-Security-Policy to 'self'.
func frameSelf(csp string) string {
	if frameAncestors.MatchString(csp) {
		return frameAncestors.ReplaceAllString(csp, "frame-ancestors 'self'")
	}
	return strings.TrimRight(csp, "; ") + "; frame-ancestors 'self'"
}

// formats answers which extensions Collabora can edit.
func (o *Office) formats(w http.ResponseWriter, _ *http.Request) {
	urls, err := o.editors()
	if err != nil {
		slog.Error("asking Collabora what it can edit", "err", err)
		writeError(w, http.StatusBadGateway, "Office nicht erreichbar")
		return
	}
	extensions := []string{}
	for ext := range urls {
		extensions = append(extensions, ext)
	}
	slices.Sort(extensions)
	writeJSON(w, http.StatusOK, map[string][]string{"extensions": extensions})
}

// open answers with what the page needs to show the editor for a file.
func (o *Office) open(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("grant") {
		writeError(w, http.StatusForbidden, "In geteilten Ordnern nicht möglich")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Dateiname")
		return
	}
	urls, err := o.editors()
	if err != nil {
		slog.Error("asking Collabora what it can edit", "err", err)
		writeError(w, http.StatusBadGateway, "Office nicht erreichbar")
		return
	}
	editor, ok := urls[lowerExt(name)]
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "Nicht in Office bearbeitbar")
		return
	}
	account := o.spaces.cfg.User(r)
	f, err := o.spaces.space(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
		return
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}

	token, err := newID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Öffnen nicht möglich")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	t := &officeToken{
		account: account, name: name, id: officeID(account, name),
		expires: time.Now().Add(tokenLife), origin: scheme + "://" + r.Host,
	}
	o.mu.Lock()
	for k, old := range o.tokens {
		if time.Now().After(old.expires) {
			delete(o.tokens, k)
		}
	}
	o.tokens[token] = t
	o.mu.Unlock()

	src := strings.TrimSuffix(o.cfg.WOPI, "/") + "/wopi/files/" + t.id
	writeJSON(w, http.StatusOK, map[string]any{
		"url":   editor + "WOPISrc=" + url.QueryEscape(src) + "&lang=de",
		"token": token,
		// Milliseconds since 1970, as WOPI wants it.
		"ttl": t.expires.UnixMilli(),
	})
}

// officeID is the WOPI id of a file: the same for everyone who opens it, so
// Collabora lets them edit it together.
func officeID(account, name string) string {
	sum := sha256.Sum256([]byte(account + "\x00" + name))
	return hex.EncodeToString(sum[:16])
}

// token returns the opened document of a WOPI request, or answers the
// request itself and returns nil.
func (o *Office) token(w http.ResponseWriter, r *http.Request) (*officeToken, *Files) {
	o.mu.Lock()
	t := o.tokens[r.URL.Query().Get("access_token")]
	o.mu.Unlock()
	if t == nil || time.Now().After(t.expires) || t.id != r.PathValue("id") {
		writeError(w, http.StatusUnauthorized, "Ungültiges Token")
		return nil, nil
	}
	f, err := o.spaces.space(t.account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Speicherbereich nicht verfügbar")
		return nil, nil
	}
	if o.spaces.cfg.Quota != nil {
		f.quota.Store(o.spaces.cfg.Quota(t.account))
	}
	return t, f
}

// wopiTime is a file's time as WOPI compares it.
func wopiTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func (o *Office) checkFileInfo(w http.ResponseWriter, r *http.Request) {
	t, f := o.token(w, r)
	if t == nil {
		return
	}
	info, err := f.root.Lstat(t.name)
	if err != nil {
		openError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"BaseFileName":            info.Name(),
		"Size":                    info.Size(),
		"Version":                 versionOf(info),
		"LastModifiedTime":        wopiTime(info.ModTime()),
		"OwnerId":                 f.account,
		"UserId":                  t.account,
		"UserFriendlyName":        t.account,
		"UserCanWrite":            true,
		"UserCanNotWriteRelative": true,
		"SupportsUpdate":          true,
		"SupportsLocks":           false,
		"PostMessageOrigin":       t.origin,
	})
}

func (o *Office) getFile(w http.ResponseWriter, r *http.Request) {
	t, f := o.token(w, r)
	if t == nil {
		return
	}
	f.serveDownload(w, r, t.name)
}

func (o *Office) putFile(w http.ResponseWriter, r *http.Request) {
	t, f := o.token(w, r)
	if t == nil {
		return
	}
	if r.Header.Get("X-WOPI-Override") != "PUT" {
		writeError(w, http.StatusNotImplemented, "not supported")
		return
	}
	info, err := f.root.Lstat(t.name)
	if err != nil {
		openError(w, err)
		return
	}
	// Collabora sends the time the file had when it was opened; another
	// change since must not be overwritten.
	if stamp := r.Header.Get("X-COOL-WOPI-Timestamp"); stamp != "" && stamp != wopiTime(info.ModTime()) {
		writeJSON(w, http.StatusConflict, map[string]int{"COOLStatusCode": 1010})
		return
	}
	o.mu.Lock()
	first := !t.saved
	o.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, MaxOffice)
	saved, ok := f.save(w, t.name, versionOf(info), first, r.Body)
	if !ok {
		return
	}
	o.mu.Lock()
	t.saved = true
	o.mu.Unlock()
	if first {
		// Collabora saves every few minutes; the log notes the first.
		f.note(r, "office", "/"+t.name)
	}
	writeJSON(w, http.StatusOK, map[string]string{"LastModifiedTime": wopiTime(saved.ModTime())})
}

// editors returns Collabora's editor by extension, from its discovery.
func (o *Office) editors() (map[string]string, error) {
	d := &o.discovery
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.urls != nil && time.Since(d.at) < discoveryLife {
		return d.urls, nil
	}
	urls, err := o.fetchDiscovery()
	if err != nil {
		return nil, err
	}
	d.urls, d.at = urls, time.Now()
	return urls, nil
}

func (o *Office) fetchDiscovery() (map[string]string, error) {
	resp, err := o.cfg.Client.Get(o.cfg.URL.JoinPath("hosting", "discovery").String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery answered %s", resp.Status)
	}
	var discovery struct {
		Zones []struct {
			Apps []struct {
				Actions []struct {
					Name string `xml:"name,attr"`
					Ext  string `xml:"ext,attr"`
					URL  string `xml:"urlsrc,attr"`
				} `xml:"action"`
			} `xml:"app"`
		} `xml:"net-zone"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("reading the discovery: %w", err)
	}
	urls := map[string]string{}
	for _, zone := range discovery.Zones {
		for _, app := range zone.Apps {
			for _, action := range app.Actions {
				ext := "." + strings.ToLower(action.Ext)
				if action.Name != "edit" || !slices.Contains(officeFormats, ext) {
					continue
				}
				u, err := url.Parse(action.URL)
				if err != nil || !strings.HasPrefix(u.Path, "/browser/") {
					continue
				}
				// Through DATA-DEN, so only the path counts; the query
				// ends with ? or & for WOPISrc to follow.
				editor := u.Path + "?" + u.RawQuery
				if u.RawQuery != "" && !strings.HasSuffix(u.RawQuery, "&") {
					editor += "&"
				}
				urls[ext] = editor
			}
		}
	}
	if len(urls) == 0 {
		return nil, errors.New("the discovery lists no editor for office documents")
	}
	return urls, nil
}
