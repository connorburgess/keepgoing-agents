//go:build !windows

package runner

// openElsewhere is always false here: an open file can be moved.
func openElsewhere(error) bool { return false }
