//go:build unix

package api

import "syscall"

// freeDiskBytes reports available space to unprivileged users at path.
func freeDiskBytes(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
