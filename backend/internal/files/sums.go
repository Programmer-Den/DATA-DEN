package files

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// Checksums: every file gets a SHA-256, kept in sumsFile. It finds
// duplicates and shows when a file was damaged on the disk:
//
//	GET  /api/duplicates                       -> {"groups": [...], "wasted": n}
//	GET  /api/integrity                        -> the state of the checks and the damaged files
//	POST /api/integrity/check {}               -> 202, reads every file again now
//	POST /api/integrity/accept {"path": "..."} -> 200, the file's content counts as correct again
//
// An upload's checksum is taken from the bytes as they arrive. Every
// checkInterval the data directory is walked: new and changed files (other
// size or time) get a checksum, and files not read for VerifyDays are read
// again. A file whose content changed while its size and time did not is
// damaged; the RAID 1 mirror keeps both disks equal, but not correct.
const (
	sumsDir  = ".data-den-sums"
	sumsFile = sumsDir + "/index.json"
	// VerifyDays is how often every file is read again and compared.
	VerifyDays    = 30
	checkInterval = time.Hour
	// saveInterval is how often a long check saves what it has, so a
	// restart does not lose hours of reading.
	saveInterval = 5 * time.Minute
)

// fileSum is the checksum of one file.
type fileSum struct {
	Size int64 `json:"size"`
	// Mod is the modification time in nanoseconds since 1970.
	Mod    int64  `json:"mod"`
	SHA256 string `json:"sha256"`
	// Checked is when the content was last read.
	Checked time.Time `json:"checked"`
	// Bad is the checksum the file has now when it is damaged, and Found
	// when that was noticed.
	Bad   string    `json:"bad,omitempty"`
	Found time.Time `json:"found,omitzero"`
}

// sums holds the checksums of the files, by path within the root.
type sums struct {
	mu     sync.Mutex
	byPath map[string]*fileSum
	// Progress of the running check.
	running     bool
	done, total int
	lastCheck   time.Time
	// kick starts a check at once; true reads every file again.
	kick chan bool
	// saveMu keeps two saves from writing the file at the same time.
	saveMu sync.Mutex
	// checker is the running StartChecks, which Close waits for.
	checker sync.WaitGroup
}

// Duplicate is a group of files with the same content.
type Duplicate struct {
	SHA256 string   `json:"sha256"`
	Size   int64    `json:"size"`
	Paths  []string `json:"paths"`
}

// Damaged is a file whose content no longer matches its checksum.
type Damaged struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Expected string    `json:"expected"`
	Actual   string    `json:"actual"`
	Found    time.Time `json:"found"`
}

// Integrity is the state of the checks.
type Integrity struct {
	// Files is how many files have a checksum.
	Files      int       `json:"files"`
	Running    bool      `json:"running"`
	Done       int       `json:"done"`
	Total      int       `json:"total"`
	LastCheck  time.Time `json:"lastCheck,omitzero"`
	VerifyDays int       `json:"verifyDays"`
	Damaged    []Damaged `json:"damaged"`
}

func (f *Files) registerSums(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/duplicates", require(f.duplicates))
	mux.HandleFunc("GET /api/integrity", require(f.integrity))
	mux.HandleFunc("POST /api/integrity/check", require(f.checkNow))
	mux.HandleFunc("POST /api/integrity/accept", require(f.accept))
}

// loadSums reads sumsFile. A missing or unreadable one starts empty: the
// next check makes the checksums again.
func (f *Files) loadSums() {
	f.sums.byPath = map[string]*fileSum{}
	f.sums.kick = make(chan bool, 1)
	data, err := f.root.ReadFile(sumsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(data, &f.sums.byPath)
	}
	if err != nil {
		slog.Warn("the checksums cannot be read, so they are made again", "err", err)
		f.sums.byPath = map[string]*fileSum{}
	}
	if f.sums.byPath == nil {
		f.sums.byPath = map[string]*fileSum{}
	}
}

// saveSums writes sumsFile, through a temporary file so a crash never
// leaves half of it.
func (f *Files) saveSums() {
	f.sums.saveMu.Lock()
	defer f.sums.saveMu.Unlock()
	f.sums.mu.Lock()
	data, err := json.Marshal(f.sums.byPath)
	f.sums.mu.Unlock()
	if err == nil {
		err = f.root.MkdirAll(sumsDir, 0o750)
	}
	if err == nil {
		err = f.root.WriteFile(sumsFile+".tmp", data, 0o640)
	}
	if err == nil {
		err = f.root.Rename(sumsFile+".tmp", sumsFile)
	}
	if err != nil {
		slog.Error("saving the checksums", "err", err)
	}
}

