package files

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Tags, favourites and the recently used files of a space, kept in
// marksFile:
//
//	GET  /api/marks                                     -> {"tags": [...], "favourites": [...], "recent": [...]}
//	GET  /api/tagged?tag=Urlaub                         -> {"tag": "Urlaub", "items": [...]}
//	POST /api/tags       {"path": "/a.jpg", "tags": ["Urlaub", "2026"]} -> 200, replaces the file's tags
//	POST /api/favourites {"path": "/a.jpg", "favourite": true}          -> 200
//
// A file counts as used when it is opened, downloaded or uploaded. The marks
// follow a file when it is moved, into the recycle bin and back, and are
// forgotten when it is deleted for good.
const (
	marksDir  = ".data-den-marks"
	marksFile = marksDir + "/marks.json"
	// MaxTags is how many tags one file or folder can have.
	MaxTags = 20
	// MaxTagLength is the longest tag, in characters.
	MaxTagLength = 40
	// RecentFiles is how many recently used files are kept.
	RecentFiles = 50
	// recentQuiet is how long a file used again at the top of the list is
	// not saved again, so a video that loads in many pieces writes once.
	recentQuiet = time.Minute
)

// use is one recently used file.
type use struct {
	Path string    `json:"path"`
	Used time.Time `json:"used"`
}

// marks holds the tags, favourites and recently used files, by path within
// the root.
type marks struct {
	mu         sync.Mutex
	Tags       map[string][]string  `json:"tags"`
	Favourites map[string]time.Time `json:"favourites"`
	// Recent is newest first.
	Recent []use `json:"recent"`
	// saveMu keeps two saves from writing the file at the same time.
	saveMu sync.Mutex
}

// TagCount is a tag and how many files and folders have it.
type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Marked is a favourite or recently used file or folder.
type Marked struct {
	Item
	// Used is when the file was last used, only in the recent list.
	Used time.Time `json:"used,omitzero"`
}

// Marks is the answer of /api/marks.
type Marks struct {
	Tags       []TagCount `json:"tags"`
	Favourites []Marked   `json:"favourites"`
	Recent     []Marked   `json:"recent"`
}

func (f *Files) registerMarks(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/marks", require(f.showMarks))
	mux.HandleFunc("GET /api/tagged", require(f.tagged))
	mux.HandleFunc("POST /api/tags", require(f.setTags))
	mux.HandleFunc("POST /api/favourites", require(f.setFavourite))
}

// loadMarks reads marksFile. A missing one starts empty; an unreadable one
// too, with a warning, since the files themselves are not affected.
func (f *Files) loadMarks() {
	m := &f.marks
	m.Tags, m.Favourites, m.Recent = map[string][]string{}, map[string]time.Time{}, []use{}
	data, err := f.root.ReadFile(marksFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(data, m)
	}
	if err != nil {
		slog.Warn("the tags and favourites cannot be read, so they start empty", "err", err)
	}
	if m.Tags == nil {
		m.Tags = map[string][]string{}
	}
	if m.Favourites == nil {
		m.Favourites = map[string]time.Time{}
	}
	if m.Recent == nil {
		m.Recent = []use{}
	}
}

// saveMarks writes marksFile, through a temporary file so a crash never
// leaves half of it.
func (f *Files) saveMarks() {
	m := &f.marks
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	data, err := json.Marshal(m)
	m.mu.Unlock()
	if err == nil {
		err = f.root.MkdirAll(marksDir, 0o750)
	}
	if err == nil {
		err = f.root.WriteFile(marksFile+".tmp", data, 0o640)
	}
	if err == nil {
		err = f.root.Rename(marksFile+".tmp", marksFile)
	}
	if err != nil {
		slog.Error("saving the tags and favourites", "err", err)
	}
}

