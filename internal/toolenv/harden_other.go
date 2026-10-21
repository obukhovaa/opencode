//go:build !linux

package toolenv

// Elsewhere there is no /proc/<pid>/environ for a same-user process to read
// (macOS exposes another process's environment only to root), so there is
// nothing to do.
const platformHardens = false

func setNonDumpable() error { return nil }
