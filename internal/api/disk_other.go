//go:build !unix

package api

// freeDiskBytes is unimplemented off-unix (dev only).
func freeDiskBytes(path string) (uint64, bool) { return 0, false }
