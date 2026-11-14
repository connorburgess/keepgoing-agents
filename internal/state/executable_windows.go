//go:build windows

package state

import "os"

func fileIDs(os.FileInfo) (uint64, uint64, uint64, bool) { return 0, 0, 0, false }
