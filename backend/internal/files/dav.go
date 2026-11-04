package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"golang.org/x/net/webdav"
)

// WebDAV under DavPrefix, so Windows (and Finder, Nautilus, rclone and the
// like) can mount a space as a network drive. It goes through the same
// rules as the web interface: the server's own folders stay hidden, deleting
// moves into the recycle bin, overwriting keeps the old file as a version,
// and new files count against the quota and get a checksum.

// DavPrefix is where WebDAV is served.
const DavPrefix = "/dav"

// errQuota is returned by a write that does not fit into the quota.
var errQuota = errors.New("the quota is full")

type requestKey struct{}

// davHandler serves WebDAV for this space, with its own locks.
func (f *Files) davHandler() *webdav.Handler {
	f.davOnce.Do(func() {
		f.dav = &webdav.Handler{
			Prefix:     DavPrefix,
			FileSystem: davFS{f},
			LockSystem: webdav.NewMemLS(),
			Logger: func(r *http.Request, err error) {
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					slog.Warn("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
				}
			},
		}
	})
	return f.dav
}

// ServeDAV answers a WebDAV request for this space; the login was checked
// before.
func (f *Files) ServeDAV(w http.ResponseWriter, r *http.Request) {
	f.davHandler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
}

// davFS is a space as a webdav.FileSystem.
type davFS struct{ f *Files }

// davName turns a WebDAV path into a name within the root, refusing the
// server's own folders.
func davName(name string) (string, error) {
	if strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return "", fs.ErrInvalid
	}
	clean := strings.TrimPrefix(path.Clean("/"+name), "/")
	if clean == "" {
		return ".", nil
	}
	if hidden(clean) {
		return "", fs.ErrNotExist
	}
	return clean, nil
}

// note records action on target in the audit log, for the request in ctx.
func (d davFS) note(ctx context.Context, action, target string) {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		d.f.note(r, action, target)
	}
}

func (d davFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	name, err := davName(name)
	if err != nil {
		return err
	}
	if name == "." {
		return fs.ErrExist
	}
	if err := d.f.root.Mkdir(name, 0o750); err != nil {
		return err
	}
	slog.Info("folder created", "path", "/"+name, "via", "webdav")
	d.note(ctx, "folder", "/"+name)
	return nil
}

func (d davFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	name, err := davName(name)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) == 0 {
		file, err := d.f.root.Open(name)
		if err != nil {
			return nil, err
		}
		if name == "." {
			return topFolder{file}, nil
		}
		return file, nil
	}
	return d.create(ctx, name, flag)
}

