//go:build !unix

package session

// alive cannot be asked cheaply here; a holder file is believed.
func alive(int) bool { return true }
