package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"time"
)

// The recycle bin: deleting moves a file or folder into trashDir, where it
// waits trashDays (DefaultTrashDays unless SetTrashDays) before it is gone
// for good.
//
//	GET  /api/trash                       -> {"items": [...]}, newest first
//	POST /api/trash/restore {"id": "..."} -> 200, back to where it was
//	POST /api/trash/delete  {"id": "..."} -> 200, gone for good
//	POST /api/trash/empty                 -> 200, all gone for good
//
// Each deleted item gets a folder trashDir/<id> with info.json and the item
// itself, renamed to "item".
const (
	trashDir = ".data-den-trash"
	// DefaultTrashDays is how long a deleted item can be restored, unless
	// SetTrashDays says otherwise.
	DefaultTrashDays = 30
	// MaxTrashDays is the longest waiting time SetTrashDays accepts.
	MaxTrashDays = 3650
)

var trashID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TrashItem is a deleted file or folder.
type TrashItem struct {
	ID string `json:"id"`
	// Path is where it was, such as "/Fotos/a.jpg".
	Path string `json:"path"`
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	// Size is the file's size, or the sum of a folder's files.
	Size    int64     `json:"size"`
	Deleted time.Time `json:"deleted"`
}

// SetTrashDays sets how many days, 1 to MaxTrashDays, a deleted item can be
// restored before it is deleted for good.
func (f *Files) SetTrashDays(days int) error {
	if days < 1 || days > MaxTrashDays {
		return fmt.Errorf("the recycle bin's waiting time must be 1 to %d days, not %d", MaxTrashDays, days)
	}
	f.trashDays = days
	return nil
}

// TrashDays is how many days a deleted item can be restored.
func (f *Files) TrashDays() int {
	return f.trashDays
}

// PurgeTrashEvery deletes for good what waited long enough in the recycle
// bin, every interval until ctx is done, so the bin is emptied on time even
// when nobody opens it.
func (f *Files) PurgeTrashEvery(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.purgeTrash()
		}
	}
}

func (f *Files) registerTrash(mux *http.ServeMux, require func(http.HandlerFunc) http.HandlerFunc) {
	f.purgeTrash()
	mux.HandleFunc("GET /api/trash", require(f.listTrash))
	mux.HandleFunc("POST /api/trash/restore", require(f.restore))
	mux.HandleFunc("POST /api/trash/delete", require(f.deleteForGood))
	mux.HandleFunc("POST /api/trash/empty", require(f.emptyTrash))
}

// toTrash moves name, a file or folder in the root, into the recycle bin
// and returns its id there.
func (f *Files) toTrash(name string, info fs.FileInfo) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	item := TrashItem{ID: id, Path: "/" + name, Name: info.Name(), Dir: info.IsDir(), Size: info.Size(), Deleted: time.Now().UTC()}
	if item.Dir {
		item.Size = f.folderSize(name)
	}
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	dir := path.Join(trashDir, id)
	if err := f.root.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if err := f.root.WriteFile(path.Join(dir, "info.json"), data, 0o640); err != nil {
		_ = f.root.RemoveAll(dir)
		return "", err
	}
	// A rename within the data directory: quick even for a large folder.
	if err := f.root.Rename(name, trashedName(id)); err != nil {
		_ = f.root.RemoveAll(dir)
		return "", err
	}
	f.sumMoved(name, trashedName(id))
	return id, nil
}

