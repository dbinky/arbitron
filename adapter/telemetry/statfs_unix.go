//go:build darwin || linux

package telemetry

import "golang.org/x/sys/unix"

// statfsFree returns the bytes available to unprivileged users on path's volume.
func statfsFree(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