// noteUse puts the file name at the top of the recently used files.
func (f *Files) noteUse(name string) {
	m := &f.marks
	now := time.Now().UTC()
	m.mu.Lock()
	if len(m.Recent) > 0 && m.Recent[0].Path == name && now.Sub(m.Recent[0].Used) < recentQuiet {
		m.mu.Unlock()
		return
	}
	m.Recent = slices.DeleteFunc(m.Recent, func(u use) bool { return u.Path == name })
	m.Recent = slices.Insert(m.Recent, 0, use{Path: name, Used: now})
	if len(m.Recent) > RecentFiles {
		m.Recent = m.Recent[:RecentFiles]
	}
	m.mu.Unlock()
	f.saveMarks()
}

// firstPiece is true for a request of a whole file or of its start: a video
// that plays asks for many pieces, but counts as used once.
func firstPiece(r *http.Request) bool {
	rng := r.Header.Get("Range")
	return rng == "" || strings.HasPrefix(rng, "bytes=0-")
}

// marksMoved follows a rename of a file or folder from one path to another.
func (f *Files) marksMoved(from, to string) {
	moved := func(p string) (string, bool) {
		if p == from {
			return to, true
		}
		if rest, ok := strings.CutPrefix(p, from+"/"); ok {
			return to + "/" + rest, true
		}
		return "", false
	}
	m := &f.marks
	m.mu.Lock()
	changed := false
	for p, tags := range m.Tags {
		if q, ok := moved(p); ok {
			delete(m.Tags, p)
			m.Tags[q] = tags
			changed = true
		}
	}
	for p, added := range m.Favourites {
		if q, ok := moved(p); ok {
			delete(m.Favourites, p)
			m.Favourites[q] = added
			changed = true
		}
	}
	for i := range m.Recent {
		if q, ok := moved(m.Recent[i].Path); ok {
			m.Recent[i].Path = q
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		f.saveMarks()
	}
}

// marksDropped forgets a file or folder that is gone for good.
func (f *Files) marksDropped(name string) {
	gone := func(p string) bool { return p == name || strings.HasPrefix(p, name+"/") }
	m := &f.marks
	m.mu.Lock()
	before := len(m.Tags) + len(m.Favourites) + len(m.Recent)
	maps.DeleteFunc(m.Tags, func(p string, _ []string) bool { return gone(p) })
	maps.DeleteFunc(m.Favourites, func(p string, _ time.Time) bool { return gone(p) })
	m.Recent = slices.DeleteFunc(m.Recent, func(u use) bool { return gone(u.Path) })
	changed := len(m.Tags)+len(m.Favourites)+len(m.Recent) != before
	m.mu.Unlock()
	if changed {
		f.saveMarks()
	}
}

// mark adds the tags and the favourite of name to its entry.
func (f *Files) mark(name string, e *Entry) {
	m := &f.marks
	m.mu.Lock()
	defer m.mu.Unlock()
	e.Tags = slices.Clone(m.Tags[name])
	_, e.Favourite = m.Favourites[name]
}

// marked is the file or folder name as an item with its marks, or false
// when it is gone, hidden or neither a file nor a folder.
func (f *Files) marked(name string) (Item, bool) {
	if hidden(name) {
		return Item{}, false
	}
	info, err := f.root.Lstat(name)
	if err != nil || (!info.Mode().IsDir() && !info.Mode().IsRegular()) {
		return Item{}, false
	}
	item := Item{Path: "/" + name, Entry: entryOf(info)}
	f.mark(name, &item.Entry)
	return item, true
}

func (f *Files) showMarks(w http.ResponseWriter, _ *http.Request) {
	m := &f.marks
	m.mu.Lock()
	counts := map[string]int{}
	for p, tags := range m.Tags {
		if hidden(p) {
			// In the recycle bin.
			continue
		}
		for _, tag := range tags {
			counts[tag]++
		}
	}
	type favourite struct {
		path  string
		added time.Time
	}
	favourites := make([]favourite, 0, len(m.Favourites))
	for p, added := range m.Favourites {
		favourites = append(favourites, favourite{p, added})
	}
	recent := slices.Clone(m.Recent)
	m.mu.Unlock()

	answer := Marks{Tags: []TagCount{}, Favourites: []Marked{}, Recent: []Marked{}}
	for name, count := range counts {
		answer.Tags = append(answer.Tags, TagCount{Name: name, Count: count})
	}
	slices.SortFunc(answer.Tags, func(a, b TagCount) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	// Newest favourite first.
	slices.SortFunc(favourites, func(a, b favourite) int { return b.added.Compare(a.added) })
	for _, fav := range favourites {
		if item, ok := f.marked(fav.path); ok {
			answer.Favourites = append(answer.Favourites, Marked{Item: item})
		}
	}
	for _, u := range recent {
		if item, ok := f.marked(u.Path); ok {
			answer.Recent = append(answer.Recent, Marked{Item: item, Used: u.Used})
		}
	}
	writeJSON(w, http.StatusOK, answer)
}

func (f *Files) tagged(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	if _, ok := cleanTag(tag); !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Tag")
		return
	}
	m := &f.marks
	m.mu.Lock()
	var names []string
	for p, tags := range m.Tags {
		if slices.ContainsFunc(tags, func(t string) bool { return strings.EqualFold(t, tag) }) {
			names = append(names, p)
		}
	}
	m.mu.Unlock()
	slices.Sort(names)
	items := []Item{}
	for _, name := range names {
		if item, ok := f.marked(name); ok {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tag": tag, "items": items})
}

// markTarget reads the path of a request to tag or star: an existing file
// or folder. It answers the request itself and returns false when that fails.
func (f *Files) markTarget(w http.ResponseWriter, p string) (string, bool) {
	name, ok := uploadName(p)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return "", false
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		openError(w, err)
		return "", false
	}
	if !info.Mode().IsDir() && !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Ungültiger Pfad")
		return "", false
	}
	return name, true
}

func (f *Files) setTags(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string   `json:"path"`
		Tags []string `json:"tags"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	tags, message := cleanTags(body.Tags)
	if message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	name, ok := f.markTarget(w, body.Path)
	if !ok {
		return
	}
	m := &f.marks
	m.mu.Lock()
	if len(tags) == 0 {
		delete(m.Tags, name)
	} else {
		m.Tags[name] = tags
	}
	m.mu.Unlock()
	f.saveMarks()
	writeJSON(w, http.StatusOK, map[string]any{"path": "/" + name, "tags": tags})
}

func (f *Files) setFavourite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Favourite bool   `json:"favourite"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := f.markTarget(w, body.Path)
	if !ok {
		return
	}
	m := &f.marks
	m.mu.Lock()
	if _, was := m.Favourites[name]; body.Favourite && !was {
		m.Favourites[name] = time.Now().UTC()
	} else if !body.Favourite {
		delete(m.Favourites, name)
	}
	m.mu.Unlock()
	f.saveMarks()
	writeJSON(w, http.StatusOK, map[string]any{"path": "/" + name, "favourite": body.Favourite})
}

// cleanTags trims the tags and drops empty ones and repeats, ignoring case.
// It returns the reason when they cannot be used.
func cleanTags(raw []string) ([]string, string) {
	tags := []string{}
	for _, t := range raw {
		tag, ok := cleanTag(t)
		if !ok {
			if strings.TrimSpace(t) == "" {
				continue
			}
			return nil, "Ungültiger Tag „" + strings.ToValidUTF8(t, "") + "“: höchstens " + strconv.Itoa(MaxTagLength) + " Zeichen, ohne Komma"
		}
		if !slices.ContainsFunc(tags, func(have string) bool { return strings.EqualFold(have, tag) }) {
			tags = append(tags, tag)
		}
	}
	if len(tags) > MaxTags {
		return nil, "Höchstens " + strconv.Itoa(MaxTags) + " Tags je Datei"
	}
	return tags, ""
}

// cleanTag trims a tag; false when it is empty, too long, or has a comma
// (which separates tags on the page) or a control character.
func cleanTag(t string) (string, bool) {
	t = strings.TrimSpace(t)
	if t == "" || !utf8.ValidString(t) || utf8.RuneCountInString(t) > MaxTagLength {
		return "", false
	}
	if strings.ContainsFunc(t, func(r rune) bool { return r == ',' || unicode.IsControl(r) }) {
		return "", false
	}
	return t, true
}