func (f *Files) folderSize(name string) int64 {
	var size int64
	_ = fs.WalkDir(f.root.FS(), name, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}

// trashItems reads the recycle bin, newest first. Folders without a readable
// info.json are left out.
func (f *Files) trashItems() []TrashItem {
	items := []TrashItem{}
	entries, err := fs.ReadDir(f.root.FS(), trashDir)
	if err != nil {
		return items
	}
	for _, e := range entries {
		if !e.IsDir() || !trashID.MatchString(e.Name()) {
			continue
		}
		data, err := f.root.ReadFile(path.Join(trashDir, e.Name(), "info.json"))
		if err != nil {
			continue
		}
		var item TrashItem
		if json.Unmarshal(data, &item) != nil || item.ID != e.Name() {
			continue
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b TrashItem) int { return b.Deleted.Compare(a.Deleted) })
	return items
}

// purgeTrash deletes for good what was deleted more than trashDays ago.
func (f *Files) purgeTrash() {
	limit := time.Now().AddDate(0, 0, -f.trashDays)
	for _, item := range f.trashItems() {
		if item.Deleted.Before(limit) {
			if err := f.root.RemoveAll(path.Join(trashDir, item.ID)); err != nil {
				slog.Warn("emptying the recycle bin", "path", item.Path, "err", err)
				continue
			}
			f.forgetUsage()
			f.sumsDropped(path.Join(trashDir, item.ID))
			slog.Info("deleted for good after the waiting time", "path", item.Path)
		}
	}
}

func (f *Files) listTrash(w http.ResponseWriter, _ *http.Request) {
	f.purgeTrash()
	writeJSON(w, http.StatusOK, map[string]any{"days": f.trashDays, "items": f.trashItems()})
}

// trashItem reads the item a request names by its id. It answers the
// request itself and returns false when that fails.
func (f *Files) trashItem(w http.ResponseWriter, r *http.Request) (TrashItem, bool) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return TrashItem{}, false
	}
	if !trashID.MatchString(body.ID) {
		writeError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return TrashItem{}, false
	}
	for _, item := range f.trashItems() {
		if item.ID == body.ID {
			return item, true
		}
	}
	writeError(w, http.StatusNotFound, "Nicht im Papierkorb")
	return TrashItem{}, false
}

func (f *Files) restore(w http.ResponseWriter, r *http.Request) {
	item, ok := f.trashItem(w, r)
	if !ok {
		return
	}
	name, ok := uploadName(item.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "Ungültiger Name")
		return
	}
	if _, err := f.root.Lstat(name); err == nil {
		writeError(w, http.StatusConflict, "Am alten Ort gibt es schon „"+item.Name+"“. Bitte erst umbenennen.")
		return
	}
	if dir := path.Dir(name); dir != "." {
		// The folder it was in may have been deleted since.
		if err := f.root.MkdirAll(dir, 0o750); err != nil {
			f.diskError(w, "restoring", err)
			return
		}
	}
	if err := f.rename(trashedName(item.ID), name, item.Dir); err != nil {
		if errors.Is(err, fs.ErrExist) {
			writeError(w, http.StatusConflict, "Am alten Ort gibt es schon „"+item.Name+"“. Bitte erst umbenennen.")
			return
		}
		f.diskError(w, "restoring", err)
		return
	}
	f.sumMoved(trashedName(item.ID), name)
	_ = f.root.RemoveAll(path.Join(trashDir, item.ID))
	slog.Info("restored", "path", item.Path)
	f.note(r, "restore", item.Path)
	writeJSON(w, http.StatusOK, map[string]string{"path": item.Path})
}

func (f *Files) deleteForGood(w http.ResponseWriter, r *http.Request) {
	item, ok := f.trashItem(w, r)
	if !ok {
		return
	}
	if err := f.root.RemoveAll(path.Join(trashDir, item.ID)); err != nil {
		f.diskError(w, "deleting for good", err)
		return
	}
	f.forgetUsage()
	f.sumsDropped(path.Join(trashDir, item.ID))
	slog.Info("deleted for good", "path", item.Path)
	f.note(r, "purge", item.Path)
	writeJSON(w, http.StatusOK, map[string]string{"path": item.Path})
}

func (f *Files) emptyTrash(w http.ResponseWriter, r *http.Request) {
	// Only from the page's own script: a JSON body, even if empty.
	var body struct{}
	if !readJSON(w, r, &body) {
		return
	}
	defer f.forgetUsage()
	for _, item := range f.trashItems() {
		if err := f.root.RemoveAll(path.Join(trashDir, item.ID)); err != nil {
			f.diskError(w, "emptying the recycle bin", err)
			return
		}
		f.sumsDropped(path.Join(trashDir, item.ID))
	}
	slog.Info("recycle bin emptied")
	f.note(r, "trash-empty", "")
	writeJSON(w, http.StatusOK, map[string]string{})
}
