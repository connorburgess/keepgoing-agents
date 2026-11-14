//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
)

// A manual `fallbacks check` leaves one history record of its probes, with
// hashed aliases and typed results only. A dry run probes nothing and leaves
// none.
func TestFallbacksCheckRecordsItsProbesInTheHistory(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "state")
	write(t, filepath.Join(f.dir, "bin", "codex"), "#!/bin/sh\nexit 1\n", 0o755)
	if code, _, _ := f.run("fallbacks", "check", "--dry-run"); code != exitOK {
		t.Fatalf("dry run: %d", code)
	}
	if j, err := incident.Load(dir); err != nil || len(j.Records) != 0 {
		t.Fatalf("a dry run recorded: %+v %v", j.Records, err)
	}
	if code, _, _ := f.run("fallbacks", "check"); code != exitFailed {
		t.Fatalf("check: %d", code)
	}
	j, err := incident.Load(dir)
	if err != nil || len(j.Records) != 1 {
		t.Fatalf("%+v %v", j.Records, err)
	}
	r := j.Records[0]
	if r.Kind != "probe" || r.Tier != incident.TierCondition || r.Outcome != "probes_saved" || len(r.Probes.Results) != 2 {
		t.Fatalf("%+v", r)
	}
	want := map[string]bool{incident.TargetKey("claude-other-account"): true, incident.TargetKey("codex-main"): false}
	for _, p := range r.Probes.Results {
		ok, known := want[p.TargetKey]
		if !known || p.OK != ok || p.Tier != "manual" {
			t.Fatalf("%+v", p)
		}
	}
	// An exact check of one candidate is recorded with its tier.
	if code, _, _ := f.run("fallbacks", "check", "--id", "claude-other-account", "--exact"); code != exitFailed {
		t.Fatalf("exact check: %d", code)
	}
	j, _ = incident.Load(dir)
	if r := j.Records[len(j.Records)-1]; r.Kind != "probe" || len(r.Probes.Results) != 1 || r.Probes.Results[0].Tier != "exact" || !r.Probes.Results[0].OK || r.Tier != incident.TierActivity {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(dir, incident.FileName))
	for _, private := range []string{"claude-other-account", "codex-main", "thr_"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("%s leaked into the history", private)
		}
	}
	code, out, errs := timelineCLI(t, dir, "--kind", "probe")
	if code != exitOK || errs != "" || !strings.Contains(out, "manual fallbacks check (recorded build ") || !strings.Contains(out, "manual passed") ||
		!strings.Contains(out, "manual failed (") || !strings.Contains(out, "no session was observed and nothing was sent") {
		t.Fatalf("%d %s\n%s", code, errs, out)
	}
	// A target filter finds a manual check by the candidate it probed.
	if got := timelineJSON(t, dir, "--target", "codex-main"); len(got.Events) != 1 || got.Events[0].Incident.Kind != "probe" {
		t.Fatalf("%+v", got.Events)
	}
}
