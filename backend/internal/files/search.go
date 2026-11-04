package files

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Full-text search over the names and paths of all files and the text of
// documents:
//
//	GET /api/search?q=rechnung+2026 -> {"items": [...], "indexed": n, "ready": true}
//
// The index lives in memory. It is built at start and refreshed every
// indexInterval; only files whose size or modification time changed are
// read again.
const (
	indexInterval = 10 * time.Minute
	// maxIndexFile is the largest document whose text is read.
	maxIndexFile = 50 << 20
	// maxIndexText is how much text of one document is kept.
	maxIndexText = 256 << 10
	// maxResults is the most a search answers.
	maxResults = 200
	// snippetSize is the text around the first hit, in runes on each side.
	snippetSize = 60
)

// textFormats are the documents whose text is indexed, by extension.
var textFormats = map[string]func(io.ReaderAt, int64) (string, error){
	".txt":  plainText,
	".md":   plainText,
	".csv":  plainText,
	".log":  plainText,
	".pdf":  pdfText,
	".docx": zipXMLText("word/document.xml"),
	".odt":  zipXMLText("content.xml"),
}

type indexed struct {
	entry Entry
	path  string
	// name and lower are the name and the path in lowercase, for matching.
	name  string
	lower string
	// text is the document's text, "" for other files.
	text      string
	textLower string
}

type index struct {
	mu    sync.RWMutex
	files map[string]*indexed
	// ready is false until the first build is done.
	ready bool
}

func (f *Files) registerSearch(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/search", require(f.search))
}

// Index builds the search index and keeps it fresh until stop is closed.
func (f *Files) Index(stop <-chan struct{}) {
	for {
		start := time.Now()
		n := f.refreshIndex()
		slog.Info("search index refreshed", "files", n, "took", time.Since(start).Round(time.Millisecond))
		select {
		case <-stop:
			return
		case <-time.After(indexInterval):
		}
	}
}

// refreshIndex walks the data directory and returns how many files the
// index holds.
func (f *Files) refreshIndex() int {
	f.index.mu.RLock()
	old := f.index.files
	f.index.mu.RUnlock()

	files := make(map[string]*indexed, len(old))
	photos := map[string]bool{}
	_ = fs.WalkDir(f.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return nil
		}
		if hidden(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entry := entryOf(info)
		if entry.Kind == "photo" {
			// Reads the EXIF data of new photos ahead, so the timeline
			// answers quickly.
			f.photoMeta(name, info)
			photos[name] = true
		}
		if prev, ok := old[name]; ok && prev.entry.Size == entry.Size && prev.entry.Modified.Equal(entry.Modified) && prev.entry.Dir == entry.Dir {
			files[name] = prev
			return nil
		}
		item := &indexed{entry: entry, path: "/" + name, name: strings.ToLower(entry.Name), lower: strings.ToLower("/" + name)}
		if !entry.Dir {
			item.text = f.documentText(name, info.Size())
			item.textLower = strings.ToLower(item.text)
		}
		files[name] = item
		return nil
	})

	f.prunePhotos(photos)

	f.index.mu.Lock()
	f.index.files = files
	f.index.ready = true
	f.index.mu.Unlock()
	return len(files)
}

// documentText reads the text of a document, "" if it has none or cannot be
// read.
func (f *Files) documentText(name string, size int64) string {
	extract, ok := textFormats[lowerExt(name)]
	if !ok || size > maxIndexFile {
		return ""
	}
	file, err := f.root.Open(name)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	text, err := safeExtract(extract, file, size)
	if err != nil {
		slog.Debug("reading a document for the search", "path", name, "err", err)
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxIndexText {
		text = text[:maxIndexText]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

// safeExtract runs extract, turning a panic of a parser on a broken file
// into an error.
func safeExtract(extract func(io.ReaderAt, int64) (string, error), r io.ReaderAt, size int64) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("broken file: %v", p)
		}
	}()
	return extract(r, size)
}

func plainText(r io.ReaderAt, size int64) (string, error) {
	b, err := io.ReadAll(io.NewSectionReader(r, 0, min(size, maxIndexText)))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		// Most likely Latin-1 or Windows-1252: those bytes are the runes.
		runes := make([]rune, len(b))
		for i, c := range b {
			runes[i] = rune(c)
		}
		return string(runes), nil
	}
	return string(b), nil
}

