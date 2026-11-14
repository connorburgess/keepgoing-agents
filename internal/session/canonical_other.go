//go:build !darwin

package session

// canonical is the identity elsewhere: the supported file systems there are
// case-sensitive.
func canonical(path string) string { return path }
