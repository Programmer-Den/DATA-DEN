package files

import (
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
)

// format is what the views know about a file extension.
type format struct {
	// kind is the view the file belongs to: photo, video, audio or document.
	kind string
	// inline is the Content-Type that /api/view serves the file with, so the
	// browser shows it; "" means the browser cannot show it and it is only
	// downloaded.
	inline string
}

const textPlain = "text/plain; charset=utf-8"

// formats by lowercase extension. Only what is listed here is ever served
// inline, never HTML or scripts.
var formats = map[string]format{
	".jpg":  {"photo", "image/jpeg"},
	".jpeg": {"photo", "image/jpeg"},
	".png":  {"photo", "image/png"},
	".gif":  {"photo", "image/gif"},
	".webp": {"photo", "image/webp"},
	".avif": {"photo", "image/avif"},
	".bmp":  {"photo", "image/bmp"},
	".svg":  {"photo", "image/svg+xml"},
	// Safari shows HEIC, the other browsers show the file's name instead.
	".heic": {"photo", "image/heic"},
	".heif": {"photo", "image/heif"},

	".mp4":  {"video", "video/mp4"},
	".m4v":  {"video", "video/mp4"},
	".webm": {"video", "video/webm"},
	".mov":  {"video", "video/quicktime"},
	".mkv":  {"video", "video/x-matroska"},
	".avi":  {"video", ""},
	".wmv":  {"video", ""},

	".mp3":  {"audio", "audio/mpeg"},
	".m4a":  {"audio", "audio/mp4"},
	".aac":  {"audio", "audio/aac"},
	".ogg":  {"audio", "audio/ogg"},
	".opus": {"audio", "audio/ogg"},
	".wav":  {"audio", "audio/wav"},
	".flac": {"audio", "audio/flac"},

	".pdf":  {"document", "application/pdf"},
	".txt":  {"document", textPlain},
	".md":   {"document", textPlain},
	".csv":  {"document", textPlain},
	".log":  {"document", textPlain},
	".rtf":  {"document", ""},
	".doc":  {"document", ""},
	".docx": {"document", ""},
	".odt":  {"document", ""},
	".xls":  {"document", ""},
	".xlsx": {"document", ""},
	".ods":  {"document", ""},
	".ppt":  {"document", ""},
	".pptx": {"document", ""},
	".odp":  {"document", ""},
	".epub": {"document", ""},
}

func formatOf(name string) format {
	return formats[lowerExt(name)]
}

// lowerExt is the extension of name in lowercase, such as ".jpg".
func lowerExt(name string) string {
	return strings.ToLower(path.Ext(name))
}

// Item is a file in a media listing, with its path from the top.
type Item struct {
	Path string `json:"path"`
	Entry
}

// Media is every file of one kind in the data directory.
type Media struct {
	Kind  string `json:"kind"`
	Items []Item `json:"items"`
}

func (f *Files) registerViews(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/view", require(f.view))
	mux.HandleFunc("GET /api/media", require(f.media))
	mux.HandleFunc("GET /api/thumb", require(f.thumb))
}

// view serves a file for the browser to show: a photo, a video with seeking
// (Range), a PDF in a frame of the website. Files the browser cannot show
// answer 415; they are downloaded instead.
func (f *Files) view(w http.ResponseWriter, r *http.Request) {
	name, ok := relative(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if hidden(name) {
		writeError(w, http.StatusNotFound, "Nicht gefunden")
		return
	}
	format := formatOf(name)
	if format.inline == "" {
		writeError(w, http.StatusUnsupportedMediaType, "Keine Vorschau möglich")
		return
	}
	file, err := f.root.Open(name)
	if err != nil {
		openError(w, err)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "Keine Datei")
		return
	}

	h := w.Header()
	h.Set("Content-Type", format.inline)
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": info.Name()}))
	h.Set("Cache-Control", "private, max-age=3600")
	if format.inline == "application/pdf" {
		// The document view shows PDFs in a frame of the website. Chrome
		// refuses to show a PDF in a sandbox, so it gets none.
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'self'")
	} else {
		// An SVG opened on its own could run scripts; in a sandbox it cannot.
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// media lists every photo, video, audio file or document in the data
// directory, newest first.
func (f *Files) media(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if !slices.Contains([]string{"photo", "video", "audio", "document"}, kind) {
		writeError(w, http.StatusBadRequest, "Unbekannte Art")
		return
	}
	items := []Item{}
	err := fs.WalkDir(f.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable folder is skipped, the rest is still listed.
			slog.Warn("listing media", "path", name, "err", err)
			return nil
		}
		if d.IsDir() {
			if hidden(name) {
				return fs.SkipDir
			}
			return nil
		}
		// WalkDir does not follow symlinks; they are left out like in the
		// folder view.
		if !d.Type().IsRegular() || formatOf(name).kind != kind {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		items = append(items, Item{
			Path:  "/" + name,
			Entry: entryOf(info),
		})
		return nil
	})
	if err != nil {
		slog.Error("listing media", "err", err)
		writeError(w, http.StatusInternalServerError, "Ordner nicht lesbar")
		return
	}
	slices.SortStableFunc(items, func(a, b Item) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	writeJSON(w, http.StatusOK, Media{Kind: kind, Items: items})
}

func entryOf(info fs.FileInfo) Entry {
	format := formatOf(info.Name())
	entry := Entry{Name: info.Name(), Dir: info.IsDir(), Modified: info.ModTime().UTC()}
	if !entry.Dir {
		entry.Size = info.Size()
		entry.Kind = format.kind
		entry.Preview = format.inline != ""
		entry.Thumb = thumbFormats[lowerExt(info.Name())]
	}
	return entry
}
