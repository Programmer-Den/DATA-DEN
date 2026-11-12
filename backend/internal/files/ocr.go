package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The search reads the text of scans, photos of documents and the office
// formats it cannot read itself with a Recognizer: Apache Tika with
// Tesseract, a container next to DATA-DEN (deploy/compose.ocr.yaml). Nothing
// leaves the VM.
//
// Recognizing takes seconds per file, so it runs in the background, one file
// after the other, after each refresh of the index. The text is kept in
// ocrDir, named after the file's path, size and modification time like the
// thumbnails, so it is read only once and survives a restart.
const (
	ocrDir = ".data-den-ocr"
	// minPDFText is how much text a PDF needs, so it is not taken for a
	// scan.
	minPDFText = 100
	// ocrTimeout is the longest one file may take.
	ocrTimeout = 5 * time.Minute
)

// ocrFormats are the files given to the Recognizer, by extension: images,
// and office formats the search cannot read itself. PDFs only when they hold
// almost no text of their own.
var ocrFormats = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true, ".bmp": true, ".gif": true,
	".doc": true, ".rtf": true, ".xls": true, ".xlsx": true, ".ods": true, ".ppt": true, ".pptx": true, ".odp": true,
}

// ocrImages are the ocrFormats whose text is guessed from pixels.
var ocrImages = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true, ".bmp": true, ".gif": true}

// Recognizer reads the text of a file, by text recognition where it is an
// image or a scan.
type Recognizer interface {
	// Text reads the text of the file name of size bytes from r. It returns
	// ErrRecognizerDown when the Recognizer cannot be reached; the file is
	// then tried again later.
	Text(ctx context.Context, name string, r io.Reader, size int64) (string, error)
}

// ErrRecognizerDown means the Recognizer is not reachable now.
var ErrRecognizerDown = errors.New("text recognition not reachable")

// Tika is a Recognizer: Apache Tika's server.
type Tika struct {
	url    *url.URL
	client *http.Client
	// slot lets one file at a time through, from all spaces together.
	slot chan struct{}
}

// NewTika returns the Recognizer for Tika's server at u, such as
// http://127.0.0.1:9998.
func NewTika(u *url.URL) *Tika {
	return &Tika{url: u, client: &http.Client{Timeout: ocrTimeout}, slot: make(chan struct{}, 1)}
}

// Text sends the file to Tika and returns its plain text.
func (t *Tika) Text(ctx context.Context, name string, r io.Reader, size int64) (string, error) {
	select {
	case t.slot <- struct{}{}:
		defer func() { <-t.slot }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, t.url.JoinPath("tika").String(), r)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Accept", "text/plain; charset=UTF-8")
	// The name helps Tika tell the format.
	req.Header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
	resp, err := t.client.Do(req)
	if err != nil {
		var dial *net.OpError
		if errors.As(err, &dial) && dial.Op == "dial" {
			return "", fmt.Errorf("%w: %w", ErrRecognizerDown, err)
		}
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4*maxIndexText))
		return string(b), err
	case http.StatusNoContent:
		return "", nil
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "", fmt.Errorf("%w: status %d", ErrRecognizerDown, resp.StatusCode)
	default:
		return "", fmt.Errorf("tika: status %d", resp.StatusCode)
	}
}

// needsOCR is true when the file name with the text read so far goes to the
// Recognizer.
func needsOCR(name, text string, size int64) bool {
	if size == 0 || size > maxIndexFile {
		return false
	}
	ext := lowerExt(name)
	return ocrFormats[ext] || (ext == ".pdf" && len(text) < minPDFText)
}

// ocrKey names the kept text of a file: its path, size and modification time.
func ocrKey(name string, info fs.FileInfo) string {
	sum := sha256.Sum256([]byte(name + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano)))
	key := hex.EncodeToString(sum[:16])
	return path.Join(ocrDir, key[:2], key+".txt")
}

