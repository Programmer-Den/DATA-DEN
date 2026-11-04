package files

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func showMarks(t *testing.T, h http.Handler) Marks {
	t.Helper()
	rec := get(h, "/api/marks", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var m Marks
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func markedPaths(items []Marked) []string {
	var p []string
	for _, item := range items {
		p = append(p, item.Path)
	}
	return p
}

func tagged(t *testing.T, h http.Handler, tag string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tagged?tag="+url.QueryEscape(tag), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var p []string
	for _, item := range body.Items {
		p = append(p, item.Path)
	}
	return p
}

func mustPost(t *testing.T, h http.Handler, endpoint, body string) {
	t.Helper()
	if rec := post(h, endpoint, body); rec.Code != http.StatusOK {
		t.Fatalf("%s %s: status %d, body %s", endpoint, body, rec.Code, rec.Body)
	}
}

func TestTags(t *testing.T) {
	_, h := tree(t)
	mustPost(t, h, "/api/tags", `{"path":"/Fotos/a.jpg","tags":[" Urlaub ","2026","urlaub",""]}`)
	mustPost(t, h, "/api/tags", `{"path":"/Fotos/2026","tags":["Urlaub"]}`)

	l := listing(t, get(h, "/api/files", "/Fotos", nil))
	for _, e := range l.Entries {
		want := map[string][]string{"a.jpg": {"Urlaub", "2026"}, "2026": {"Urlaub"}}[e.Name]
		if !slices.Equal(e.Tags, want) {
			t.Errorf("%s: tags %q, want %q", e.Name, e.Tags, want)
		}
	}
	if got := tagged(t, h, "URLAUB"); !slices.Equal(got, []string{"/Fotos/2026", "/Fotos/a.jpg"}) {
		t.Errorf("tagged Urlaub: %q", got)
	}
	m := showMarks(t, h)
	if !slices.Equal(m.Tags, []TagCount{{"2026", 1}, {"Urlaub", 2}}) {
		t.Errorf("tag counts %+v", m.Tags)
	}

	// No tags removes them.
	mustPost(t, h, "/api/tags", `{"path":"/Fotos/a.jpg","tags":[]}`)
	if got := tagged(t, h, "2026"); len(got) != 0 {
		t.Errorf("tagged 2026 after removing: %q", got)
	}
}

func TestTagsRefuses(t *testing.T) {
	_, h := tree(t)
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = `"t` + strings.Repeat("x", i) + `"`
	}
	tests := []struct {
		body string
		want int
	}{
		{`{"path":"/fehlt.txt","tags":["a"]}`, http.StatusNotFound},
		{`{"path":"/","tags":["a"]}`, http.StatusBadRequest},
		{`{"path":"/../x","tags":["a"]}`, http.StatusBadRequest},
		{`{"path":"/.data-den-marks/marks.json","tags":["a"]}`, http.StatusBadRequest},
		{`{"path":"/notizen.txt","tags":["a,b"]}`, http.StatusBadRequest},
		{`{"path":"/notizen.txt","tags":["a\nb"]}`, http.StatusBadRequest},
		{`{"path":"/notizen.txt","tags":["` + strings.Repeat("x", MaxTagLength+1) + `"]}`, http.StatusBadRequest},
		{`{"path":"/notizen.txt","tags":[` + strings.Join(many, ",") + `]}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if rec := post(h, "/api/tags", tt.body); rec.Code != tt.want {
			t.Errorf("%.60s: status %d, want %d", tt.body, rec.Code, tt.want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tagged?tag=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty tag: status %d", rec.Code)
	}
}

func TestFavourites(t *testing.T) {
	_, h := tree(t)
	mustPost(t, h, "/api/favourites", `{"path":"/notizen.txt","favourite":true}`)
	mustPost(t, h, "/api/favourites", `{"path":"/Fotos","favourite":true}`)
	if got := markedPaths(showMarks(t, h).Favourites); !slices.Equal(got, []string{"/Fotos", "/notizen.txt"}) {
		t.Errorf("favourites %q, want the newest first", got)
	}
	for _, e := range listing(t, get(h, "/api/files", "/", nil)).Entries {
		if !e.Favourite {
			t.Errorf("%s is not shown as a favourite", e.Name)
		}
	}
	mustPost(t, h, "/api/favourites", `{"path":"/Fotos","favourite":false}`)
	if got := markedPaths(showMarks(t, h).Favourites); !slices.Equal(got, []string{"/notizen.txt"}) {
		t.Errorf("favourites after removing %q", got)
	}
	if rec := post(h, "/api/favourites", `{"path":"/fehlt","favourite":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("missing file: status %d", rec.Code)
	}
}

func TestRecent(t *testing.T) {
	_, h := tree(t)
	get(h, "/api/download", "/notizen.txt", nil)
	get(h, "/api/view", "/Fotos/a.jpg", nil)
	// Later pieces of a file do not count, nor files that are not there.
	get(h, "/api/download", "/Fotos/2026/b.jpg", http.Header{"Range": {"bytes=5-"}})
	get(h, "/api/download", "/fehlt.txt", nil)
	if got := markedPaths(showMarks(t, h).Recent); !slices.Equal(got, []string{"/Fotos/a.jpg", "/notizen.txt"}) {
		t.Fatalf("recent %q", got)
	}
	// Used again: back to the top, once.
	get(h, "/api/download", "/notizen.txt", http.Header{"Range": {"bytes=0-"}})
	recent := showMarks(t, h).Recent
	if got := markedPaths(recent); !slices.Equal(got, []string{"/notizen.txt", "/Fotos/a.jpg"}) {
		t.Errorf("recent %q", got)
	}
	if recent[0].Used.IsZero() {
		t.Error("no time of use")
	}
}

func TestRecentUpload(t *testing.T) {
	_, h := tree(t)
	rec := post(h, "/api/uploads", `{"path":"/leer.txt","size":0}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := markedPaths(showMarks(t, h).Recent); !slices.Equal(got, []string{"/leer.txt"}) {
		t.Errorf("recent %q", got)
	}
}

func TestMarksFollowMoves(t *testing.T) {
	data, _ := tree(t)
	h := handler(t, data)
	mustPost(t, h, "/api/tags", `{"path":"/Fotos/2026/b.jpg","tags":["Urlaub"]}`)
	mustPost(t, h, "/api/favourites", `{"path":"/Fotos/2026/b.jpg","favourite":true}`)
	get(h, "/api/download", "/Fotos/2026/b.jpg", nil)

	mustPost(t, h, "/api/move", `{"from":"/Fotos","to":"/Bilder"}`)
	m := showMarks(t, h)
	if got := tagged(t, h, "Urlaub"); !slices.Equal(got, []string{"/Bilder/2026/b.jpg"}) {
		t.Errorf("tagged after the move %q", got)
	}
	if !slices.Equal(markedPaths(m.Favourites), []string{"/Bilder/2026/b.jpg"}) || !slices.Equal(markedPaths(m.Recent), []string{"/Bilder/2026/b.jpg"}) {
		t.Errorf("after the move %+v", m)
	}

	// In the recycle bin they are not shown, and come back with the file.
	rec := post(h, "/api/delete", `{"path":"/Bilder"}`)
	var deleted struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	m = showMarks(t, h)
	if len(m.Tags)+len(m.Favourites)+len(m.Recent) != 0 {
		t.Errorf("deleted file still marked %+v", m)
	}
	mustPost(t, h, "/api/trash/restore", `{"id":"`+deleted.ID+`"}`)
	if got := tagged(t, h, "Urlaub"); !slices.Equal(got, []string{"/Bilder/2026/b.jpg"}) {
		t.Errorf("tagged after restoring %q", got)
	}

	// Kept across a restart.
	h = handler(t, data)
	m = showMarks(t, h)
	if len(m.Favourites) != 1 || len(m.Recent) != 1 || len(m.Tags) != 1 {
		t.Errorf("after a restart %+v", m)
	}

	// Gone for good: forgotten.
	rec = post(h, "/api/delete", `{"path":"/Bilder"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	mustPost(t, h, "/api/trash/delete", `{"id":"`+deleted.ID+`"}`)
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if n := len(f.marks.Tags) + len(f.marks.Favourites) + len(f.marks.Recent); n != 0 {
		t.Errorf("%d marks left of a file deleted for good", n)
	}
}

func TestShareHidesMarks(t *testing.T) {
	_, h, _ := shareTree(t)
	mustPost(t, h, "/api/tags", `{"path":"/Fotos/a.jpg","tags":["Privat"]}`)
	mustPost(t, h, "/api/favourites", `{"path":"/Fotos/a.jpg","favourite":true}`)
	s := share(t, h, `{"path":"/Fotos","days":1}`)
	rec := public(h, http.MethodGet, "/api/s/"+s.ID+"/files?path=/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, "Privat") || strings.Contains(body, "favourite") {
		t.Errorf("a share link shows the marks: %s", body)
	}
}
