package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each "Needs you" card ended in "Resume the loop when ..." even on a panel
// that cannot resume (design pass 2, new finding 2). A held loop's need now
// carries the command that resumes it, which a read-only page shows to copy
// with where to find the configuration; a page that allows changes keeps its
// own controls.
func TestDockHeldNeedGivesTheResumeCommand(t *testing.T) {
	held := dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}}.input()
	unread := dockEvidence{noSnap: true, issues: []string{"control_unavailable"}}.input()
	unread.Entry.ID, unread.Entry.Loop = "loop_0123456789abcdef0123456789abcde2", "example-unread"
	r := buildDockReport([]dockInput{held, unread}, dockNow, 15*time.Second, 30*time.Minute, false, true, 0, true)
	if len(r.NeedsYou) != 2 {
		t.Fatalf("%+v", r.NeedsYou)
	}
	for _, n := range r.NeedsYou {
		switch n.Kind {
		case "held":
			if n.Resume != "keepgoing resume -c CONFIG" || !strings.Contains(n.Action, "esume") {
				t.Errorf("held: %+v", n)
			}
		default:
			// Resuming is not the way out of a loop that could not be read.
			if n.Resume != "" {
				t.Errorf("%s: %+v", n.Kind, n)
			}
		}
	}

	// The command is the real CLI's: resume takes the configuration with -c.
	// The flags parse: with a missing file the command gets as far as
	// loading it.
	args := strings.Fields(dockResumeCommand)
	if args[0] != "keepgoing" || args[len(args)-1] != "CONFIG" {
		t.Fatalf("%q", args)
	}
	args[len(args)-1] = filepath.Join(t.TempDir(), "missing.yaml")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), args[1:], &out, &errOut); code == 0 || !strings.HasPrefix(errOut.String(), "config: open ") {
		t.Errorf("exit %d: %s", code, errOut.String())
	}

	// The page shows it only when it cannot resume, and says where CONFIG is.
	js := dockUIText(t, "dock.js")
	for _, want := range []string{
		"if (n.resume && !changesOn()) {",
		`copyCommand(el("p", "copy resume-command"), n.resume, "copy-resume-" + n.key)`,
		`"keepgoing registry list --private"`,
		`return copyCommand(el("dd", "copy"), "bb thread open " + id, "copy-thread-command");`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
}
