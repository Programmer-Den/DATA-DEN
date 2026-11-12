package files

import (
	"io/fs"
	"net/http"
	"sync"
	"time"
)

// usageTTL is how long a counted usage is trusted. Uploads add to it at once,
// so only changes from outside the app wait this long.
const usageTTL = time.Minute

type usedCache struct {
	mu    sync.Mutex
	bytes int64
	at    time.Time
}

// usage is how many bytes the space holds, recycle bin included. Thumbnails,
// unfinished uploads, the share links and the spaces of other accounts do
// not count.
func (f *Files) usage() (int64, error) {
	f.used.mu.Lock()
	defer f.used.mu.Unlock()
	if !f.used.at.IsZero() && time.Since(f.used.at) < usageTTL {
		return f.used.bytes, nil
	}
	var total int64
	err := fs.WalkDir(f.root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			// A folder that cannot be read is skipped, not the whole count.
			if d != nil && d.IsDir() && name != "." {
				return fs.SkipDir
			}
			return err
		}
		if d.IsDir() && (name == usersDir || name == thumbDir || name == uploadDir || name == sharesDir || name == editDir || name == ocrDir) {
			return fs.SkipDir
		}
		if d.Type().IsRegular() && name != grantsFile {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	f.used.bytes, f.used.at = total, time.Now()
	return total, nil
}

// addUsage counts n more bytes (negative for fewer), if usage was counted.
func (f *Files) addUsage(n int64) {
	f.used.mu.Lock()
	defer f.used.mu.Unlock()
	f.used.bytes = max(f.used.bytes+n, 0)
}

// forgetUsage makes the next usage count again, after files were deleted
// for good.
func (f *Files) forgetUsage() {
	f.used.mu.Lock()
	defer f.used.mu.Unlock()
	f.used.at = time.Time{}
}

// pending is how many bytes the unfinished uploads will add.
func (f *Files) pending() int64 {
	f.uploads.mu.Lock()
	defer f.uploads.mu.Unlock()
	var total int64
	for _, u := range f.uploads.byID {
		total += u.size
	}
	return total
}

// fits reports whether size more bytes fit into the quota.
func (f *Files) fits(size int64) (bool, error) {
	quota := f.quota.Load()
	if quota <= 0 {
		return true, nil
	}
	used, err := f.usage()
	if err != nil {
		return false, err
	}
	return used+f.pending()+size <= quota, nil
}

// showUsage answers GET /api/usage: {"used": n, "quota": n}, quota 0 for no
// limit.
func (f *Files) showUsage(w http.ResponseWriter, _ *http.Request) {
	used, err := f.usage()
	if err != nil {
		f.diskError(w, "counting the usage", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"used": used, "quota": f.quota.Load()})
}
