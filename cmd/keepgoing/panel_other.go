//go:build !unix

package main

// keepgoing panel refuses to start where it is unverified, so these have
// nothing to do there.
func panelRestrictUmask()        {}
func panelCloseInheritedOnExec() {}
