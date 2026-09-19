//go:build windows

package deviceauth

// Directory fsync is not exposed on Windows; the file sync plus atomic
// rename above is the durability offered there.
func fsyncDir(path string) error { return nil }
