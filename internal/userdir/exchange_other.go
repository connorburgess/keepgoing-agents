//go:build !linux && !darwin

package userdir

// exchange is not available here; a move renames, then links.
func exchange(a, b string) error { return errNoExchange }
