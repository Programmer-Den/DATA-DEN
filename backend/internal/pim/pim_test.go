package pim

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// setup serves calendars and contacts; the account is the Basic auth
// username, each with its own folder under the returned directory.
func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	data := t.TempDir()
	roots := map[string]*os.Root{}
	p := New(Config{
		User: func(r *http.Request) string {
			name, _, _ := r.BasicAuth()
			return name
		},
		Space: func(name string) (*os.Root, error) {
			if root, ok := roots[name]; ok {
				return root, nil
			}
			if err := os.MkdirAll(filepath.Join(data, name), 0o750); err != nil {
				return nil, err
			}
			root, err := os.OpenRoot(filepath.Join(data, name))
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = root.Close() })
			roots[name] = root
			return root, nil
		},
	})
	mux := http.NewServeMux()
	p.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, _, ok := r.BasicAuth(); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, data
}

func client(name string) webdav.HTTPClient {
	return webdav.HTTPClientWithBasicAuth(http.DefaultClient, name, "app-password")
}

func event(uid, summary string) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//test//EN")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetText(ical.PropSummary, summary)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC))
	ev.Props.SetDateTime(ical.PropDateTimeStart, time.Date(2026, 12, 24, 18, 0, 0, 0, time.UTC))
	ev.Props.SetDateTime(ical.PropDateTimeEnd, time.Date(2026, 12, 24, 22, 0, 0, 0, time.UTC))
	cal.Children = append(cal.Children, ev.Component)
	return cal
}

