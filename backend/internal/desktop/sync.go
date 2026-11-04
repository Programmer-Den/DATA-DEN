package desktop

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// How a round works: the client lists both sides and compares each path
// with the state after the last round (the base). A side changed a file
// when its size or time differs from the base.
//
//   - Changed on one side: the change is copied to the other side. On the
//     server, a replaced file is kept as a version and a deleted one goes
//     into the recycle bin; on the PC, a deleted file goes into DeletedDir.
//   - Changed on both sides: the PC's version is renamed to
//     "name (Konflikt <date> <PC>).ext" and uploaded too, so nothing is
//     lost, and the server's version takes the name.
//   - Deleted on one side and changed on the other: the change wins.
//
// A replace or delete on the server only happens while the file there is
// still as the list showed it; otherwise the next round sees the change.
const (
	// StateDir is the client's own folder at the top of the synced folder,
	// never synced: the server refuses names starting with ".data-den-".
	StateDir = ".data-den-sync"
	// DeletedDir keeps the files the sync deleted on the PC, by day.
	DeletedDir = StateDir + "/deleted"
	// KeepDeletedDays is how long DeletedDir keeps them.
	KeepDeletedDays = 30
	stateFile       = StateDir + "/state.json"
	tmpDir          = StateDir + "/tmp"
	// saveEvery saves the base while a long round is still running.
	saveEvery = 100
)

// Syncer keeps the folder Local on the PC and the folder Remote on the
// server the same.
type Syncer struct {
	Client *Client
	// Local is the folder on the PC.
	Local string
	// Remote is the folder on the server, such as "/" or "/Dokumente".
	Remote string
	// Host names the PC in conflict copies.
	Host string
	// FoldCase is true where the PC's file names ignore case (Windows,
	// macOS): of server names that differ only in case, one is synced.
	FoldCase bool
	// LocalName reports whether the PC can hold a file of this name; nil
	// takes every name.
	LocalName func(string) bool
	Log       *slog.Logger
	// Now is the clock, time.Now when nil.
	Now func() time.Time

	base    map[string]Base
	warned  map[string]bool
	actions int
}

// Base is the state of a path after the last round.
type Base struct {
	Dir bool `json:"dir,omitempty"`
	// LocalSize, LocalMod are the file on the PC, RemoteSize, RemoteMod the
	// file on the server; Mod in nanoseconds since 1970.
	LocalSize  int64 `json:"lsize,omitempty"`
	LocalMod   int64 `json:"lmod,omitempty"`
	RemoteSize int64 `json:"rsize,omitempty"`
	RemoteMod  int64 `json:"rmod,omitempty"`
}

// Local is one file or folder on the PC.
type Local struct {
	Dir  bool
	Size int64
	Mod  int64
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// local is the path p, such as "Fotos/a.jpg", on the PC.
func (s *Syncer) local(p string) string {
	return filepath.Join(s.Local, filepath.FromSlash(p))
}

// remote is the path p on the server.
func (s *Syncer) remote(p string) string {
	return path.Join("/", s.Remote, p)
}

// Ignored is true for names that are never synced: the client's own and
// the server's folders, and what Windows, macOS and Office leave behind.
func Ignored(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(name, ".data-den-") || lower == "desktop.ini" || lower == "thumbs.db" ||
		name == ".DS_Store" || strings.HasPrefix(name, "~$") || strings.HasSuffix(lower, ".tmp")
}

// Round syncs once and returns how many changes it made. It stops at the
// first error that concerns every file, such as a refused login.
func (s *Syncer) Round(ctx context.Context) (int, error) {
	s.actions = 0
	if s.warned == nil {
		s.warned = map[string]bool{}
	}
	if err := os.MkdirAll(s.local(tmpDir), 0o700); err != nil {
		return 0, err
	}
	if err := s.load(); err != nil {
		return 0, err
	}
	s.purgeDeleted()
	locals, err := s.scan()
	if err != nil {
		return 0, err
	}
	list, err := s.Client.Tree(ctx, s.remote(""))
	if err != nil {
		return 0, err
	}
	remotes := s.remotes(list)
	if len(s.base) > 0 && (len(locals) == 0) != (len(remotes) == 0) {
		// One side is empty although both had files: more likely a missing
		// drive or a renamed folder than everything deleted on purpose.
		side := "on the PC"
		if len(remotes) == 0 {
			side = "on the server"
		}
		return 0, fmt.Errorf("the folder %s is empty or missing, so nothing is synced; to start over, delete %s", side, s.local(stateFile))
	}

	paths := make([]string, 0, len(s.base)+len(locals)+len(remotes))
	for p := range s.base {
		paths = append(paths, p)
	}
	for p := range locals {
		paths = append(paths, p)
	}
	for p := range remotes {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)

	// done holds folders handled as a whole: what is below them is skipped.
	var done []string
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		if slices.ContainsFunc(done, func(d string) bool { return strings.HasPrefix(p, d+"/") }) {
			continue
		}
		whole, err := s.syncPath(ctx, p, locals, remotes)
		if whole {
			done = append(done, p)
		}
		if errors.Is(err, ErrLogin) {
			_ = s.save()
			return s.actions, err
		}
		if errors.Is(err, ErrChanged) {
			s.log().Info("changed meanwhile, next round", "path", p)
		} else if err != nil {
			s.log().Warn("not synced", "path", p, "err", err)
		}
		if s.actions > 0 && s.actions%saveEvery == 0 {
			_ = s.save()
		}
	}
	return s.actions, errors.Join(s.save(), ctx.Err())
}

