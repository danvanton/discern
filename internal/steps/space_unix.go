//go:build !windows

package steps

import "golang.org/x/sys/unix"

// freeSpace is the space an unprivileged process can still write on the
// filesystem holding dir.
func freeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), nil
}