func request(t *testing.T, srv *httptest.Server, user, method, target, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, "app-password")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestCalendarDiscoveryAndEvents(t *testing.T) {
	srv, data := setup(t)
	ctx := context.Background()
	if resp := request(t, srv, "anna", "PROPFIND", "/.well-known/caldav", "", nil); resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/caldav/anna/" {
		t.Fatalf("well-known: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	dc, err := webdav.NewClient(client("anna"), srv.URL+"/caldav/")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := dc.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != "/caldav/anna/" {
		t.Fatalf("principal %q, %v", principal, err)
	}
	c, err := caldav.NewClient(client("anna"), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil || home != "/caldav/anna/calendars/" {
		t.Fatalf("home set %q, %v", home, err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil || len(cals) != 1 || cals[0].Path != "/caldav/anna/calendars/default/" || cals[0].Name != "Kalender" {
		t.Fatalf("calendars %+v, %v", cals, err)
	}

	obj, err := c.PutCalendarObject(ctx, "/caldav/anna/calendars/default/fest.ics", event("fest@test", "Weihnachten"))
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag == "" {
		t.Error("no ETag after PUT")
	}
	got, err := c.GetCalendarObject(ctx, "/caldav/anna/calendars/default/fest.ics")
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Data.Children[0].Props.Get(ical.PropSummary).Value; s != "Weihnachten" {
		t.Errorf("summary %q", s)
	}
	if _, err := os.Stat(filepath.Join(data, "anna", ".data-den-pim", "calendars", "default", "fest.ics")); err != nil {
		t.Errorf("event not stored in the space: %v", err)
	}

	// A time range around the event finds it, one in January does not.
	query := func(start, end time.Time) int {
		t.Helper()
		objs, err := c.QueryCalendar(ctx, "/caldav/anna/calendars/default/", &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
			CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
				Name: "VEVENT", Start: start, End: end,
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(objs)
	}
	if n := query(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); n != 1 {
		t.Errorf("December: %d events", n)
	}
	if n := query(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)); n != 0 {
		t.Errorf("January: %d events", n)
	}

	multi, err := c.MultiGetCalendar(ctx, "/caldav/anna/calendars/default/", &caldav.CalendarMultiGet{
		Paths:       []string{"/caldav/anna/calendars/default/fest.ics"},
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
	})
	if err != nil || len(multi) != 1 || multi[0].ETag != obj.ETag {
		t.Errorf("multiget %+v, %v", multi, err)
	}

	if resp := request(t, srv, "anna", http.MethodDelete, "/caldav/anna/calendars/default/fest.ics", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE: %d", resp.StatusCode)
	}
	if _, err := c.GetCalendarObject(ctx, "/caldav/anna/calendars/default/fest.ics"); err == nil {
		t.Error("event still there after DELETE")
	}
}

func TestPutConditionsAndValidation(t *testing.T) {
	srv, _ := setup(t)
	c, err := caldav.NewClient(client("anna"), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.FindCalendars(ctx, "/caldav/anna/calendars/"); err != nil {
		t.Fatal(err)
	}
	obj, err := c.PutCalendarObject(ctx, "/caldav/anna/calendars/default/a.ics", event("a@test", "A"))
	if err != nil {
		t.Fatal(err)
	}
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:a@test\r\nDTSTAMP:20261118T120000Z\r\nDTSTART:20261224T180000Z\r\nSUMMARY:B\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ics := map[string]string{"Content-Type": "text/calendar"}
	with := func(k, v string) map[string]string { return map[string]string{"Content-Type": "text/calendar", k: v} }
	for _, tc := range []struct {
		name   string
		target string
		body   string
		header map[string]string
		want   int
	}{
		{"create over an existing one", "/caldav/anna/calendars/default/a.ics", body, with("If-None-Match", "*"), http.StatusPreconditionFailed},
		{"stale ETag", "/caldav/anna/calendars/default/a.ics", body, with("If-Match", `"0000"`), http.StatusPreconditionFailed},
		{"If-Match on a new one", "/caldav/anna/calendars/default/new.ics", body, with("If-Match", `"`+obj.ETag+`"`), http.StatusPreconditionFailed},
		{"current ETag", "/caldav/anna/calendars/default/a.ics", body, with("If-Match", `"`+obj.ETag+`"`), http.StatusCreated},
		{"no UID", "/caldav/anna/calendars/default/b.ics", strings.Replace(body, "UID:a@test\r\n", "", 1), ics, http.StatusConflict},
		{"not iCalendar", "/caldav/anna/calendars/default/c.ics", "hello", ics, http.StatusBadRequest},
		{"missing calendar", "/caldav/anna/calendars/nope/a.ics", body, ics, http.StatusConflict},
		{"hidden name", "/caldav/anna/calendars/default/.collection.json", body, ics, http.StatusNotFound},
		{"other account", "/caldav/bob/calendars/default/a.ics", body, ics, http.StatusNotFound},
		{"too large", "/caldav/anna/calendars/default/d.ics", strings.Replace(body, "SUMMARY:B", "SUMMARY:"+strings.Repeat("B", maxObject), 1), ics, http.StatusBadRequest},
	} {
		if resp := request(t, srv, "anna", http.MethodPut, tc.target, tc.body, tc.header); resp.StatusCode != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

func TestAccountsAreSeparate(t *testing.T) {
	srv, _ := setup(t)
	ctx := context.Background()
	anna, _ := caldav.NewClient(client("anna"), srv.URL)
	if _, err := anna.PutCalendarObject(ctx, "/caldav/anna/calendars/default/x.ics", event("x@test", "Geheim")); err != nil {
		t.Fatal(err)
	}
	bob, _ := caldav.NewClient(client("bob"), srv.URL)
	if _, err := bob.GetCalendarObject(ctx, "/caldav/anna/calendars/default/x.ics"); err == nil {
		t.Error("bob reads anna's event")
	}
	if cals, err := bob.FindCalendars(ctx, "/caldav/anna/calendars/"); err == nil && len(cals) != 0 {
		t.Errorf("bob lists anna's calendars: %+v", cals)
	}
	multi, err := bob.MultiGetCalendar(ctx, "/caldav/bob/calendars/default/", &caldav.CalendarMultiGet{
		Paths:       []string{"/caldav/anna/calendars/default/x.ics", "/caldav/bob/calendars/../../anna/calendars/default/x.ics"},
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
	})
	if err == nil && len(multi) != 0 {
		t.Errorf("bob gets anna's events by multiget: %+v", multi)
	}
	if resp := request(t, srv, "bob", http.MethodDelete, "/caldav/anna/calendars/default/", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob deletes anna's calendar: %d", resp.StatusCode)
	}
}

func TestMakeAndDeleteCalendar(t *testing.T) {
	srv, data := setup(t)
	body := `<?xml version="1.0" encoding="utf-8"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set><D:prop><D:displayname>Arbeit</D:displayname></D:prop></D:set>
</C:mkcalendar>`
	xml := map[string]string{"Content-Type": "application/xml"}
	if resp := request(t, srv, "anna", "MKCALENDAR", "/caldav/anna/calendars/arbeit/", body, xml); resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCALENDAR: %d", resp.StatusCode)
	}
	if resp := request(t, srv, "anna", "MKCALENDAR", "/caldav/anna/calendars/arbeit/", body, xml); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("MKCALENDAR again: %d", resp.StatusCode)
	}
	if resp := request(t, srv, "anna", "MKCALENDAR", "/caldav/anna/calendars/", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("MKCALENDAR on the home set: %d", resp.StatusCode)
	}
	if resp := request(t, srv, "anna", "MKCALENDAR", "/caldav/bob/calendars/x/", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("MKCALENDAR for bob: %d", resp.StatusCode)
	}
	c, _ := caldav.NewClient(client("anna"), srv.URL)
	cals, err := c.FindCalendars(context.Background(), "/caldav/anna/calendars/")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, cal := range cals {
		names[cal.Path] = cal.Name
	}
	if names["/caldav/anna/calendars/arbeit/"] != "Arbeit" || names["/caldav/anna/calendars/default/"] != "Kalender" {
		t.Errorf("calendars %v", names)
	}

	if resp := request(t, srv, "anna", http.MethodDelete, "/caldav/anna/calendars/arbeit/", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE calendar: %d", resp.StatusCode)
	}
	deleted, err := os.ReadDir(filepath.Join(data, "anna", ".data-den-pim", ".deleted"))
	if err != nil || len(deleted) != 1 || !strings.HasPrefix(deleted[0].Name(), "calendars-arbeit-") {
		t.Errorf("deleted calendar not kept: %v, %v", deleted, err)
	}
	if resp := request(t, srv, "anna", http.MethodDelete, "/caldav/anna/calendars/", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE home set: %d", resp.StatusCode)
	}
}

func TestContacts(t *testing.T) {
	srv, data := setup(t)
	ctx := context.Background()
	if resp := request(t, srv, "anna", "PROPFIND", "/.well-known/carddav", "", nil); resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "/carddav/anna/" {
		t.Fatalf("well-known: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c, err := carddav.NewClient(client("anna"), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	home, err := c.FindAddressBookHomeSet(ctx, "/carddav/anna/")
	if err != nil || home != "/carddav/anna/contacts/" {
		t.Fatalf("home set %q, %v", home, err)
	}
	books, err := c.FindAddressBooks(ctx, home)
	if err != nil || len(books) != 1 || books[0].Name != "Kontakte" {
		t.Fatalf("address books %+v, %v", books, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "erika@test")
	card.SetValue(vcard.FieldFormattedName, "Erika Mustermann")
	card.SetName(&vcard.Name{FamilyName: "Mustermann", GivenName: "Erika"})
	card.SetValue(vcard.FieldEmail, "erika@example.org")
	if _, err := c.PutAddressObject(ctx, "/carddav/anna/contacts/default/erika.vcf", card); err != nil {
		t.Fatal(err)
	}
	found, err := c.QueryAddressBook(ctx, "/carddav/anna/contacts/default/", &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
		PropFilters: []carddav.PropFilter{{Name: vcard.FieldEmail, TextMatches: []carddav.TextMatch{{Text: "example.org"}}}},
	})
	if err != nil || len(found) != 1 || found[0].Card.Value(vcard.FieldFormattedName) != "Erika Mustermann" {
		t.Errorf("query %+v, %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(data, "anna", ".data-den-pim", "contacts", "default", "erika.vcf")); err != nil {
		t.Errorf("contact not stored in the space: %v", err)
	}
	if resp := request(t, srv, "anna", http.MethodPut, "/carddav/anna/contacts/default/x.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nEND:VCARD\r\n", map[string]string{"Content-Type": "text/vcard"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("contact without name: %d", resp.StatusCode)
	}
	if resp := request(t, srv, "anna", http.MethodDelete, "/carddav/anna/contacts/default/", "", nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE address book: %d", resp.StatusCode)
	}
	if _, err := fs.Stat(os.DirFS(data), "anna/.data-den-pim/contacts/default"); err == nil {
		t.Error("address book still there after DELETE")
	}
}

func TestParse(t *testing.T) {
	a := account{name: "anna"}
	for _, tc := range []struct {
		path, coll, obj string
		ok              bool
	}{
		{"/caldav/anna/calendars/", "", "", true},
		{"/caldav/anna/calendars", "", "", true},
		{"/caldav/anna/calendars/x/", "x", "", true},
		{"/caldav/anna/calendars/x/y.ics", "x", "y.ics", true},
		{"/caldav/anna/calendarsx/", "", "", false},
		{"/caldav/anna/calendars/x/y/z", "", "", false},
		{"/caldav/anna/calendars/.deleted/", "", "", false},
		{"/caldav/anna/calendars/x/../../../bob/calendars/x/", "", "", false},
		{"/caldav/annabelle/calendars/x/", "", "", false},
		{"/carddav/anna/contacts/x/", "", "", false},
	} {
		coll, obj, err := a.parse(calendars, tc.path)
		if (err == nil) != tc.ok || coll != tc.coll || obj != tc.obj {
			t.Errorf("parse(%q) = %q, %q, %v", tc.path, coll, obj, err)
		}
	}
}
