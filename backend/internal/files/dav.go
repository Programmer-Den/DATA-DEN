package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"hash"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/webdav"

	"github.com/Programmer-Den/DATA-DEN/backend/internal/status"
)

// WebDAV under DavPrefix, so Windows (and Finder, Nautilus, rclone and the
// like) can mount a space as a network drive. It goes through the same
// rules as the web interface: the server's own folders stay hidden, deleting
// moves into the recycle bin, overwriting keeps the old file as a version,
// and new files count against the quota and get a checksum. The drive of
// one's own space shows the quota (or the free space on the disk) as its
// size, so Explorer and Finder show how much still fits.
//
// Each shared or team folder is a drive of its own under DavSharedPrefix,
// as /dav-shared/<grant id>/, with the rights of its grant: only reading,
// or also changing, as in the web interface.

// DavPrefix is where WebDAV is served.
const DavPrefix = "/dav"

// DavSharedPrefix is where the shared and team folders are served over
// WebDAV, each under the id of its grant.
const DavSharedPrefix = "/dav-shared"

// errQuota is returned by a write that does not fit into the quota.
var errQuota = errors.New("the quota is full")

type requestKey struct{}

// davHandler serves WebDAV for this space, with its own locks.
func (f *Files) davHandler() *webdav.Handler {
	f.davOnce.Do(func() {
		f.dav = newDavHandler(DavPrefix, davFS{f: f, dir: ".", quota: true})
	})
	return f.dav
}

// newDavHandler serves d under prefix, with its own locks.
func newDavHandler(prefix string, d davFS) *webdav.Handler {
	return &webdav.Handler{
		Prefix:     prefix,
		FileSystem: d,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}
}

// ServeDAV answers a WebDAV request for this space; the login was checked
// before.
func (f *Files) ServeDAV(w http.ResponseWriter, r *http.Request) {
	f.davHandler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
}

// davFS is a space, or a folder in it, as a webdav.FileSystem.
type davFS struct {
	f *Files
	// dir is the folder served, such as "Projekt", or "." for the whole
	// space.
	dir string
	// readOnly refuses every change.
	readOnly bool
	// quota shows the space's quota on its folders.
	quota bool
}

// name turns a WebDAV path into a name within the root, refusing the
// server's own folders. In a shared folder it also refuses a path through a
// symlink, which could lead out of the folder, as the web interface does.
func (d davFS) name(name string) (string, error) {
	if strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return "", fs.ErrInvalid
	}
	clean := path.Join(d.dir, strings.TrimPrefix(path.Clean("/"+name), "/"))
	if hidden(clean) || (d.dir != "." && d.f.symlinkIn(clean)) {
		return "", fs.ErrNotExist
	}
	return clean, nil
}

// change is name for a change, refusing the folder served itself and every
// change when it may only be read.
func (d davFS) change(name string) (string, error) {
	if d.readOnly {
		return "", fs.ErrPermission
	}
	name, err := d.name(name)
	if err != nil {
		return "", err
	}
	if name == d.dir {
		return "", fs.ErrPermission
	}
	return name, nil
}

// note records action on target in the audit log, for the request in ctx.
func (d davFS) note(ctx context.Context, action, target string) {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		d.f.note(r, action, target)
	}
}

func (d davFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	if top, err := d.name(name); err == nil && top == d.dir {
		return fs.ErrExist
	}
	name, err := d.change(name)
	if err != nil {
		return err
	}
	if err := d.f.root.Mkdir(name, 0o750); err != nil {
		return err
	}
	slog.Info("folder created", "path", "/"+name, "via", "webdav")
	d.note(ctx, "folder", "/"+name)
	return nil
}

func (d davFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	// Read and write without creating or truncating is how WebDAV opens a
	// file to change its properties (PROPPATCH), not its content: that
	// opens it to read, and Patch answers.
	if flag&(os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		name, err := d.change(name)
		if err != nil {
			return nil, err
		}
		return d.create(ctx, name, flag)
	}
	name, err := d.name(name)
	if err != nil {
		return nil, err
	}
	file, err := d.f.root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.IsDir() {
		return davFolder{davFile: davFile{file}, d: d, top: name == "."}, nil
	}
	return davFile{file}, nil
}

