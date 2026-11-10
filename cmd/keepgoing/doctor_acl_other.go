//go:build !linux && !darwin

package main

func doctorACLMetadata(string, bool) aclMetadata { return aclMetadata{} }