// syncPath brings p to the same state on both sides. whole is true when it
// handled the folder p with everything below it.
func (s *Syncer) syncPath(ctx context.Context, p string, locals map[string]Local, remotes map[string]Remote) (whole bool, err error) {
	l, hasL := locals[p]
	r, hasR := remotes[p]
	b, hasB := s.base[p]

	switch {
	case !hasL && !hasR:
		delete(s.base, p)
		return false, nil
	case hasL && hasR && l.Dir != r.Dir:
		// A file on one side, a folder on the other: the PC's moves aside.
		return true, s.conflict(ctx, p, r)
	case hasL && l.Dir || hasR && r.Dir:
		return s.syncFolder(ctx, p, hasL, hasR, hasB && b.Dir, locals, remotes)
	}

	hasB = hasB && !b.Dir
	localChanged := hasL && hasB && (l.Size != b.LocalSize || l.Mod != b.LocalMod)
	remoteChanged := hasR && hasB && (r.Size != b.RemoteSize || r.Mod != b.RemoteMod)
	switch {
	case hasL && hasR && !hasB:
		same, err := s.same(ctx, p, l, r)
		if err != nil {
			return false, err
		}
		if same {
			s.base[p] = Base{LocalSize: l.Size, LocalMod: l.Mod, RemoteSize: r.Size, RemoteMod: r.Mod}
			return false, nil
		}
		return false, s.conflict(ctx, p, r)
	case hasL && hasR:
		switch {
		case !localChanged && !remoteChanged:
			return false, nil
		case localChanged && !remoteChanged:
			return false, s.upload(ctx, p, l, &r)
		case !localChanged && remoteChanged:
			return false, s.download(ctx, p, r, &l)
		}
		same, err := s.same(ctx, p, l, r)
		if err != nil {
			return false, err
		}
		if same {
			s.base[p] = Base{LocalSize: l.Size, LocalMod: l.Mod, RemoteSize: r.Size, RemoteMod: r.Mod}
			return false, nil
		}
		return false, s.conflict(ctx, p, r)
	case hasL:
		if hasB && !localChanged {
			// Deleted on the server.
			return false, s.deleteLocal(p, &l)
		}
		return false, s.upload(ctx, p, l, nil)
	default:
		if hasB && !remoteChanged {
			// Deleted on the PC.
			s.log().Info("deleting on the server", "path", p)
			if err := s.Client.Delete(ctx, s.remote(p), &r); err != nil {
				return false, err
			}
			s.actions++
			delete(s.base, p)
			return false, nil
		}
		return false, s.download(ctx, p, r, nil)
	}
}

// syncFolder handles the folder p, which exists on at least one side.
func (s *Syncer) syncFolder(ctx context.Context, p string, hasL, hasR, hadDir bool, locals map[string]Local, remotes map[string]Remote) (bool, error) {
	switch {
	case hasL && hasR:
	case hasL && hadDir && unchangedBelow(s, p, locals, true):
		// Deleted on the server, and nothing below it changed on the PC.
		return true, s.deleteLocal(p, nil)
	case hasL:
		s.log().Info("creating a folder on the server", "path", p)
		if err := s.Client.Mkdir(ctx, s.remote(p)); err != nil {
			return false, err
		}
		s.actions++
	case hadDir && unchangedBelow(s, p, remotes, false):
		// Deleted on the PC, and nothing below it changed on the server.
		s.log().Info("deleting a folder on the server", "path", p)
		if err := s.Client.Delete(ctx, s.remote(p), nil); err != nil {
			return false, err
		}
		s.actions++
		s.forgetBelow(p)
		return true, nil
	default:
		if err := os.MkdirAll(s.local(p), 0o750); err != nil {
			return false, err
		}
		s.actions++
	}
	s.base[p] = Base{Dir: true}
	return false, nil
}

