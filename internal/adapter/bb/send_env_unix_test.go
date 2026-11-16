//go:build unix

package bb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/testexec"
)

// A message goes out without the supervisor's BB thread identity; every other
// bb command keeps the environment it was given.
func TestSendRunsWithoutTheThreadIdentity(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	cli := filepath.Join(dir, "bb")
	testexec.Script(t, cli, "#!/bin/sh\necho \"$1 $2 [${BB_THREAD_ID-unset}] [$BB_PROJECT_ID]\" >> \""+seen+"\"\necho '{\"ok\":true}'\n")
	t.Setenv("BB_THREAD_ID", "thr_exampleoperator")
	t.Setenv("BB_PROJECT_ID", "proj_example")
	a := New(config.BB{Thread: "thr_example123", CLI: cli})
	if err := a.Send(context.Background(), "keep going", adapter.ModeQueue); err != nil {
		t.Fatal(err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(seen)
	want := "thread tell [unset] [proj_example]\nthread stop [thr_exampleoperator] [proj_example]\n"
	if strings.TrimSpace(string(b)) != strings.TrimSpace(want) {
		t.Fatalf("bb saw %q, want %q", b, want)
	}
}
