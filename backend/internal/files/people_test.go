package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/vision"
)

// fakeRecognizer tells the photos apart by their width: what it finds in a
// photo of each width is in found.
type fakeRecognizer struct {
	found   map[int]vision.Result
	calls   atomic.Int32
	fail    atomic.Bool
	version string
}

func (r *fakeRecognizer) Recognize(_ context.Context, photo []byte) (vision.Result, error) {
	r.calls.Add(1)
	if r.fail.Load() {
		return vision.Result{}, errors.New("service down")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(photo))
	if err != nil {
		return vision.Result{}, err
	}
	return r.found[config.Width], nil
}

func (r *fakeRecognizer) Version() string { return r.version }

func face(x, y float32) vision.Face {
	return vision.Face{Box: [4]float64{0.25, 0.25, 0.75, 0.75}, Embedding: vision.Normalize([]float32{x, y})}
}

// peopleSpace is a space with five photos and a broken one, and a fake
// recognizer: anna is in a, b and c, bert in b only, a dog in d.
func peopleSpace(t *testing.T) (*Files, http.Handler, *fakeRecognizer, string) {
	t.Helper()
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "Fotos"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, width := range map[string]int{"a.png": 100, "b.png": 101, "c.png": 102, "d.png": 103, "e.png": 104} {
		writePNG(t, filepath.Join(data, "Fotos", name), width, 60)
	}
	if err := os.WriteFile(filepath.Join(data, "Fotos", "kaputt.jpg"), []byte("no photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &fakeRecognizer{version: "v1", found: map[int]vision.Result{
		100: {Faces: []vision.Face{face(1, 0)}},
		101: {Faces: []vision.Face{face(0.95, 0.05), face(0, 1)}, Labels: []string{"Gruppenfotos"}},
		102: {Faces: []vision.Face{face(0.9, 0.1)}},
		103: {Labels: []string{"Hunde"}},
	}}
	f, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	f.people.recognizer = rec
	mux := http.NewServeMux()
	f.Register(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return f, mux, rec, data
}

type peopleAnswer struct {
	Enabled    bool
	Recognised int
	Waiting    int
	People     []Person
}

func listedPeople(t *testing.T, h http.Handler) peopleAnswer {
	t.Helper()
	rec := get(h, "/api/people", "", nil)
	var a peopleAnswer
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	return a
}

func photoItems(t *testing.T, h http.Handler) map[string]Item {
	t.Helper()
	var m Media
	if err := json.Unmarshal(media(h, "photo").Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	items := map[string]Item{}
	for _, item := range m.Items {
		items[item.Name] = item
	}
	return items
}

func getURL(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestRecognizePeople(t *testing.T) {
	f, h, rec, data := peopleSpace(t)
	if err := f.recognizeAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The broken photo never reaches the recognizer.
	if got := rec.calls.Load(); got != 5 {
		t.Errorf("%d photos recognised, want 5", got)
	}
	a := listedPeople(t, h)
	if !a.Enabled || a.Recognised != 6 || a.Waiting != 0 {
		t.Errorf("answer %+v", a)
	}
	// Bert is in one photo only, so not listed yet.
	if len(a.People) != 1 || a.People[0].Photos != 3 || a.People[0].Name != "" {
		t.Fatalf("people %+v", a.People)
	}
	anna := a.People[0].ID

	items := photoItems(t, h)
	if got := items["b.png"]; len(got.People) != 2 || got.People[0] != anna || !slices.Equal(got.Labels, []string{"Gruppenfotos"}) {
		t.Errorf("b.png: %+v", got)
	}
	if got := items["d.png"]; got.People != nil || !slices.Equal(got.Labels, []string{"Hunde"}) {
		t.Errorf("d.png: %+v", got)
	}
	bert := items["b.png"].People[1]

	// Nothing new: nothing is sent again, and the file keeps it all.
	if err := f.recognizeAll(context.Background()); err != nil || rec.calls.Load() != 5 {
		t.Errorf("second round: %v, %d calls", err, rec.calls.Load())
	}
	if _, err := os.Stat(filepath.Join(data, ".data-den-people", "people.json")); err != nil {
		t.Errorf("not kept: %v", err)
	}

	// A name makes bert listed; merging puts him to anna.
	if r := post(h, "/api/people/rename", `{"id": "`+bert+`", "name": " Bert "}`); r.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", r.Code, r.Body)
	}
	if got := listedPeople(t, h).People; len(got) != 2 || got[1].Name != "Bert" {
		t.Errorf("after rename: %+v", got)
	}
	if r := post(h, "/api/people/merge", `{"id": "`+bert+`", "into": "`+anna+`"}`); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"name":"Bert"`) {
		t.Fatalf("merge: %d %s", r.Code, r.Body)
	}
	if got := listedPeople(t, h).People; len(got) != 1 || got[0].ID != anna || got[0].Photos != 3 {
		t.Errorf("after merge: %+v", got)
	}

	// A face's picture is a small square JPEG.
	r := getURL(h, "/api/people/face?id="+anna)
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("face: %d %s", r.Code, r.Body)
	}
	if b := decodeJPEG(t, r.Body.Bytes()).Bounds(); b.Dx() != b.Dy() {
		t.Errorf("face %v, want a square", b)
	}
	if r := getURL(h, "/api/people/face?id=nobody"); r.Code != http.StatusNotFound {
		t.Errorf("unknown face: %d", r.Code)
	}

	// A deleted photo is forgotten; a changed one recognised again.
	if err := os.Remove(filepath.Join(data, "Fotos", "a.png")); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(data, "Fotos", "c.png"), 104, 60)
	if err := f.recognizeAll(context.Background()); err != nil || rec.calls.Load() != 6 {
		t.Errorf("after changes: %v, %d calls", err, rec.calls.Load())
	}
	if got := listedPeople(t, h); got.Recognised != 5 || len(got.People) != 1 || got.People[0].Photos != 1 {
		t.Errorf("after changes: %+v", got)
	}

	// New models: everything again.
	rec.version = "v2"
	if err := f.recognizeAll(context.Background()); err != nil || rec.calls.Load() != 10 {
		t.Errorf("new version: %v, %d calls", err, rec.calls.Load())
	}
}

func TestRecognizeStopsWhenTheServiceFails(t *testing.T) {
	f, h, rec, _ := peopleSpace(t)
	rec.fail.Store(true)
	if err := f.recognizeAll(context.Background()); err == nil || rec.calls.Load() != 1 {
		t.Errorf("error %v after %d calls, want the first to stop it", err, rec.calls.Load())
	}
	if got := listedPeople(t, h); got.Recognised != 0 {
		t.Errorf("%+v", got)
	}
	rec.fail.Store(false)
	if err := f.recognizeAll(context.Background()); err != nil || listedPeople(t, h).Recognised != 6 {
		t.Errorf("later: %v", err)
	}
}

func TestPeopleRequests(t *testing.T) {
	_, h, _, _ := peopleSpace(t)
	for _, tt := range []struct {
		endpoint, body string
		want           int
	}{
		{"/api/people/rename", `{"id": "p1", "name": "x"}`, http.StatusNotFound},
		{"/api/people/rename", `{"id": "p1", "name": "a\nb"}`, http.StatusBadRequest},
		{"/api/people/merge", `{"id": "p1", "into": "p1"}`, http.StatusBadRequest},
		{"/api/people/merge", `{"id": "p1", "into": "p2"}`, http.StatusNotFound},
	} {
		if rec := post(h, tt.endpoint, tt.body); rec.Code != tt.want {
			t.Errorf("%s %s: status %d, want %d", tt.endpoint, tt.body, rec.Code, tt.want)
		}
	}
}

func TestPeopleOff(t *testing.T) {
	_, h := tree(t)
	if a := listedPeople(t, h); a.Enabled || a.People == nil {
		t.Errorf("%+v", a)
	}
	if rec := post(h, "/api/people/rename", `{"id": "p1", "name": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("rename: %d", rec.Code)
	}
}
