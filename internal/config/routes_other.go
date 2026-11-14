//go:build !unix

package config

import (
	"os"

	snapfile "github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// openRoutesFile opens the list as other records are read on Windows: with
// delete sharing, so a reader never blocks the rename that replaces the list,
// and retried while a replacement is in progress.
func openRoutesFile(path string) (*os.File, error) { return snapfile.Open(path) }

// Windows access is governed by ACLs, which these mode bits do not show; the
// loop registry makes the same allowance.

func ownedByUser(os.FileInfo) bool { return true }

func privateMode(os.FileInfo) bool { return true }
