package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/vision"
)

// People and things in the photos. In the background each space's photos
// are sent, one at a time and made smaller, to a PhotoRecognizer, which runs on
// the NAS. The faces found are put together into people: a face goes to the
// person whose faces it is most like, or else becomes a new person. What was
// found is kept in peopleFile, with the photo's size and time, so each photo
// is recognised once, and again only when it changes.
//
//	GET  /api/people                  -> {"enabled", "recognised", "waiting", "people": [...]}
//	GET  /api/people/face?id=         -> a JPEG of the person's face
//	POST /api/people/rename {"id", "name"}
//	POST /api/people/merge  {"id", "into"}
//
// /api/media gives each photo its "people" (ids) and "labels" (such as
// "Hunde"). Only people with a name or in at least minPersonPhotos photos are
// listed, so a stranger in the background does not become a person.
const (
	peopleFile = ".data-den-people/people.json"
	// sameFace is how alike two faces must be to be one person: the cosine
	// of their embeddings, as Immich uses it.
	sameFace = 0.5
	// minPersonPhotos is how many photos an unnamed person needs to be
	// listed.
	minPersonPhotos = 3
	// recognizeSize is the longer side of what the PhotoRecognizer gets.
	recognizeSize = 1440
	// faceSize is the side of a face's picture.
	faceSize = 160
	// saveEvery is after how many photos a round writes what it found.
	saveEveryPhotos = 50
	// recognizeInterval is how often new photos are looked for.
	recognizeInterval = 10 * time.Minute
)

// PhotoRecognizer finds faces and things in a photo, such as *vision.Client.
type PhotoRecognizer interface {
	Recognize(ctx context.Context, jpeg []byte) (vision.Result, error)
	// Version changes when the models or labels change; photos are then
	// recognised again.
	Version() string
}

// recognised is what was found in one photo.
type recognised struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Version  string    `json:"version"`
	Faces    []faceRef `json:"faces,omitempty"`
	Labels   []string  `json:"labels,omitempty"`
	// Failed is true when the photo could not be read; it is tried again
	// when it changes.
	Failed bool `json:"failed,omitempty"`
}

// faceRef is a face in a photo: whose, and where, as fractions of the
// photo's width and height (left, top, right, bottom).
type faceRef struct {
	Person string     `json:"person"`
	Box    [4]float64 `json:"box"`
	// Embedding is only kept until the face has its person.
	Embedding []float32 `json:"-"`
}

// person is the faces that are alike.
type person struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Center is the mean of the faces' embeddings, of length 1, and Weight
	// how many faces it is made of.
	Center []float32 `json:"center"`
	Weight int       `json:"weight"`
}

type peopleData struct {
	// Photos are by name, such as "Fotos/a.jpg".
	Photos map[string]*recognised `json:"photos"`
	People []*person              `json:"people"`
	// Next numbers the next new person.
	Next int `json:"next"`
}

type people struct {
	mu sync.Mutex
	// data is nil until it was read from peopleFile.
	data       *peopleData
	recognizer PhotoRecognizer
	// waiting is how many photos the round running has still to recognise.
	waiting int
}

// Person is a person as /api/people lists them.
type Person struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Photos int    `json:"photos"`
}

func (f *Files) registerPeople(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/people", require(f.listPeople))
	mux.HandleFunc("GET /api/people/face", require(f.personFace))
	mux.HandleFunc("POST /api/people/rename", require(f.renamePerson))
	mux.HandleFunc("POST /api/people/merge", require(f.mergePeople))
}

// loadPeopleLocked reads peopleFile on first use. The caller holds mu.
func (f *Files) loadPeopleLocked() error {
	if f.people.data != nil {
		return nil
	}
	data, err := f.root.ReadFile(peopleFile)
	if errors.Is(err, fs.ErrNotExist) {
		f.people.data = &peopleData{Photos: map[string]*recognised{}}
		return nil
	}
	if err != nil {
		return err
	}
	var d peopleData
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.Photos == nil {
		d.Photos = map[string]*recognised{}
	}
	f.people.data = &d
	return nil
}

