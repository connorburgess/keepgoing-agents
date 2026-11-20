//go:build !darwin && !linux && !windows

package main

func doctorOSAccess(string, uint32) error { return errAccessUnavailable }
