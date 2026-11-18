//go:build !windows

package main

import (
	"fmt"
	"github.com/looptender/looptender/internal/state"
	"os"
)

func registryNativeIdentity(_ string, info os.FileInfo) (string, error) {
	id, ok := state.IdentityOf(info)
	if !ok {
		return "", errRegistryBinding
	}
	return fmt.Sprintf("%d:%d", id.Dev, id.Ino), nil
}
