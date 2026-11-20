//go:build darwin

package userdir

import "golang.org/x/sys/unix"

// exchange swaps two directory entries in one step (renamex_np with
// RENAME_SWAP): no other process can see a moment in which either name is
// missing.
func exchange(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_SWAP)
}
