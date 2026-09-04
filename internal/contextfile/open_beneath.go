package contextfile

import "os"

// OpenBeneath opens relPath (relative to workDir) for reading with
// kernel-enforced beneath-only resolution: os.Root guarantees the lookup
// cannot escape workDir even when a path component is swapped for a
// symlink between a caller's check and this open — the TOCTOU window a
// check-then-open sequence (Lstat/EvalSymlinks, then os.Open) can never
// close on its own. On Unix the open additionally carries O_NOFOLLOW (a
// symlink swapped into the FINAL component fails with an error instead of
// being followed) and O_NONBLOCK (a FIFO swapped in after the caller's
// check cannot block the open) — both no-ops for regular files. Callers
// MUST Stat the returned file and reject non-regular modes before
// reading: O_NONBLOCK means a FIFO can be opened without hanging, and the
// fstat is what rejects it.
func OpenBeneath(workDir, relPath string) (*os.File, error) {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.OpenFile(relPath, os.O_RDONLY|openBeneathExtraFlags, 0)
}