// StartChecks runs the checks: at once, every checkInterval and when asked
// to, until ctx is done. Close waits for them, so cancel ctx before.
func (f *Files) StartChecks(ctx context.Context) {
	f.sums.checker.Add(1)
	go func() {
		defer f.sums.checker.Done()
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		all := false
		for {
			f.check(ctx, all)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				all = false
			case all = <-f.sums.kick:
			}
		}
	}()
}

// sumMoved follows a rename of a file or folder from one path to another.
func (f *Files) sumMoved(from, to string) {
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	for p, s := range f.sums.byPath {
		if p == from {
			delete(f.sums.byPath, p)
			f.sums.byPath[to] = s
		} else if rest, ok := strings.CutPrefix(p, from+"/"); ok {
			delete(f.sums.byPath, p)
			f.sums.byPath[to+"/"+rest] = s
		}
	}
}

// sumsDropped forgets a file or folder that is gone for good.
func (f *Files) sumsDropped(name string) {
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	for p := range f.sums.byPath {
		if p == name || strings.HasPrefix(p, name+"/") {
			delete(f.sums.byPath, p)
		}
	}
}

// uploaded keeps the checksum of a finished upload and returns the other
// files with the same content.
func (f *Files) uploaded(name string, info fs.FileInfo, sum string) []string {
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	f.sums.byPath[name] = &fileSum{Size: info.Size(), Mod: info.ModTime().UnixNano(), SHA256: sum, Checked: time.Now().UTC()}
	others := []string{}
	if info.Size() == 0 {
		return others
	}
	for p, s := range f.sums.byPath {
		if p != name && s.SHA256 == sum && !hidden(p) {
			others = append(others, "/"+p)
		}
	}
	slices.Sort(others)
	return others
}

// hashingWriter writes to a file and hashes what was written.
type hashingWriter struct {
	w io.Writer
	h hash.Hash
}

func (hw hashingWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	hw.h.Write(p[:n])
	return n, err
}

var errChanged = errors.New("changed while it was read")

// readSlot lets the checks of all spaces read one file at a time, so
// several accounts do not make the disks seek between their files.
var readSlot = make(chan struct{}, 1)

// hashFile reads a file and returns its checksum, with the size and time it
// had. It fails when the file changes meanwhile.
func (f *Files) hashFile(ctx context.Context, name string) (string, fs.FileInfo, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !before.Mode().IsRegular() {
		return "", nil, errors.New("not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, file}); err != nil {
		return "", nil, err
	}
	after, err := f.root.Stat(name)
	if err != nil {
		return "", nil, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", nil, errChanged
	}
	return hex.EncodeToString(h.Sum(nil)), before, nil
}

// ctxReader stops reading a large file when ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

type found struct {
	name string
	size int64
	mod  int64
}

// check walks the data directory: it makes checksums of new and changed
// files, forgets those that are gone, and reads again those that were not
// read for VerifyDays, or all of them.
func (f *Files) check(ctx context.Context, all bool) {
	var files []found
	err := fs.WalkDir(f.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("checking files: a folder cannot be read", "path", p, "err", err)
			return nil
		}
		if d.IsDir() && p != "." && hidden(p) {
			// The server's own folders. The recycle bin keeps the checksums
			// its items had, for when they are restored.
			return fs.SkipDir
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				files = append(files, found{p, info.Size(), info.ModTime().UnixNano()})
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return
	}

	seen := make(map[string]bool, len(files))
	for _, file := range files {
		seen[file.name] = true
	}
	limit := time.Now().AddDate(0, 0, -VerifyDays)
	var todo []found
	f.sums.mu.Lock()
	for p := range f.sums.byPath {
		if !seen[p] && !hidden(p) {
			if _, err := f.root.Lstat(p); errors.Is(err, fs.ErrNotExist) {
				delete(f.sums.byPath, p)
			}
		}
	}
	for _, file := range files {
		s := f.sums.byPath[file.name]
		if all || s == nil || s.Size != file.size || s.Mod != file.mod || s.Checked.Before(limit) {
			todo = append(todo, file)
		}
	}
	f.sums.running, f.sums.done, f.sums.total = true, 0, len(todo)
	f.sums.mu.Unlock()
	defer func() {
		f.sums.mu.Lock()
		f.sums.running = false
		if ctx.Err() == nil {
			f.sums.lastCheck = time.Now().UTC()
		}
		f.sums.mu.Unlock()
		f.saveSums()
	}()

	// The files found damaged in this check, told about at its end.
	var damaged []string
	defer func() {
		if len(damaged) > 0 && f.damaged != nil {
			f.damaged(f.account, damaged)
		}
	}()
	saved := time.Now()
	for _, file := range todo {
		select {
		case readSlot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		sum, info, err := f.hashFile(ctx, file.name)
		<-readSlot
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if f.record(file.name, sum, info) {
				damaged = append(damaged, "/"+file.name)
			}
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errChanged) {
			slog.Error("checking a file: it cannot be read", "path", "/"+file.name, "err", err)
		}
		f.sums.mu.Lock()
		f.sums.done++
		f.sums.mu.Unlock()
		if time.Since(saved) > saveInterval {
			f.saveSums()
			saved = time.Now()
		}
	}
}

// record compares what was just read with the checksum kept for the file.
// It returns true when it found the file damaged for the first time.
func (f *Files) record(name, sum string, info fs.FileInfo) (newly bool) {
	f.sums.mu.Lock()
	defer f.sums.mu.Unlock()
	now := time.Now().UTC()
	size, mod := info.Size(), info.ModTime().UnixNano()
	s := f.sums.byPath[name]
	switch {
	case s == nil || s.Size != size || s.Mod != mod:
		// New or changed on purpose: the content now is the right one.
		f.sums.byPath[name] = &fileSum{Size: size, Mod: mod, SHA256: sum, Checked: now}
	case s.SHA256 == sum:
		if s.Bad != "" {
			slog.Info("a damaged file is correct again", "path", "/"+name)
		}
		s.Checked, s.Bad, s.Found = now, "", time.Time{}
	default:
		if s.Bad != sum {
			slog.Error("file damaged: its content changed although its size and time did not", "path", "/"+name, "expected", s.SHA256, "actual", sum)
			s.Found = now
			newly = true
		}
		s.Checked, s.Bad = now, sum
	}
	return newly
}

func (f *Files) duplicates(w http.ResponseWriter, _ *http.Request) {
	f.sums.mu.Lock()
	bySum := map[string]*Duplicate{}
	for p, s := range f.sums.byPath {
		if s.Size == 0 || hidden(p) {
			continue
		}
		d := bySum[s.SHA256]
		if d == nil {
			d = &Duplicate{SHA256: s.SHA256, Size: s.Size}
			bySum[s.SHA256] = d
		}
		d.Paths = append(d.Paths, "/"+p)
	}
	f.sums.mu.Unlock()

	groups := []Duplicate{}
	var wasted int64
	for _, d := range bySum {
		if len(d.Paths) > 1 {
			slices.Sort(d.Paths)
			groups = append(groups, *d)
			wasted += d.Size * int64(len(d.Paths)-1)
		}
	}
	// Most space to gain first.
	slices.SortFunc(groups, func(a, b Duplicate) int {
		return cmp.Or(
			cmp.Compare(b.Size*int64(len(b.Paths)-1), a.Size*int64(len(a.Paths)-1)),
			strings.Compare(a.Paths[0], b.Paths[0]),
		)
	})
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "wasted": wasted})
}

