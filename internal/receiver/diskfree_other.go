//go:build !windows

package receiver

import "golang.org/x/sys/unix"

// diskSpace returns the free space available to this user and the size of
// the disk holding dir.
func diskSpace(dir string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}
