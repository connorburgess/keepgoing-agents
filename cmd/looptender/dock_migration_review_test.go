package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/testexec"
)

func migrationProjection(t *testing.T, st *statusOut) map[string]any {
	t.Helper()
	l, _ := projectDockLoop(dockInput{Status: st}, dockNow, time.Hour, false)
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	m, ok := fields["migrationReview"].(map[string]any)
	if !ok {
		t.Fatalf("migration review absent from loop projection: %s", b)
	}
	return m
}

func TestDockMigrationReviewProjectionPrivacyAndStates(t *testing.T) {
	g := &control.MigrationSummary{ID: "PRIVATE-REVIEW-ID", Digest: "PRIVATE-DIGEST", Outcome: "review_recorded_not_execution", Coverage: "direct_lanes_only", ObservedAt: dockNow.Add(-365 * 24 * time.Hour), Assignments: 4, Unfinished: 3, Dormant: 1, Unresolved: 1, DirectLanes: 2, AbsentAssignments: 1}
	for _, kind := range []string{"valid", "missing", "unavailable", "binding_changed", "status_unavailable"} {
		t.Run(kind, func(t *testing.T) {
			st := &statusOut{}
			want := kind
			switch kind {
			case "valid":
				st.MigrationReview = g
				want = "available"
			case "missing":
				want = "not_recorded"
			case "status_unavailable":
				st = nil
				want = "unavailable"
			default:
				st.MigrationReviewError = kind
				st.MigrationReview = g // an error must win over a retained summary
			}
			m := migrationProjection(t, st)
			if m["availability"] != want {
				t.Fatalf("%s: %+v", kind, m)
			}
			counts, has := m["counts"].(map[string]any)
			if has != (kind == "valid") {
				t.Fatalf("unknown/absent evidence fabricated counts: %+v", m)
			}
			if !has && m["observedAt"] != nil {
				t.Fatal("unavailable evidence retained a valid-looking timestamp")
			}
			if has && (counts["unfinished"] != float64(3) || counts["dormant"] != float64(1) || counts["unresolved"] != float64(1) || counts["absentAssignments"] != float64(1) || m["observedAt"] != g.ObservedAt.Format(time.RFC3339)) {
				t.Fatal(m)
			}
			b, _ := json.Marshal(m)
			if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "digest") || strings.Contains(string(b), "id") {
				t.Fatalf("private review identity exposed: %s", b)
			}
			hidden, _ := projectDockLoop(dockInput{Status: st}, dockNow, time.Hour, false)
			shown, _ := projectDockLoop(dockInput{Status: st}, dockNow, time.Hour, true)
			if !reflect.DeepEqual(hidden.MigrationReview, shown.MigrationReview) {
				t.Fatal("identifier toggle exposed private migration detail")
			}
		})
	}
	zero := migrationProjection(t, &statusOut{MigrationReview: &control.MigrationSummary{ObservedAt: dockNow}})
	if zero["availability"] != "available" || zero["counts"].(map[string]any)["unfinished"] != float64(0) {
		t.Fatal("valid zero confused with missing evidence")
	}
}

func TestDockMigrationReviewDamagedRecordsStayVisible(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	for _, kind := range []string{"valid", "missing", "corrupt", "copied_runtime", "copied_config", "lost_history"} {
		t.Run(kind, func(t *testing.T) {
			f := newReconcileFixture(t)
			g := f.record(t)
			record := filepath.Join(g.Successor.StateDir, control.MigrationReviewName)
			path := f.dest
			want := "available"
			switch kind {
			case "missing":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
				want = "not_recorded"
			case "corrupt":
				reconcileWrite(t, record, "{", 0600)
				want = "unavailable"
			case "copied_runtime", "copied_config":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(filepath.Dir(path), "copied.yaml")
				if kind == "copied_runtime" {
					raw = []byte(strings.ReplaceAll(string(raw), "./successor-state", "./copied-state"))
					dir := filepath.Join(filepath.Dir(path), "copied-state")
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					b, err := os.ReadFile(record)
					if err != nil {
						t.Fatal(err)
					}
					reconcileWrite(t, filepath.Join(dir, control.MigrationReviewName), string(b), 0600)
				}
				reconcileWrite(t, path, string(raw), 0600)
				want = "binding_changed"
			case "lost_history":
				next := g.Revisions[0]
				next.Revision = 2
				next.Plan.Assignments = []control.MigrationAssignment{}
				g.Revisions = append(g.Revisions, next)
				b, err := json.Marshal(g)
				if err != nil {
					t.Fatal(err)
				}
				reconcileWrite(t, record, string(b), 0600)
				want = "unavailable"
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			registryDir := filepath.Join(t.TempDir(), "registry")
			if _, err := registry.Add(registryDir, cfg.Loop.Name, path, cfg.Loop.StateDir); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" {
				bin := t.TempDir()
				marker := filepath.Join(bin, "called")
				for _, name := range []string{"bb", "git", "codex", "claude", "nonexistent-provider-fixture"} {
					testexec.Script(t, filepath.Join(bin, name), "#!/bin/sh\necho called >> '"+marker+"'\nexit 99\n")
				}
				t.Setenv("PATH", bin)
				t.Cleanup(func() {
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Error("read executed an external command")
					}
				})
			}
			before := treeDigest(t, filepath.Dir(f.dest))
			query := func(context.Context, string, string, bool) daemonStatus { return daemonStatus{State: "skipped"} }
			for i := 0; i < 2; i++ {
				st, err := readStatus(context.Background(), cfg, dockNow, time.Hour, query)
				if err != nil {
					t.Fatal(err)
				}
				m := migrationProjection(t, &st)
				if m["availability"] != want {
					t.Fatalf("%s: %+v", kind, m)
				}
				if kind != "valid" && m["counts"] != nil {
					t.Fatalf("false zero counts for damaged record: %+v", m)
				}
			}
			d := &dockReader{directory: registryDir, refresh: 15 * time.Second, staleAfter: time.Hour, query: query, now: func() time.Time { return dockNow }}
			report := d.read(context.Background(), false)
			loop := dockLoopNamed(t, report, cfg.Loop.Name)
			if loop.MigrationReview.Availability != want {
				t.Fatalf("registered report lost review: %+v", loop.MigrationReview)
			}
			if after := treeDigest(t, filepath.Dir(f.dest)); before != after {
				t.Fatal("read rewrote private files")
			}
		})
	}
}

