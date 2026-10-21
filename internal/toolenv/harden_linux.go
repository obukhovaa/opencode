//go:build linux

package toolenv

import "golang.org/x/sys/unix"

const platformHardens = true

func setNonDumpable() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
