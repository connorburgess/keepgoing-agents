//go:build !unix

package session

import "os"

// keepgoing panel and the claim are unverified here; these are permissive.
func alive(int) bool               { return true }
func ownedByMe(os.FileInfo) bool   { return true }
func heldByOther(path string) bool { _, err := os.Stat(path); return err == nil }
