//go:build !unix

package config

import "os"

// Windows access is governed by ACLs, which these mode bits do not show; the
// loop registry makes the same allowance.
func ownedByUser(os.FileInfo) bool { return true }

func privateMode(os.FileInfo) bool { return true }