// create starts writing the file name. The content goes into the upload
// folder first and takes the file's place when it is closed, so a broken
// upload leaves the old file as it was.
func (d davFS) create(ctx context.Context, name string, flag int) (webdav.File, error) {
	if name == "." || flag&os.O_APPEND != 0 {
		return nil, fs.ErrPermission
	}
	info, err := d.f.root.Lstat(name)
	exists := err == nil
	switch {
	case exists && flag&os.O_EXCL != 0:
		return nil, fs.ErrExist
	case exists && !info.Mode().IsRegular():
		return nil, fs.ErrExist
	case !exists && !errors.Is(err, fs.ErrNotExist):
		return nil, err
	case !exists && flag&os.O_CREATE == 0:
		return nil, fs.ErrNotExist
	}
	if dir, err := d.f.root.Stat(path.Dir(name)); err != nil || !dir.IsDir() {
		return nil, fs.ErrNotExist
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if err := d.f.root.MkdirAll(uploadDir, 0o750); err != nil {
		return nil, err
	}
	tmp := path.Join(uploadDir, id)
	file, err := d.f.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	return &davUpload{file: file, d: d, ctx: ctx, name: name, tmp: tmp, replace: exists, hash: sha256.New()}, nil
}

func (d davFS) RemoveAll(ctx context.Context, name string) error {
	name, err := davName(name)
	if err != nil {
		return err
	}
	if name == "." {
		return fs.ErrPermission
	}
	info, err := d.f.root.Lstat(name)
	if err != nil {
		return err
	}
	if _, err := d.f.toTrash(name, info); err != nil {
		return err
	}
	slog.Info("moved to the recycle bin", "path", "/"+name, "via", "webdav")
	d.note(ctx, "delete", "/"+name)
	return nil
}

// Rename moves a file or folder. WebDAV has removed anything at newName
// before, when the client asked to overwrite it.
func (d davFS) Rename(ctx context.Context, oldName, newName string) error {
	from, err := davName(oldName)
	if err != nil {
		return err
	}
	to, err := davName(newName)
	if err != nil {
		return err
	}
	if from == "." || to == "." || strings.HasPrefix(to, from+"/") {
		return fs.ErrPermission
	}
	info, err := d.f.root.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsDir() && !info.Mode().IsRegular() {
		return fs.ErrInvalid
	}
	if err := d.f.rename(from, to, info.IsDir()); err != nil {
		return err
	}
	d.f.moveShares(from, to)
	d.f.sumMoved(from, to)
	d.f.versionsMoved(from, to)
	slog.Info("moved", "from", "/"+from, "to", "/"+to, "via", "webdav")
	d.note(ctx, "move", "/"+from+" → /"+to)
	return nil
}

func (d davFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	name, err := davName(name)
	if err != nil {
		return nil, err
	}
	return d.f.root.Stat(name)
}

// topFolder is the top of a space, without the server's own folders.
type topFolder struct{ *os.File }

func (t topFolder) Readdir(count int) ([]fs.FileInfo, error) {
	infos, err := t.File.Readdir(count)
	visible := infos[:0]
	for _, info := range infos {
		if !hidden(info.Name()) {
			visible = append(visible, info)
		}
	}
	return visible, err
}

// davUpload is a file being written over WebDAV. It does not embed the
// file, so io.Copy cannot get past Write through the file's own ReadFrom or
// WriteString.
type davUpload struct {
	file    *os.File
	d       davFS
	ctx     context.Context
	name    string
	tmp     string
	replace bool
	hash    hash.Hash
	size    int64
	failed  bool
	once    sync.Once
}

func (u *davUpload) Write(p []byte) (int, error) {
	if ok, err := u.d.f.fits(u.size + int64(len(p))); err != nil || !ok {
		u.failed = true
		if err == nil {
			err = errQuota
		}
		return 0, err
	}
	n, err := u.file.Write(p)
	u.size += int64(n)
	u.hash.Write(p[:n])
	if err != nil {
		u.failed = true
	}
	return n, err
}

func (u *davUpload) Read(p []byte) (int, error) { return u.file.Read(p) }

func (u *davUpload) Seek(offset int64, whence int) (int64, error) {
	return u.file.Seek(offset, whence)
}

func (u *davUpload) Readdir(int) ([]fs.FileInfo, error) { return nil, fs.ErrInvalid }

func (u *davUpload) Stat() (fs.FileInfo, error) { return u.file.Stat() }

// Close puts the written file in its place, or throws it away when writing
// failed.
func (u *davUpload) Close() error {
	var err error
	u.once.Do(func() { err = u.finish() })
	return err
}

func (u *davUpload) finish() error {
	f := u.d.f
	closeErr := u.file.Close()
	defer func() { _ = f.root.Remove(u.tmp) }()
	if u.failed || closeErr != nil {
		return errors.Join(errors.New("upload not finished"), closeErr)
	}
	link := f.root.Link
	if u.replace {
		link = f.replaceFile
	}
	if err := link(u.tmp, u.name); err != nil {
		return err
	}
	f.addUsage(u.size)
	action := "upload"
	if u.replace {
		action = "replace"
	}
	slog.Info("uploaded", "path", "/"+u.name, "size", u.size, "via", "webdav")
	u.d.note(u.ctx, action, "/"+u.name)
	if info, err := f.root.Stat(u.name); err == nil {
		f.uploaded(u.name, info, hex.EncodeToString(u.hash.Sum(nil)))
	}
	return nil
}
