//go:build unix

package main

import (
	"context"
	"github.com/looptender/looptender/internal/accounts"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountsExecStandInBoundsAndPrivateOutput(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "fixture")
	write(t, cli, "#!/bin/sh\nprintf '%s' \"$BB_THREAD_ID\"\nprintf 'private stderr' >&2\n", 0700)
	out, err := accountExec(context.Background(), cli, []string{"--version"}, []string{"BB_THREAD_ID=thr_example_actual"}, root)
	if err != nil || string(out) != "thr_example_actual" {
		t.Fatal("caller environment changed")
	}
	write(t, cli, "#!/bin/sh\nhead -c 70000 /dev/zero\n", 0700)
	if out, err = accountExec(context.Background(), cli, nil, os.Environ(), root); err == nil || len(out) != 0 {
		t.Fatal("output not bounded")
	}
	write(t, cli, "#!/bin/sh\nexec sleep 20\n", 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err = accountExec(ctx, cli, nil, os.Environ(), root); err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("timeout not bounded")
	}
}
func TestAccountsRefreshNeverRunsStandIn(t *testing.T) {
	reader, _ := dockFixtureReader(t)
	dir, e := accountFixture(t, "claude-code")
	reader.accountsDir = dir
	marker := filepath.Join(e.Workdir, "invoked")
	write(t, e.CLI, "#!/bin/sh\ntouch '"+marker+"'\nprintf '2.1.318 (Claude Code)'\n", 0700)
	for i := 0; i < 3; i++ {
		reader.read(context.Background(), false)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refresh executed provider")
	}
}
func TestAccountsCLIDirectoryAndExplicitSelection(t *testing.T) {
	// Covered by the regular CLI tests as well; shell fixture makes fixed argv,
	// explicit context and no implicit login/model command observable end to end.
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	dir, e := accountFixture(t, "claude-code")
	account := filepath.Join(e.Workdir, "selected")
	if os.Mkdir(account, 0700) != nil {
		t.Fatal("fixture directory")
	}
	write(t, e.CLI, "#!/bin/sh\nif [ \"$1\" = '--version' ]; then printf '2.1.318 (Claude Code)'; exit; fi\n[ \"$1 $2 $3\" = 'auth status --json' ] || exit 9\n[ -z \"${BB_THREAD_ID+set}\" ] || exit 8\n[ \"$CLAUDE_CONFIG_DIR\" = '"+account+"' ] || exit 7\nprintf '%s' '{\"loggedIn\":true,\"subscriptionType\":\"max\",\"email\":\"private@example.com\"}'\n", 0700)
	if err := accounts.Update(dir, func(f *accounts.File) error {
		old := f.Entries[0]
		next := old
		next.Mode = "explicit_directory"
		next.Directory = account
		f.Entries[0] = accounts.Revise(*f, old, next, "thr_example_actual", time.Now(), os.Environ())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", accountExec, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, _ := accounts.Load(dir)
	if f.Entries[0].Attempt.Auth != "reported_present" {
		t.Fatal("fixed status command failed")
	}
	// A checkout destination is refused by the same canonical guards as registry.
	checkout := filepath.Join(t.TempDir(), "repo")
	_ = os.Mkdir(checkout, 0700)
	_ = os.Mkdir(filepath.Join(checkout, ".git"), 0700)
	if _, err := accountsDirectory(filepath.Join(checkout, "accounts")); err == nil {
		t.Fatal("checkout accepted")
	}
}
