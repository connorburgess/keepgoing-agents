//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

// No native artifact or bounded admission path is reviewed on these platforms.
func accountClaudeQuotaOpen(string) (*os.File, error) {
	return nil, errors.New("unverified_cli")
}
