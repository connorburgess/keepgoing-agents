//go:build unix

package supervise

import (
	"os"
	"syscall"
)

// executableByOwner: executable, and owned by this user or root.
func executableByOwner(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().Perm()&0o111 != 0 && (int(st.Uid) == os.Getuid() || st.Uid == 0)
}