func pdfText(r io.ReaderAt, size int64) (string, error) {
	doc, err := pdf.NewReader(r, size)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for i := 1; i <= doc.NumPage() && text.Len() < maxIndexText; i++ {
		page := doc.Page(i)
		if page.V.IsNull() {
			continue
		}
		s, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		text.WriteString(s)
		text.WriteByte(' ')
	}
	return text.String(), nil
}

// zipXMLText reads the text of one XML file in a zip, as in Word (.docx)
// and OpenDocument (.odt) files.
func zipXMLText(member string) func(io.ReaderAt, int64) (string, error) {
	return func(r io.ReaderAt, size int64) (string, error) {
		z, err := zip.NewReader(r, size)
		if err != nil {
			return "", err
		}
		for _, file := range z.File {
			if file.Name != member {
				continue
			}
			rc, err := file.Open()
			if err != nil {
				return "", err
			}
			defer func() { _ = rc.Close() }()
			return xmlText(io.LimitReader(rc, 16*maxIndexText))
		}
		return "", errors.New("no " + member)
	}
}

// xmlText is the character data of an XML document, with a space after each
// paragraph so words do not run together.
func xmlText(r io.Reader) (string, error) {
	var text bytes.Buffer
	dec := xml.NewDecoder(r)
	for text.Len() < maxIndexText {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// What was read so far is still worth indexing.
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if t.Name.Local == "p" || t.Name.Local == "tab" || t.Name.Local == "br" {
				text.WriteByte(' ')
			}
		}
	}
	return text.String(), nil
}

// Hit is a search result.
type Hit struct {
	Item
	// Snippet is the text around the first word found in the document's
	// text, "" when only the name or path matched.
	Snippet string `json:"snippet,omitempty"`
}

func (f *Files) search(w http.ResponseWriter, r *http.Request) {
	words := strings.Fields(strings.ToLower(r.URL.Query().Get("q")))
	if len(words) == 0 || len(words) > 10 {
		writeError(w, http.StatusBadRequest, "Suchbegriff fehlt")
		return
	}
	f.index.mu.RLock()
	defer f.index.mu.RUnlock()

	type scored struct {
		hit   Hit
		score int
	}
	var found []scored
	for _, item := range f.index.files {
		score := 0
		for _, word := range words {
			switch {
			case strings.Contains(item.name, word):
				score += 3
			case strings.Contains(item.lower, word):
				score += 2
			case strings.Contains(item.textLower, word):
				score++
			default:
				score = -1
			}
			if score < 0 {
				break
			}
		}
		if score < 0 {
			continue
		}
		hit := Hit{Item: Item{Path: item.path, Entry: item.entry}}
		for _, word := range words {
			if i := strings.Index(item.textLower, word); i >= 0 {
				hit.Snippet = snippet(item.text, item.textLower, i, len(word))
				break
			}
		}
		found = append(found, scored{hit, score})
	}
	// Best first, then newest.
	slices.SortFunc(found, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if c := b.hit.Modified.Compare(a.hit.Modified); c != 0 {
			return c
		}
		return strings.Compare(a.hit.Path, b.hit.Path)
	})
	hits := make([]Hit, 0, min(len(found), maxResults))
	for _, s := range found[:min(len(found), maxResults)] {
		hits = append(hits, s.hit)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   hits,
		"total":   len(found),
		"indexed": len(f.index.files),
		"ready":   f.index.ready,
	})
}

// snippet cuts the text around a hit at byte offset i of length n in lower,
// the lowercase text. Lowercasing can change byte lengths, so the cut is
// made by runes in both.
func snippet(text, lower string, i, n int) string {
	runesBefore := utf8.RuneCountInString(lower[:i])
	runes := []rune(text)
	lowerRunes := []rune(lower)
	if len(runes) != len(lowerRunes) {
		// Lowercasing changed the number of runes; start at the beginning.
		runesBefore = 0
	}
	start := max(0, runesBefore-snippetSize)
	end := min(len(runes), runesBefore+utf8.RuneCountInString(lower[i:i+n])+snippetSize)
	s := string(runes[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}
