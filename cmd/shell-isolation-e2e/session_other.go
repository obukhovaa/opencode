//go:build windows

package main

// sessionIsolated has no meaning on Windows, which has no POSIX sessions. The
// harness that calls this driver is a bash script and does not run there.
func sessionIsolated(_ int) bool { return false }
