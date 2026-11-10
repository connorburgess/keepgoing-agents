//go:build !linux

package main

func doctorACLMetadata(string, bool) aclMetadata { return aclMetadata{} }
