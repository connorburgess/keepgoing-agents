//go:build !windows

package userdir

func isSharingViolation(error) bool { return false }
