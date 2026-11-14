//go:build windows

package main

import "os"

// fileOwnedByUser is not checked on Windows, where a build is never replaced
// from the panel (buildFileRefusal).
func fileOwnedByUser(os.FileInfo) bool { return false }