// unchangedBelow reports whether every entry below p on one side is as in
// the base, so the folder can be deleted as a whole.
func unchangedBelow[E Local | Remote](s *Syncer, p string, side map[string]E, local bool) bool {
	for q, e := range side {
		if !strings.HasPrefix(q, p+"/") {
			continue
		}
		b, ok := s.base[q]
		if !ok {
			return false
		}
		dir, size, mod := stateOf(e)
		if dir != b.Dir {
			return false
		}
		if dir {
			continue
		}
		if local && (size != b.LocalSize || mod != b.LocalMod) || !local && (size != b.RemoteSize || mod != b.RemoteMod) {
			return false
		}
	}
	return true
}

func stateOf[E Local | Remote](e E) (dir bool, size, mod int64) {
	switch e := any(e).(type) {
	case Local:
		return e.Dir, e.Size, e.Mod
	case Remote:
		return e.Dir, e.Size, e.Mod
	}
	return false, 0, 0
}

// forgetBelow drops p and everything below it from the base.
func (s *Syncer) forgetBelow(p string) {
	for q := range s.base {
		if q == p || strings.HasPrefix(q, p+"/") {
			delete(s.base, q)
		}
	}
}

// same reports whether the file p has the same content on both sides.
func (s *Syncer) same(ctx context.Context, p string, l Local, r Remote) (bool, error) {
	if l.Size != r.Size {
		return false, nil
	}
	sum, err := hashFile(s.local(p))
	if err != nil {
		return false, err
	}
	if r.SHA256 != "" {
		return sum == r.SHA256, nil
	}
	// The server has no checksum yet: compare with its content.
	h := sha256.New()
	if err := s.Client.Download(ctx, s.remote(p), h); err != nil {
		return false, err
	}
	return sum == hex.EncodeToString(h.Sum(nil)), nil
}

// conflict keeps both versions of p: the PC's moves to a conflict copy,
// which the next round uploads, and the server's takes its place.
func (s *Syncer) conflict(ctx context.Context, p string, r Remote) error {
	copyName := s.conflictName(p)
	s.log().Warn("changed on both sides, the PC's version is kept as a copy", "path", p, "copy", copyName)
	if err := os.Rename(s.local(p), s.local(copyName)); err != nil {
		return err
	}
	s.actions++
	s.forgetBelow(p)
	if r.Dir {
		if err := os.Mkdir(s.local(p), 0o750); err != nil {
			return err
		}
		s.base[p] = Base{Dir: true}
		// What is below it comes in the next round.
		return nil
	}
	return s.download(ctx, p, r, nil)
}

// conflictName is a free name for the PC's version of p, such as
// "Fotos/a (Konflikt 2026-11-04 2105 PC).jpg".
func (s *Syncer) conflictName(p string) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	label := "Konflikt " + s.now().Format("2006-01-02 1504")
	if s.Host != "" {
		label += " " + s.Host
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s%s (%s)%s", dir, stem, label, ext)
		if i > 1 {
			candidate = fmt.Sprintf("%s%s (%s %d)%s", dir, stem, label, i, ext)
		}
		if _, err := os.Lstat(s.local(candidate)); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
	}
}

// upload sends the PC's file p; with replace, over the server's file.
func (s *Syncer) upload(ctx context.Context, p string, l Local, replace *Remote) error {
	s.log().Info("uploading", "path", p, "size", l.Size)
	mod, _, err := s.Client.Upload(ctx, s.local(p), s.remote(p), l.Size, replace)
	if err != nil {
		return err
	}
	s.actions++
	// If the file changed during the upload, its time now differs from the
	// base, and the next round uploads it again.
	s.base[p] = Base{LocalSize: l.Size, LocalMod: l.Mod, RemoteSize: l.Size, RemoteMod: mod}
	return nil
}

