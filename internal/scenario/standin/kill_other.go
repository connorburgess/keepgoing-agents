//go:build !unix

package main

import "os"

func killParent() {
	if p, err := os.FindProcess(os.Getppid()); err == nil {
		p.Kill()
	}
}
