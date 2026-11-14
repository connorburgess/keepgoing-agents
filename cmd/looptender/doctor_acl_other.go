//go:build !linux && !darwin && !windows

package main

func doctorACLMetadata(string, bool) aclMetadata { return aclMetadata{} }