// download fetches the server's file p. With replace, it replaces the PC's
// file, if that is still so; without, only where none is.
func (s *Syncer) download(ctx context.Context, p string, r Remote, replace *Local) error {
	s.log().Info("downloading", "path", p, "size", r.Size)
	tmp, err := os.CreateTemp(s.local(tmpDir), "download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	counter := &countingWriter{w: tmp}
	err = s.Client.Download(ctx, s.remote(p), counter)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if counter.n != r.Size {
		return ErrChanged
	}
	target := s.local(p)
	if replace == nil {
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			return ErrChanged
		}
	} else if now, err := stat(target); err != nil || now != *replace {
		return ErrChanged
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	s.actions++
	l, err := stat(target)
	if err != nil {
		return err
	}
	s.base[p] = Base{LocalSize: l.Size, LocalMod: l.Mod, RemoteSize: r.Size, RemoteMod: r.Mod}
	return nil
}

// deleteLocal moves p into DeletedDir. With want, only a file that is still
// so.
func (s *Syncer) deleteLocal(p string, want *Local) error {
	if want != nil {
		if now, err := stat(s.local(p)); err != nil || now != *want {
			return ErrChanged
		}
	}
	s.log().Info("deleted on the server, moving aside on the PC", "path", p, "to", DeletedDir)
	target := s.local(path.Join(DeletedDir, s.now().Format("2006-01-02"), p))
	if _, err := os.Lstat(target); err == nil {
		target += "." + randomSuffix()
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Rename(s.local(p), target); err != nil {
		return err
	}
	s.actions++
	s.forgetBelow(p)
	return nil
}

// purgeDeleted removes the days in DeletedDir older than KeepDeletedDays.
func (s *Syncer) purgeDeleted() {
	days, err := os.ReadDir(s.local(DeletedDir))
	if err != nil {
		return
	}
	limit := s.now().AddDate(0, 0, -KeepDeletedDays)
	for _, day := range days {
		t, err := time.ParseInLocation("2006-01-02", day.Name(), time.Local)
		if err == nil && t.Before(limit) {
			if err := os.RemoveAll(s.local(path.Join(DeletedDir, day.Name()))); err != nil {
				s.log().Warn("purging deleted files", "day", day.Name(), "err", err)
			}
		}
	}
}

// scan lists the PC's folder.
func (s *Syncer) scan() (map[string]Local, error) {
	locals := map[string]Local{}
	err := filepath.WalkDir(s.Local, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			if name == s.Local {
				return err
			}
			s.log().Warn("not readable", "path", name, "err", err)
			return nil
		}
		if name == s.Local {
			return nil
		}
		rel, err := filepath.Rel(s.Local, name)
		if err != nil {
			return err
		}
		p := filepath.ToSlash(rel)
		if Ignored(d.Name()) || len(d.Name()) > 255 || strings.Contains(d.Name(), "\\") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsDir() && !d.Type().IsRegular() {
			// Links and the like are not synced.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		locals[p] = localOf(info)
		return nil
	})
	return locals, err
}

// remotes keeps the server's entries the PC can hold.
func (s *Syncer) remotes(list []Remote) map[string]Remote {
	slices.SortFunc(list, func(a, b Remote) int { return strings.Compare(a.Path, b.Path) })
	byPath := make(map[string]Remote, len(list))
	folded := map[string]string{}
	var skipped []string
	for _, r := range list {
		if slices.ContainsFunc(skipped, func(d string) bool { return strings.HasPrefix(r.Path, d+"/") }) {
			continue
		}
		name := path.Base(r.Path)
		if Ignored(name) {
			skipped = append(skipped, r.Path)
			continue
		}
		reason := ""
		if s.LocalName != nil && !s.LocalName(name) {
			reason = "the name is not allowed on this PC"
		}
		key := r.Path
		if s.FoldCase {
			key = strings.ToLower(r.Path)
			if other, ok := folded[key]; ok && reason == "" {
				reason = "differs from " + other + " only in case"
			}
		}
		if reason != "" {
			skipped = append(skipped, r.Path)
			if !s.warned[r.Path] {
				s.warned[r.Path] = true
				s.log().Warn("not synced", "path", r.Path, "reason", reason)
			}
			continue
		}
		folded[key] = r.Path
		byPath[r.Path] = r
	}
	return byPath
}

func (s *Syncer) load() error {
	if s.base != nil {
		return nil
	}
	s.base = map[string]Base{}
	data, err := os.ReadFile(s.local(stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = json.Unmarshal(data, &s.base)
	}
	if err != nil {
		// Without the base every file is compared by content once, and
		// nothing is deleted.
		s.log().Warn("the sync state cannot be read, so both sides are compared again", "err", err)
		s.base = map[string]Base{}
	}
	return nil
}

// save writes the base through a temporary file, so a crash never leaves
// half of it.
func (s *Syncer) save() error {
	data, err := json.Marshal(s.base)
	if err != nil {
		return err
	}
	tmp := s.local(stateFile) + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.local(stateFile))
}

func localOf(info fs.FileInfo) Local {
	if info.IsDir() {
		return Local{Dir: true}
	}
	return Local{Size: info.Size(), Mod: info.ModTime().UnixNano()}
}

func stat(name string) (Local, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return Local{}, err
	}
	return localOf(info), nil
}

func hashFile(name string) (string, error) {
	file, err := os.Open(name) //nolint:gosec // a file of the synced folder
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
