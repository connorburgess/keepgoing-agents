//go:build windows

package main

import (
	"context"
	"errors"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"os"
)

type buildTarget struct{ mode os.FileMode }

func openBuildTarget(string, state.FileIdentity, ...string) (*buildTarget, error) {
	return nil, errors.New("unavailable on Windows")
}
func (*buildTarget) close()                             {}
func (*buildTarget) read() ([]byte, error)              { return nil, errors.New("unavailable on Windows") }
func (*buildTarget) swap(context.Context, []byte) error { return errors.New("unavailable on Windows") }

func (*buildTarget) stat() (os.FileInfo, error) { return nil, errors.New("unavailable on Windows") }
