//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorExecUsesAccountEnvironmentAndDiscardsStderr(t *testing.T) {
	p := filepath.Join(t.TempDir(), "version-cli")
	write(t, p, "#!/bin/sh\nprintf '%s' \"$EXAMPLE_ACCOUNT\"\nprintf 'private stderr' >&2\n", 0o700)
	t.Setenv("EXAMPLE_ACCOUNT", "inherited")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := doctorExec(ctx, p, []string{"--version"}, map[string]string{"EXAMPLE_ACCOUNT": "selected"}, "")
	if err != nil || string(out) != "selected" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDoctorExecBoundsOutputAndTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "version-cli")
	write(t, p, "#!/bin/sh\nhead -c 70000 /dev/zero\n", 0o700)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, err := doctorExec(ctx, p, []string{"--version"}, nil, "")
	cancel()
	if err == nil || len(out) != 0 || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("unbounded output: %d %v", len(out), err)
	}
	write(t, p, "#!/bin/sh\nexec sleep 20\n", 0o700)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = doctorExec(ctx, p, []string{"--version"}, nil, "")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("version command did not time out")
	}
}

func TestDoctorLoginExitCodesAndWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth-cli")
	for _, code := range []int{0, 1, 2} {
		// Emit a sensitive field to ensure only the status is reported.
		script := fmt.Sprintf("#!/bin/sh\n[ -f ./expected-project ] || exit 9\nprintf '%%s' '{\"loggedIn\":false,\"email\":\"%s\"}'\nprintf 'private error' >&2\nexit %d\n", doctorPrivate, code)
		write(t, p, script, 0o700)
		if err := os.WriteFile(filepath.Join(dir, "expected-project"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := doctorExec(ctx, p, []string{"auth", "status", "--json"}, nil, dir)
		cancel()
		r := doctorReport{}
		doctorClaudeLogin(&r, "login", out, err)
		want := "warning"
		if code == 2 {
			want = "unavailable"
		}
		if c := doctorCheckByID(t, r, "login"); c.Status != want || strings.Contains(c.Message, doctorPrivate) {
			t.Fatalf("exit %d: %+v", code, c)
		}
		r = doctorReport{}
		doctorCodexLogin(&r, "login", err)
		want = "unavailable"
		if code == 0 {
			want = "ok"
		}
		if c := doctorCheckByID(t, r, "login"); c.Status != want {
			t.Fatalf("codex exit %d: %+v", code, c)
		}
	}
}
