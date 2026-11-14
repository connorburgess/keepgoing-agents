//go:build linux

package userdir

import "golang.org/x/sys/unix"

// exchange swaps two directory entries in one step (renameat2 with
// RENAME_EXCHANGE): no other process can see a moment in which either name
// is missing.
func exchange(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}
