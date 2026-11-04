package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushServer stands in for the push services: it takes every request the
// client makes.
type pushServer struct {
	mu     sync.Mutex
	got    []*http.Request
	bodies [][]byte
	status int
}

func (p *pushServer) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, r)
	p.bodies = append(p.bodies, body)
	status := p.status
	if status == 0 {
		status = http.StatusCreated
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

type sentMail struct{ to, msg string }

type fixture struct {
	n     *Notifier
	h     http.Handler
	push  *pushServer
	mu    sync.Mutex
	mails []sentMail
	dir   string
}

func (f *fixture) sent() []sentMail {
	f.n.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMail(nil), f.mails...)
}

// setup has the admin "chriszly" and the user "anna"; the user of a request
// is its X-User header.
func setup(t *testing.T, dir string) *fixture {
	t.Helper()
	f := &fixture{push: &pushServer{}, dir: dir}
	n, err := New(Config{
		Dir:         dir,
		Mail:        &Mail{Host: "mail.example.org", Port: 587, From: "DATA-DEN <nas@example.org>"},
		PushContact: "mailto:nas@example.org",
		User:        func(r *http.Request) string { return r.Header.Get("X-User") },
		System:      func(name string) bool { return name == "chriszly" },
		Client:      &http.Client{Transport: f.push},
		SendMail: func(to string, msg []byte) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.mails = append(f.mails, sentMail{to, string(msg)})
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.n = n
	mux := http.NewServeMux()
	n.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	f.h = mux
	return f
}

func (f *fixture) do(t *testing.T, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User", user)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) view(t *testing.T, user string) view {
	t.Helper()
	rec := f.do(t, user, http.MethodGet, "/api/notifications", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var v view
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	f := setup(t, dir)
	v := f.view(t, "chriszly")
	if len(v.Kinds) != len(kinds) || !v.Mail || v.PushKey == "" {
		t.Errorf("admin view %+v", v)
	}
	// Users without admin rights only get what concerns them.
	if v := f.view(t, "anna"); len(v.Kinds) != 1 || v.Kinds[0].Kind != KindShare {
		t.Errorf("user kinds %+v", v.Kinds)
	}

	rec := f.do(t, "chriszly", http.MethodPost, "/api/notifications",
		`{"email": "chris@example.org", "events": {"disk": {"email": true, "push": true}, "space": {"email": false, "push": false}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	for body, why := range map[string]string{
		`{"email": "not an address"}`:                      "invalid address",
		`{"email": "a@example.org\r\nBcc: b@example.org"}`: "header injection",
		`{"email": "Anna <a@example.org>"}`:                "display name",
		`{"events": {"disk": {"email": true}}}`:            "system kind for a user",
		`{"events": {"something": {"email": true}}}`:       "unknown kind",
		`{"email": "a@example.org", "events": "nonsense"}`: "bad JSON",
	} {
		if rec := f.do(t, "anna", http.MethodPost, "/api/notifications", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", why, rec.Code)
		}
	}

	// Kept across a restart, without the kinds switched off.
	v = setup(t, dir).view(t, "chriszly")
	if v.Email != "chris@example.org" || len(v.Events) != 1 || !v.Events[KindDisk].Push {
		t.Errorf("after restart %+v", v)
	}
}

func TestNotify(t *testing.T) {
	f := setup(t, t.TempDir())
	b := newBrowser(t)
	sub, _ := json.Marshal(b.subscription("https://fcm.googleapis.com/fcm/send/abc"))
	if rec := f.do(t, "chriszly", http.MethodPost, "/api/notifications/subscribe", string(sub)); rec.Code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	f.do(t, "chriszly", http.MethodPost, "/api/notifications", `{"email": "chris@example.org", "events": {"disk": {"email": true, "push": true}, "share": {"push": true}}}`)
	f.do(t, "anna", http.MethodPost, "/api/notifications", `{"email": "anna@example.org", "events": {"share": {"email": true}}}`)

	f.n.NotifySystem(Message{Kind: KindDisk, Title: "RAID md0 beschädigt", Body: "Eine Platte fehlt."})
	mails := f.sent()
	if len(mails) != 1 || mails[0].to != "chris@example.org" || !strings.Contains(mails[0].msg, "Subject: =?utf-8?q?DATA-DEN:_RAID_md0_besch=C3=A4digt?=") {
		t.Fatalf("mails %+v", mails)
	}
	if len(f.push.got) != 1 {
		t.Fatalf("pushed %d", len(f.push.got))
	}
	req := f.push.got[0]
	if req.URL.String() != "https://fcm.googleapis.com/fcm/send/abc" || req.Header.Get("Urgency") != "high" ||
		!strings.HasPrefix(req.Header.Get("Authorization"), "vapid t=") || req.Header.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("push request %v %v", req.URL, req.Header)
	}
	var payload map[string]string
	if err := json.Unmarshal(b.decrypt(t, f.push.bodies[0]), &payload); err != nil || payload["title"] != "RAID md0 beschädigt" {
		t.Errorf("payload %v %v", payload, err)
	}

	// Only by email, as anna chose; the admin by push.
	f.n.Notify("anna", Message{Kind: KindShare, Title: "Neu geteilt", Body: "chriszly teilt /Fotos mit dir."})
	f.n.Notify("chriszly", Message{Kind: KindShare, Title: "Neu geteilt", Body: "anna teilt /Urlaub mit dir."})
	if mails := f.sent(); len(mails) != 2 || mails[1].to != "anna@example.org" {
		t.Errorf("mails %+v", mails)
	}
	if len(f.push.got) != 2 {
		t.Errorf("pushed %d", len(f.push.got))
	}
	// A user cannot get the system events, even when set in the file.
	f.n.Notify("anna", Message{Kind: KindDisk, Title: "x"})
	if mails := f.sent(); len(mails) != 2 {
		t.Errorf("anna got a system event: %+v", mails)
	}

	// A subscription the browser dropped is forgotten.
	f.push.status = http.StatusGone
	f.n.Notify("chriszly", Message{Kind: KindShare, Title: "x"})
	f.n.Wait()
	if v := f.view(t, "chriszly"); len(v.Devices) != 0 {
		t.Errorf("devices %v", v.Devices)
	}

	f.n.Forget("anna")
	if v := f.view(t, "anna"); v.Email != "" {
		t.Errorf("anna not forgotten: %+v", v)
	}
}

func TestSubscribe(t *testing.T) {
	f := setup(t, t.TempDir())
	b := newBrowser(t)
	evil, _ := json.Marshal(b.subscription("https://192.168.1.1/admin"))
	if rec := f.do(t, "anna", http.MethodPost, "/api/notifications/subscribe", string(evil)); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign address: status %d", rec.Code)
	}
	for i := range MaxDevices + 2 {
		sub, _ := json.Marshal(b.subscription("https://fcm.googleapis.com/fcm/send/" + string(rune('a'+i))))
		if rec := f.do(t, "anna", http.MethodPost, "/api/notifications/subscribe", string(sub)); rec.Code != http.StatusOK {
			t.Fatalf("subscribe: %d", rec.Code)
		}
	}
	v := f.view(t, "anna")
	if len(v.Devices) != MaxDevices || v.Devices[0] != "https://fcm.googleapis.com/fcm/send/c" {
		t.Errorf("devices %v", v.Devices)
	}
	f.do(t, "anna", http.MethodPost, "/api/notifications/unsubscribe", `{"endpoint": "https://fcm.googleapis.com/fcm/send/c"}`)
	if v := f.view(t, "anna"); len(v.Devices) != MaxDevices-1 {
		t.Errorf("devices after unsubscribe %v", v.Devices)
	}

	rec := f.do(t, "anna", http.MethodPost, "/api/notifications/test", `{}`)
	var res result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Devices != MaxDevices-1 || res.Email != "" {
		t.Errorf("test: %d %+v", rec.Code, res)
	}
}

func TestNoPushWithoutContact(t *testing.T) {
	n, err := New(Config{Dir: t.TempDir(), User: func(*http.Request) string { return "anna" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	n.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/subscribe", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status %d", rec.Code)
	}
	if v := n.view("anna"); v.PushKey != "" || v.Mail {
		t.Errorf("view %+v", v)
	}
}

func TestMailFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if m, err := MailFromEnv(env(nil)); m != nil || err != nil {
		t.Errorf("empty: %v %v", m, err)
	}
	good := map[string]string{
		"DATA_DEN_SMTP_HOST": "smtp.example.org", "DATA_DEN_SMTP_PORT": "587",
		"DATA_DEN_SMTP_USERNAME": "nas", "DATA_DEN_SMTP_PASSWORD": "geheim",
		"DATA_DEN_SMTP_FROM": "DATA-DEN <nas@example.org>",
	}
	m, err := MailFromEnv(env(good))
	if err != nil || m.Port != 587 || m.Address() != "nas@example.org" {
		t.Fatalf("good: %+v %v", m, err)
	}
	if c, err := PushContact("", m); c != "mailto:nas@example.org" || err != nil {
		t.Errorf("contact %q %v", c, err)
	}
	if _, err := PushContact("nas@example.org", m); err == nil {
		t.Error("contact without mailto: accepted")
	}
	for key, value := range map[string]string{
		"DATA_DEN_SMTP_PORT":     "",
		"DATA_DEN_SMTP_FROM":     "nobody",
		"DATA_DEN_SMTP_PASSWORD": "",
		"DATA_DEN_SMTP_HOST":     "",
	} {
		bad := map[string]string{}
		for k, v := range good {
			bad[k] = v
		}
		bad[key] = value
		if _, err := MailFromEnv(env(bad)); err == nil {
			t.Errorf("%s=%q accepted", key, value)
		}
	}
}

func TestBuildMail(t *testing.T) {
	msg := string(buildMail("DATA-DEN <nas@example.org>", "a@example.org", "Datei\r\nBcc: x@example.org", "Zeile 1\nÄrger", time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), "id@example.org"))
	head, body, _ := strings.Cut(msg, "\r\n\r\n")
	if strings.Contains(head, "\r\nBcc:") {
		t.Errorf("header injected:\n%s", head)
	}
	if !strings.Contains(body, "Zeile 1\r\n=C3=84rger") {
		t.Errorf("body %q", body)
	}
}
