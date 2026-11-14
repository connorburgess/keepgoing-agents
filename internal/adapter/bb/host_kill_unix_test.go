//go:build unix

package bb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// A bb command ended by a signal the adapter did not send is counted, so a
// check can say the host killed it. One that the adapter's own time limit
// stopped, and one that simply failed, are not.
func TestHostKillOfABBCommandIsCounted(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) *Adapter {
		path := filepath.Join(dir, name)
		testexec.Script(t, path, "#!/bin/sh\n"+body+"\n")
		return New(config.BB{Thread: "thr_example123", CLI: path})
	}
	killed := script("killed", "kill -9 $$")
	if _, err := killed.exec(context.Background(), "", "thread", "show", "thr_example123", "--json"); err == nil || killed.HostKills() != 1 {
		t.Fatalf("%v, %d host kills", err, killed.HostKills())
	}
	var _ adapter.KillReporter = killed

	failed := script("failed", "exit 3")
	if _, err := failed.exec(context.Background(), "", "thread", "show"); err == nil || failed.HostKills() != 0 {
		t.Fatalf("%v, %d host kills", err, failed.HostKills())
	}
	slow := script("slow", "sleep 30")
	slow.Timeout = 200 * time.Millisecond
	if _, err := slow.exec(context.Background(), "", "thread", "show"); err == nil || slow.HostKills() != 0 {
		t.Fatalf("a command stopped at the adapter's own limit was counted as a host kill: %v, %d", err, slow.HostKills())
	}
}
