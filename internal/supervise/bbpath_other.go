//go:build !unix

package supervise

import "os"

// executableByOwner: on systems without Unix owners and modes, a regular
// file is taken as it is.
func executableByOwner(os.FileInfo) bool { return true }
