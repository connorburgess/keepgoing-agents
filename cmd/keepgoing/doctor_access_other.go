//go:build !darwin && !linux

package main

func doctorOSAccess(string, uint32) error { return errAccessUnavailable }
