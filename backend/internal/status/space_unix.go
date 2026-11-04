//go:build unix

package status

import "golang.org/x/sys/unix"

// space is the size of the file system that holds dir, and the space on it
// that an unprivileged user can still write.
func space(dir string) (total, free int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)                                       //nolint:unconvert // its type differs between systems
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil //nolint:gosec // block counts fit in int64
}