// create starts writing the file name. The content goes into the upload
// folder first and takes the file's place when it is closed, so a broken
// upload leaves the old file as it was.
func (d davFS) create(ctx context.Context, name string, flag int) (webdav.File, error) {
	if flag&os.O_APPEND != 0 {
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
	name, err := d.change(name)
	if err != nil {
		return err
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
	from, err := d.change(oldName)
	if err != nil {
		return err
	}
	to, err := d.change(newName)
	if err != nil {
		return err
	}
	if strings.HasPrefix(to, from+"/") {
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
	d.f.moveRecords(from, to)
	slog.Info("moved", "from", "/"+from, "to", "/"+to, "via", "webdav")
	d.note(ctx, "move", "/"+from+" → /"+to)
	return nil
}

func (d davFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	name, err := d.name(name)
	if err != nil {
		return nil, err
	}
	return d.f.root.Stat(name)
}

// davFile is a file or folder opened to read, whose properties cannot be
// changed.
type davFile struct{ *os.File }

// DeadProps answers no properties of its own.
func (davFile) DeadProps() (map[xml.Name]webdav.Property, error) { return nil, nil }

// win32 is the namespace of the properties Windows sets after copying a
// file: its times and attributes.
const win32 = "urn:schemas-microsoft-com:"

// Patch accepts the properties Windows sets, without keeping them, since
// Explorer reports a failed copy when they are refused. Any other change is
// refused, and then none is made.
func (davFile) Patch(patches []webdav.Proppatch) ([]webdav.Propstat, error) {
	var props []webdav.Property
	accepted := true
	for _, patch := range patches {
		for _, p := range patch.Props {
			props = append(props, webdav.Property{XMLName: p.XMLName})
			accepted = accepted && !patch.Remove && p.XMLName.Space == win32 && strings.HasPrefix(p.XMLName.Local, "Win32")
		}
	}
	status := http.StatusOK
	if !accepted {
		status = http.StatusForbidden
	}
	return []webdav.Propstat{{Props: props, Status: status}}, nil
}

// davFolder is a folder: at the top of a space without the server's own
// folders, and with the space's quota as properties.
type davFolder struct {
	davFile
	d   davFS
	top bool
}

func (t davFolder) Readdir(count int) ([]fs.FileInfo, error) {
	infos, err := t.File.Readdir(count)
	if !t.top {
		return infos, err
	}
	visible := infos[:0]
	for _, info := range infos {
		if !hidden(info.Name()) {
			visible = append(visible, info)
		}
	}
	return visible, err
}

// The quota properties of RFC 4331, which Windows and macOS show as the
// drive's size.
var (
	quotaUsed      = xml.Name{Space: "DAV:", Local: "quota-used-bytes"}
	quotaAvailable = xml.Name{Space: "DAV:", Local: "quota-available-bytes"}
)

// DeadProps answers the quota properties: what the space holds, and what
// still fits.
func (t davFolder) DeadProps() (map[xml.Name]webdav.Property, error) {
	if !t.d.quota {
		return nil, nil
	}
	used, available, ok := t.d.f.room()
	if !ok {
		return nil, nil
	}
	prop := func(name xml.Name, n int64) webdav.Property {
		return webdav.Property{XMLName: name, InnerXML: []byte(strconv.FormatInt(n, 10))}
	}
	return map[xml.Name]webdav.Property{
		quotaUsed:      prop(quotaUsed, used),
		quotaAvailable: prop(quotaAvailable, available),
	}, nil
}

// room is how many bytes the space holds and how many still fit: up to the
// quota, and no more than is free on the disk. ok is false when neither is
// known.
func (f *Files) room() (used, available int64, ok bool) {
	used, err := f.usage()
	if err != nil {
		return 0, 0, false
	}
	quota := f.quota.Load()
	_, free, err := status.Space(f.root.Name())
	switch {
	case quota > 0 && err != nil:
		available = quota - used
	case quota > 0:
		available = min(quota-used, free)
	case err != nil:
		return 0, 0, false
	default:
		available = free
	}
	return used, max(available-f.pending(), 0), true
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
	sum := hex.EncodeToString(u.hash.Sum(nil))
	if u.replace {
		link = f.replaceFile(sum)
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
		f.uploaded(u.name, info, sum)
	}
	return nil
}
