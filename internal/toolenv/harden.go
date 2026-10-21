package toolenv

import "os"

// HardenProcess hides opencode's own environment from the processes it
// starts, when that environment holds anything the deny list matches.
//
// Filtering a child's environment is not enough on its own: a child runs as
// opencode's user, and on Linux a same-user process can read another's
// initial environment from /proc/<pid>/environ, so `cat /proc/$PPID/environ`
// in the bash tool would print the very values the filter removed. Unsetting
// them in-process does not help, because the kernel serves the original
// environment block. Marking the process non-dumpable (as ssh-agent does)
// makes that file unreadable to anyone but root. It also disables core dumps
// and same-user debugger attach, which is why it is applied only when there
// is something to hide. execve resets the flag, so children are unaffected.
//
// Call it once, after config is loaded (the configured deny entries count).
// It returns the denied names it found and whether the process was hardened;
// on platforms without the mechanism it never hardens.
func HardenProcess() (names []string, hardened bool, err error) {
	names = Present(os.Environ(), DenyList())
	if len(names) == 0 {
		return nil, false, nil
	}
	if err := setNonDumpable(); err != nil {
		return names, false, err
	}
	return names, platformHardens, nil
}
