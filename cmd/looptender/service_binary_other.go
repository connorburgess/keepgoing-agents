//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func openServiceBinary(string) (*os.File, error) {
	return nil, errors.New("service executable inspection unavailable on this platform")
}