// recognized is the kept text of a file, false when it was not read yet.
func (f *Files) recognized(key string) (string, bool) {
	b, err := f.root.ReadFile(key)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Recognize reads the text of the files waiting for it, each time the index
// was refreshed, until ctx is done. Without a Recognizer it does nothing.
func (f *Files) Recognize(ctx context.Context) {
	if f.ocr == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.index.wake:
		}
		start := time.Now()
		done, err := f.recognizeWaiting(ctx)
		if errors.Is(err, ErrRecognizerDown) {
			slog.Warn("text recognition not reachable, trying again at the next refresh of the search index", "err", err)
		}
		if done > 0 {
			slog.Info("text recognition done", "files", done, "took", time.Since(start).Round(time.Second))
		}
	}
}

// recognizeWaiting reads the text of the files waiting as of the last
// refresh and returns how many it read. It stops when ctx is done or the
// Recognizer is down.
func (f *Files) recognizeWaiting(ctx context.Context) (int, error) {
	f.index.mu.RLock()
	waiting := f.index.waiting
	f.index.mu.RUnlock()
	done := 0
	for _, name := range waiting {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		read, err := f.recognize(ctx, name)
		if errors.Is(err, ErrRecognizerDown) {
			return done, err
		}
		if err != nil {
			slog.Debug("text recognition", "path", name, "err", err)
		}
		if read {
			done++
		}
	}
	return done, nil
}

// recognize reads the text of one file, keeps it and puts it into the index.
// A file that cannot be read gets an empty text, so it is not tried again.
// read is false when the file no longer waits.
func (f *Files) recognize(ctx context.Context, name string) (read bool, err error) {
	f.index.mu.RLock()
	item := f.index.files[name]
	f.index.mu.RUnlock()
	if item == nil || !item.ocr {
		return false, nil
	}
	file, err := f.root.Open(name)
	if err != nil {
		f.recognizedAs(item, "")
		return false, nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() != item.entry.Size || !info.ModTime().Equal(item.entry.Modified) {
		// Changed meanwhile: the next refresh queues it again.
		return false, nil
	}
	one, cancel := context.WithTimeout(ctx, ocrTimeout)
	defer cancel()
	text, err := f.ocr.Text(one, name, file, info.Size())
	if errors.Is(err, ErrRecognizerDown) {
		return false, err
	}
	if ctx.Err() != nil {
		// Stopped, not a file that cannot be read.
		return false, ctx.Err()
	}
	if err == nil {
		text = cleanText(text)
		if ocrImages[lowerExt(name)] {
			text = dropNoise(text)
		}
	}
	key := ocrKey(name, info)
	if saveErr := f.saveRecognized(key, text); saveErr != nil {
		slog.Error("keeping the recognized text", "path", name, "err", saveErr)
	}
	f.recognizedAs(item, text)
	return true, err
}

// recognizedAs puts the text of item into the index; "" keeps what the
// search read itself.
func (f *Files) recognizedAs(item *indexed, text string) {
	f.index.mu.Lock()
	defer f.index.mu.Unlock()
	if item.ocr {
		item.ocr = false
		f.index.left--
	}
	if text != "" {
		item.text = text
		item.textLower = strings.ToLower(text)
	}
}

func (f *Files) saveRecognized(key, text string) error {
	if err := f.root.MkdirAll(path.Dir(key), 0o750); err != nil {
		return err
	}
	tmp := key + ".tmp"
	if err := f.root.WriteFile(tmp, []byte(text), 0o640); err != nil {
		return err
	}
	return f.root.Rename(tmp, key)
}

// pruneRecognized removes the kept texts of files that are gone or changed.
func (f *Files) pruneRecognized(keep map[string]bool) {
	_ = fs.WalkDir(f.root.FS(), ocrDir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !keep[name] {
			_ = f.root.Remove(name)
		}
		return nil
	})
}

// dropNoise drops what text recognition makes of the textures in a photo:
// words without two letters or digits.
func dropNoise(text string) string {
	words := strings.Fields(text)
	kept := words[:0]
	for _, w := range words {
		n := 0
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
			}
		}
		if n >= 2 {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}