// A review is informational even on enabled, held, stopped and retired loops.
func TestDockMigrationReviewPreservesControlProjection(t *testing.T) {
	d, _ := dockFixtureReader(t)
	all, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	cfgs := map[string]*config.Config{}
	for _, entry := range all.Entries {
		cfg, err := config.Load(entry.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		cfgs[entry.ID] = cfg
	}
	for _, entry := range all.Entries {
		in := d.readLoop(context.Background(), d.directory, entry, all.Entries, cfgs, dockNow)
		if in.Status == nil {
			t.Fatal("fixture has no status")
		}
		before, need := projectDockLoop(in, dockNow, d.staleAfter, false)
		in.Status.MigrationReview = &control.MigrationSummary{ObservedAt: dockNow, Assignments: 2, Unfinished: 2, Dormant: 2}
		after, afterNeed := projectDockLoop(in, dockNow, d.staleAfter, false)
		if before.State != after.State || !reflect.DeepEqual(before.Can, after.Can) || !reflect.DeepEqual(need, afterNeed) {
			t.Fatalf("%s: review changed control projection", entry.Loop)
		}
		in.Client.Snapshot.ControlState = "superseded"
		in.Status.MigrationReview = nil
		retired, retiredNeed := projectDockLoop(in, dockNow, d.staleAfter, false)
		in.Status.MigrationReview = &control.MigrationSummary{ObservedAt: dockNow, Assignments: 2, Unfinished: 2}
		retiredAfter, retiredAfterNeed := projectDockLoop(in, dockNow, d.staleAfter, false)
		if retiredAfter.Phrase != "Superseded" || !reflect.DeepEqual(retired.Can, retiredAfter.Can) || !reflect.DeepEqual(retiredNeed, retiredAfterNeed) || slices.Contains(retiredAfter.Can, "resume") {
			t.Fatal("review changed retirement or offered resume")
		}
	}
}

// Optional canned report for the loopback-only browser fixture. No live data.
func TestDockMigrationReviewBrowserFixture(t *testing.T) {
	path := os.Getenv("KEEPGOING_MIGRATION_BROWSER_REPORT")
	if path == "" {
		t.Skip("set KEEPGOING_MIGRATION_BROWSER_REPORT to write fictional browser JSON")
	}
	d, _ := dockFixtureReader(t)
	report := d.read(context.Background(), false)
	for i := range report.Loops {
		l := &report.Loops[i]
		switch l.Name {
		case "docs-site-refresh":
			l.MigrationReview = dockMigrationReviewOf(&statusOut{MigrationReview: &control.MigrationSummary{ObservedAt: dockNow.Add(-365 * 24 * time.Hour), Assignments: 4, Unfinished: 3, Dormant: 1, Unresolved: 1, DirectLanes: 2, AbsentAssignments: 1}})
		case "billing-api-migration":
			l.MigrationReview = dockMigrationReviewOf(&statusOut{MigrationReviewError: "binding_changed"})
		case "mobile-onboarding":
			l.MigrationReview = dockMigrationReviewOf(&statusOut{MigrationReviewError: "unavailable"})
		case "search-indexer":
			l.MigrationReview = dockMigrationReviewOf(&statusOut{MigrationReview: &control.MigrationSummary{ObservedAt: dockNow, Assignments: 2}})
		}
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
