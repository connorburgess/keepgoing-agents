package telegram

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTokenStorageAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(TokenEnv, "")

	if HasTokenFile(dir) {
		t.Fatal("expected no token file initially")
	}
	if _, err := LoadToken(dir); !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("missing token err = %v", err)
	}

	if err := SaveToken(dir, "bad-token"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid token save err = %v", err)
	}

	if err := SaveToken(dir, testToken); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if !HasTokenFile(dir) {
		t.Fatal("expected token file to exist")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, TokenFile))
		if err != nil {
			t.Fatal(err)
		}
		if mode := st.Mode().Perm(); mode != 0600 {
			t.Fatalf("token.env mode = %o, want 0600", mode)
		}
	}

	got, err := LoadToken(dir)
	if err != nil || got != testToken {
		t.Fatalf("LoadToken = %q, %v", got, err)
	}

	envTok := "987654321:ENV_OVERRIDE_TOKEN_EXAMPLE"
	t.Setenv(TokenEnv, envTok)
	got, err = LoadToken(dir)
	if err != nil || got != envTok {
		t.Fatalf("env precedence = %q, %v", got, err)
	}

	t.Setenv(TokenEnv, "invalid-env-token")
	if _, err := LoadToken(dir); !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "invalid-env-token") {
		t.Fatalf("bad env err = %v", err)
	}

	t.Setenv(TokenEnv, "")
	if err := RemoveToken(dir); err != nil {
		t.Fatalf("RemoveToken: %v", err)
	}
	if HasTokenFile(dir) {
		t.Fatal("expected token file removed")
	}
}