// savePeopleLocked writes peopleFile through a temporary file. The caller
// holds mu.
func (f *Files) savePeopleLocked() error {
	data, err := json.Marshal(f.people.data)
	if err != nil {
		return err
	}
	if err := f.root.MkdirAll(path.Dir(peopleFile), 0o750); err != nil {
		return err
	}
	tmp := peopleFile + ".tmp"
	if err := f.root.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return f.root.Rename(tmp, peopleFile)
}

// RecognizeEvery recognises the new and changed photos at once and then
// every interval, until ctx is done.
func (f *Files) RecognizeEvery(ctx context.Context, interval time.Duration) {
	for {
		if err := f.recognizeAll(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("recognising faces and things; trying again later", "account", f.account, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// recognizeAll recognises each photo that is new or changed, and forgets
// those that are gone. It stops at the first failure of the PhotoRecognizer,
// such as when its service is not running.
func (f *Files) recognizeAll(ctx context.Context) error {
	rec := f.people.recognizer
	version := rec.Version()
	type photo struct {
		name string
		info fs.FileInfo
	}
	var todo []photo
	seen := map[string]bool{}
	f.people.mu.Lock()
	err := f.loadPeopleLocked()
	f.people.mu.Unlock()
	if err != nil {
		return fmt.Errorf("reading %s: %w", peopleFile, err)
	}
	err = fs.WalkDir(f.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if hidden(name) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !thumbFormats[lowerExt(name)] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		seen[name] = true
		f.people.mu.Lock()
		r := f.people.data.Photos[name]
		f.people.mu.Unlock()
		if !r.current(info) || r.Version != version {
			todo = append(todo, photo{name, info})
		}
		return nil
	})
	if err != nil {
		return err
	}

	f.people.mu.Lock()
	changed := false
	for name, r := range f.people.data.Photos {
		if !seen[name] {
			f.forgetFacesLocked(r)
			delete(f.people.data.Photos, name)
			changed = true
		}
	}
	f.people.waiting = len(todo)
	f.people.mu.Unlock()
	defer func() {
		f.people.mu.Lock()
		f.people.waiting = 0
		if changed {
			if err := f.savePeopleLocked(); err != nil {
				slog.Warn("keeping the people", "account", f.account, "err", err)
			}
		}
		f.people.mu.Unlock()
	}()

	for i, p := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r := &recognised{Size: p.info.Size(), Modified: p.info.ModTime().UTC(), Version: version}
		photo, err := f.recognizable(p.name)
		if err != nil {
			r.Failed = true
		} else {
			result, err := rec.Recognize(ctx, photo)
			if err != nil {
				return err
			}
			r.Labels = result.Labels
			for _, face := range result.Faces {
				r.Faces = append(r.Faces, faceRef{Box: face.Box, Embedding: face.Embedding})
			}
		}
		f.people.mu.Lock()
		if old := f.people.data.Photos[p.name]; old != nil {
			f.forgetFacesLocked(old)
		}
		for j := range r.Faces {
			r.Faces[j].Person = f.addFaceLocked(r.Faces[j].Embedding)
			r.Faces[j].Embedding = nil
		}
		f.people.data.Photos[p.name] = r
		f.people.waiting = len(todo) - i - 1
		changed = true
		if (i+1)%saveEveryPhotos == 0 {
			if err := f.savePeopleLocked(); err != nil {
				slog.Warn("keeping the people", "account", f.account, "err", err)
			} else {
				changed = false
			}
		}
		f.people.mu.Unlock()
	}
	return nil
}

// current is true when r is about the photo as info says it is now.
func (r *recognised) current(info fs.FileInfo) bool {
	return r != nil && r.Size == info.Size() && r.Modified.Equal(info.ModTime().UTC())
}

// addFaceLocked puts the face with embedding to the person it is most like,
// or to a new person, and returns the person's id. The caller holds mu.
func (f *Files) addFaceLocked(embedding []float32) string {
	d := f.people.data
	var best *person
	bestSimilarity := sameFace
	for _, p := range d.People {
		if s := vision.Similarity(p.Center, embedding); s >= bestSimilarity {
			best, bestSimilarity = p, s
		}
	}
	if best == nil {
		d.Next++
		best = &person{ID: "p" + strconv.Itoa(d.Next), Center: make([]float32, len(embedding))}
		d.People = append(d.People, best)
	}
	for i := range best.Center {
		best.Center[i] = best.Center[i]*float32(best.Weight) + embedding[i]
	}
	vision.Normalize(best.Center)
	best.Weight++
	return best.ID
}

// forgetFacesLocked takes the faces of r away from their people; an unnamed
// person without faces is gone. The caller holds mu.
func (f *Files) forgetFacesLocked(r *recognised) {
	d := f.people.data
	for _, face := range r.Faces {
		for i, p := range d.People {
			if p.ID != face.Person {
				continue
			}
			p.Weight--
			if p.Weight <= 0 && p.Name == "" {
				d.People = slices.Delete(d.People, i, i+1)
			}
			break
		}
	}
}

// recognition is who and what is in the photo name as info says it is now;
// nil when it was not recognised (yet).
func (f *Files) recognition(name string, info fs.FileInfo) (people, labels []string) {
	if f.people.recognizer == nil {
		return nil, nil
	}
	f.people.mu.Lock()
	defer f.people.mu.Unlock()
	if f.loadPeopleLocked() != nil {
		return nil, nil
	}
	r := f.people.data.Photos[name]
	if !r.current(info) {
		return nil, nil
	}
	for _, face := range r.Faces {
		if !slices.Contains(people, face.Person) {
			people = append(people, face.Person)
		}
	}
	return people, r.Labels
}

// recognizable is the photo name as a JPEG for the PhotoRecognizer: turned the
// way it is shown and at most recognizeSize pixels.
func (f *Files) recognizable(name string) ([]byte, error) {
	img, err := f.decodeShown(name, recognizeSize)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decodeShown decodes the photo name, at most size pixels and turned the
// way the browser shows it.
func (f *Files) decodeShown(name string, size int) (image.Image, error) {
	f.thumbSlots <- struct{}{}
	defer func() { <-f.thumbSlots }()
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width*config.Height > maxPixels {
		return nil, errNoThumb
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(file)
	if err != nil {
		return nil, errNoThumb
	}
	orientation := 1
	if exifFormats[lowerExt(name)] {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		orientation = exifOrientation(file)
	}
	return orient(scale(src, size), orientation), nil
}

func (f *Files) listPeople(w http.ResponseWriter, _ *http.Request) {
	f.people.mu.Lock()
	defer f.people.mu.Unlock()
	answer := struct {
		Enabled    bool     `json:"enabled"`
		Recognised int      `json:"recognised"`
		Waiting    int      `json:"waiting"`
		People     []Person `json:"people"`
	}{Enabled: f.people.recognizer != nil, People: []Person{}}
	if !answer.Enabled {
		writeJSON(w, http.StatusOK, answer)
		return
	}
	if err := f.loadPeopleLocked(); err != nil {
		slog.Error("reading the people", "err", err)
		writeError(w, http.StatusInternalServerError, "Personen nicht lesbar")
		return
	}
	photos := map[string]int{}
	for _, r := range f.people.data.Photos {
		seen := map[string]bool{}
		for _, face := range r.Faces {
			if !seen[face.Person] {
				seen[face.Person] = true
				photos[face.Person]++
			}
		}
	}
	for _, p := range f.people.data.People {
		if p.Name != "" || photos[p.ID] >= minPersonPhotos {
			answer.People = append(answer.People, Person{ID: p.ID, Name: p.Name, Photos: photos[p.ID]})
		}
	}
	slices.SortStableFunc(answer.People, func(a, b Person) int { return b.Photos - a.Photos })
	answer.Recognised, answer.Waiting = len(f.people.data.Photos), f.people.waiting
	writeJSON(w, http.StatusOK, answer)
}

// personLocked is the person id, nil when there is none. The caller holds mu.
func (f *Files) personLocked(id string) *person {
	for _, p := range f.people.data.People {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// personFace serves a square picture of a person's face: the largest of
// their faces, cut out of its photo. It is kept with the thumbnails.
func (f *Files) personFace(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	f.people.mu.Lock()
	var name string
	var box [4]float64
	var photo *recognised
	if f.people.recognizer != nil && f.loadPeopleLocked() == nil {
		largest := 0.0
		for n, rec := range f.people.data.Photos {
			for _, face := range rec.Faces {
				area := (face.Box[2] - face.Box[0]) * (face.Box[3] - face.Box[1])
				// The name decides between alike faces, so it is always the same one.
				if face.Person == id && (area > largest || area == largest && n < name) {
					name, box, photo, largest = n, face.Box, rec, area
				}
			}
		}
	}
	f.people.mu.Unlock()
	if photo == nil {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "face\x00%s\x00%d\x00%s\x00%v", name, photo.Size, photo.Modified.Format(time.RFC3339Nano), box))
	key := hex.EncodeToString(sum[:16])
	cached := path.Join(thumbDir, "faces", key[:2], key+".jpg")
	data, err := f.root.ReadFile(cached)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = f.makeFace(name, box, cached)
	}
	if err != nil {
		slog.Warn("cutting out a face", "path", name, "err", err)
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// makeFace cuts the face at box out of the photo name, with some room
// around it, and keeps it at cached.
func (f *Files) makeFace(name string, box [4]float64, cached string) ([]byte, error) {
	img, err := f.decodeShown(name, recognizeSize)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	cx, cy := (box[0]+box[2])/2*w, (box[1]+box[3])/2*h
	side := max((box[2]-box[0])*w, (box[3]-box[1])*h) * 1.5
	side = min(side, w, h)
	left := min(max(cx-side/2, 0), w-side)
	top := min(max(cy-side/2, 0), h-side)
	rect := image.Rect(int(left), int(top), int(left+side), int(top+side)).Add(b.Min)
	crop := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := range rect.Dy() {
		for x := range rect.Dx() {
			crop.Set(x, y, img.At(rect.Min.X+x, rect.Min.Y+y))
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scale(crop, faceSize), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	data := out.Bytes()
	if err := f.keepThumb(cached, data); err != nil {
		slog.Warn("keeping a face", "path", name, "err", err)
	}
	return data, nil
}

// withPeople runs change on the people and saves them when it returns
// true. It answers the request itself when reading or saving fails.
func (f *Files) withPeople(w http.ResponseWriter, change func() (status int, v any, save bool)) {
	f.people.mu.Lock()
	defer f.people.mu.Unlock()
	if f.people.recognizer == nil {
		writeError(w, http.StatusNotFound, "Gesichtserkennung ist aus")
		return
	}
	if err := f.loadPeopleLocked(); err != nil {
		slog.Error("reading the people", "err", err)
		writeError(w, http.StatusInternalServerError, "Personen nicht lesbar")
		return
	}
	status, v, save := change()
	if save {
		if err := f.savePeopleLocked(); err != nil {
			// What is in memory is what the file holds.
			f.people.data = nil
			f.diskError(w, "saving the people", err)
			return
		}
	}
	writeJSON(w, status, v)
}

func (f *Files) renamePerson(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := albumName(body.Name)
	if name == "" && body.Name != "" {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	f.withPeople(w, func() (int, any, bool) {
		p := f.personLocked(body.ID)
		if p == nil {
			return http.StatusNotFound, map[string]string{"error": "Person nicht gefunden"}, false
		}
		p.Name = name
		return http.StatusOK, map[string]string{"id": p.ID, "name": p.Name}, true
	})
}

// mergePeople puts the faces of id to into, when the recognition took one
// person for two.
func (f *Files) mergePeople(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Into string `json:"into"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.ID == body.Into {
		writeError(w, http.StatusBadRequest, "Dieselbe Person")
		return
	}
	f.withPeople(w, func() (int, any, bool) {
		from, into := f.personLocked(body.ID), f.personLocked(body.Into)
		if from == nil || into == nil {
			return http.StatusNotFound, map[string]string{"error": "Person nicht gefunden"}, false
		}
		for _, rec := range f.people.data.Photos {
			for i := range rec.Faces {
				if rec.Faces[i].Person == from.ID {
					rec.Faces[i].Person = into.ID
				}
			}
		}
		if len(from.Center) == len(into.Center) {
			for i := range into.Center {
				into.Center[i] = into.Center[i]*float32(into.Weight) + from.Center[i]*float32(from.Weight)
			}
			vision.Normalize(into.Center)
		}
		into.Weight += from.Weight
		if into.Name == "" {
			into.Name = from.Name
		}
		f.people.data.People = slices.DeleteFunc(f.people.data.People, func(p *person) bool { return p == from })
		return http.StatusOK, map[string]string{"id": into.ID, "name": into.Name}, true
	})
}
