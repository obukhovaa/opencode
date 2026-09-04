//go:build windows

package contextfile

// Windows open flags have no O_NOFOLLOW/O_NONBLOCK equivalents; os.Root
// itself provides the beneath-only resolution and symlink/junction
// containment there.
const openBeneathExtraFlags = 0