func (f *Files) integrity(w http.ResponseWriter, _ *http.Request) {
	f.sums.mu.Lock()
	state := Integrity{
		Running: f.sums.running, Done: f.sums.done, Total: f.sums.total,
		LastCheck: f.sums.lastCheck, VerifyDays: VerifyDays, Damaged: []Damaged{},
	}
	for p, s := range f.sums.byPath {
		if hidden(p) {
			continue
		}
		state.Files++
		if s.Bad != "" {
			state.Damaged = append(state.Damaged, Damaged{Path: "/" + p, Size: s.Size, Expected: s.SHA256, Actual: s.Bad, Found: s.Found})
		}
	}
	f.sums.mu.Unlock()
	slices.SortFunc(state.Damaged, func(a, b Damaged) int { return strings.Compare(a.Path, b.Path) })
	writeJSON(w, http.StatusOK, state)
}

func (f *Files) checkNow(w http.ResponseWriter, r *http.Request) {
	// Only from the page's own script: a JSON body, even if empty.
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	select {
	case f.sums.kick <- true:
	default:
		// A check is already asked for.
	}
	slog.Info("check of all files asked for")
	writeJSON(w, http.StatusAccepted, map[string]string{})
}

// accept takes a damaged file's content as correct, for example after
// looking at the photo or putting it back from a copy.
func (f *Files) accept(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, ok := uploadName(body.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	f.sums.mu.Lock()
	s := f.sums.byPath[name]
	if s == nil || s.Bad == "" {
		f.sums.mu.Unlock()
		writeError(w, http.StatusNotFound, "Keine beschädigte Datei")
		return
	}
	s.SHA256, s.Bad, s.Found = s.Bad, "", time.Time{}
	f.sums.mu.Unlock()
	f.saveSums()
	slog.Info("damaged file accepted as it is", "path", "/"+name)
	f.note(r, "integrity-accept", "/"+name)
	writeJSON(w, http.StatusOK, map[string]string{"path": "/" + name})
}

// trashedName is where the recycle bin keeps a deleted item.
func trashedName(id string) string {
	return path.Join(trashDir, id, "item")
}
