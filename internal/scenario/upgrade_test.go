package scenario

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// releases are the released versions with a state fixture, oldest first.
var releases = []string{"v0.1.0", "v0.1.1", "v0.1.2", "v0.1.3", "v0.1.4", "v0.1.5"}

// upgradeStart is after every time in the fixtures, including the wall-clock
// times of the day they were generated.
var upgradeStart = time.Date(2027, 1, 4, 9, 0, 0, 0, time.UTC)

// Adopt sets the world up as a loop that a released version has been
// running: that release's state directory, and the configuration it ran
// with. The session is where the fixture's journey left it: in error after
// an unclassified failure, with the two log events the release had read.
func (w *World) Adopt(release string) {
	w.t.Helper()
	dir := filepath.Join(w.Root, "setup")
	src := filepath.Join("testdata", "released", release, "state")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dst := filepath.Join(dir, "state", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	cfg := fmt.Sprintf("version: 1\nloop:\n  name: example-loop\n  state_dir: state\n  adapter: bb\n  provider: claude-code\n  bb: {thread: %s, cli: %q}\n  prompt: %s\n  report: {events: [await_user, complete, error]}\n",
		primaryThread, w.BinPath("bb"), scenarioTask)
	if err := os.WriteFile(w.Config, []byte(cfg), 0o600); err != nil {
		w.t.Fatal(err)
	}
	th := w.Thread(primaryThread)
	th.events = []map[string]any{
		{"seq": 1, "type": "turn/started", "createdAt": 1772442060000, "data": map[string]any{}},
		{"seq": 2, "type": "provider/error", "createdAt": 1772442600000, "data": map[string]any{"willRetry": false, "errorInfo": map[string]any{"category": "unknown", "httpStatusCode": nil, "providerCode": "unknown"}}},
	}
	th.writeLog()
	th.Set("error")
}

// TestScenarioUpgradeFromEachRelease runs today's build on the state each
// released version left behind: a loop held for a person, with a send
// history and one re-prompt spent. The upgrade must keep the hold and its
// note, send nothing while held, keep the earlier history readable, and after
// `resume` carry on from where the release stopped without repeating a send.
func TestScenarioUpgradeFromEachRelease(t *testing.T) {
	t.Parallel()
	for _, release := range releases {
		t.Run(release, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, upgradeStart)
			w.Adopt(release)
			primary := w.Thread(primaryThread)

			// The first check by the new build honours the hold: no bb
			// command, no send, no report.
			s := w.Tick()
			w.Check(s, "the hold survives the upgrade", all(
				action(s, "none"), reasonIs(s, "awaiting user"), nothingSent(s), noCalls(s), noReports(s), holds(s, "awaitingUser"),
				check(s.Status.Control.Note == "Fictional: choose the release name", "note %q", s.Status.Control.Note),
				savedCounters(s, 1, "2026-03-02T09:11:00Z"),
			))
			// Releases from v0.1.3 kept an incident journal; its records are
			// still in the timeline after the new build has written to it.
			wantHistory := 1
			if release >= "v0.1.3" {
				wantHistory = 2
			}
			w.Check(s, "earlier history is still readable", check(s.Timeline.Sources.retained("incidents.json") >= wantHistory,
				"timeline retains %d incident records, want at least %d", s.Timeline.Sources.retained("incidents.json"), wantHistory))

			// The person resumes. The session is still in error, so the next
			// check recovers it once, by retrying the failed turn.
			s = w.Control("resume")
			w.Check(s, "resume", all(exit(s, 0), holds(s), nothingSent(s)))
			w.Advance(time.Minute)
			s = w.Tick()
			w.Check(s, "one recovery after resume", all(
				action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread}), holds(s), noReports(s),
			))
			w.Advance(5 * time.Minute)
			s = w.Tick()
			w.Check(s, "left to work", all(action(s, "none"), reasonIs(s, "active"), nothingSent(s)))

			// The turn finishes with progress, the loop carries on, and the
			// agent completes the task.
			w.Commit("feat: fictional work after the upgrade")
			primary.FinishTurn()
			w.Advance(5 * time.Minute)
			s = w.Tick()
			w.Check(s, "wake after progress", all(action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"}), sentContains(s, scenarioTask)))
			w.Advance(10 * time.Minute)
			primary.FinishTurn()
			s = w.AgentRuns("complete", "--note", "Fictional task verified")
			w.Check(s, "complete", all(exit(s, 0), holds(s, "completed"), reported(s, "keepgoing.complete")))
			w.Advance(5 * time.Minute)
			s = w.Tick()
			w.Check(s, "completed", all(action(s, "none"), reasonIs(s, "completed"), nothingSent(s), noCalls(s)))
			w.Check(s, "two sends in the whole journey", journeySends(w, primaryThread, "retry", "tell"))
		})
	}
}

// savedCounters: the re-prompt count and last send time the release saved
// are still what status reports.
func savedCounters(s *Step, reprompts int, lastWake string) error {
	var st struct {
		State struct {
			Reprompts  int       `json:"reprompts"`
			LastWakeAt time.Time `json:"lastWakeAt"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(s.Status.Raw), &st); err != nil {
		return err
	}
	want, _ := time.Parse(time.RFC3339, lastWake)
	return check(st.State.Reprompts == reprompts && st.State.LastWakeAt.Equal(want),
		"saved counters: %d re-prompts, last send %s; want %d, %s", st.State.Reprompts, st.State.LastWakeAt.Format(time.RFC3339), reprompts, lastWake)
}

// TestScenarioFilesFromANewerBuild is the rollback direction: a state
// directory that a newer build has written to, run under a build that does
// not know its formats. Old releases cannot be built in CI, so this build
// plays the older one: the files are given a schema or record kind it does
// not know, which is what its own files become under any earlier build. The
// messages are the ones v0.1.5 prints against today's state directory; see
// "Upgrading and rolling back" in docs/operations.md.
func TestScenarioFilesFromANewerBuild(t *testing.T) {
	t.Parallel()
	// adopted is a loop this build has taken over from v0.1.5 and resumed,
	// with a session still in error: the next check has something to do.
	adopted := func(t *testing.T) (*World, string) {
		w := NewWorld(t, upgradeStart)
		w.Adopt("v0.1.5")
		w.Tick()
		s := w.Control("resume")
		w.Check(s, "resume", all(exit(s, 0), holds(s)))
		w.Advance(time.Minute)
		return w, filepath.Join(filepath.Dir(w.Config), "state")
	}
	sends := func(w *World) []Send {
		var all []Send
		readLines(w.t, filepath.Join(w.world, "sends.jsonl"), &all)
		return all
	}

	t.Run("incident journal with a newer schema", func(t *testing.T) {
		t.Parallel()
		w, state := adopted(t)
		rewriteJSON(t, filepath.Join(state, "incidents.json"), func(v map[string]any) {
			v["schemaVersion"] = v["schemaVersion"].(float64) + 1
		})
		journal, _ := os.ReadFile(filepath.Join(state, "incidents.json"))

		// The check still does its work: the failed turn is retried once.
		// But it cannot record the check in a journal it does not
		// understand, so it exits 1, on every check, for as long as the
		// journal stays. A scheduler that counts failures stops calling it.
		r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
		got := sends(w)
		if r.Code != 1 || !strings.Contains(r.Stderr, "tick: incident history unavailable; check private state storage") ||
			!strings.Contains(r.Stdout, `"warnings":["incident history write failed; diagnostic evidence is incomplete"]`) ||
			!strings.Contains(r.Stdout, `"action":"reprompt"`) || len(got) != 1 || got[0].Kind != "retry" {
			t.Fatalf("check under a newer journal: exit %d, sent %s\n%s%s", r.Code, describeSends(got), r.Stdout, r.Stderr)
		}
		// The newer journal is left exactly as it was.
		if after, _ := os.ReadFile(filepath.Join(state, "incidents.json")); string(after) != string(journal) {
			t.Fatal("the newer journal was rewritten")
		}
		// status still works; timeline says its source is unavailable.
		if r := w.Run("status", "-c", w.Config, "--json", "--now", w.stamp()); r.Code != 0 {
			t.Fatalf("status: exit %d %s", r.Code, r.Stderr)
		}
		tl := w.Run("timeline", "-c", w.Config)
		if tl.Code != 1 || !strings.Contains(tl.Stdout, "source incidents.json: unavailable; schema 0; retained 0; dropped 0") {
			t.Fatalf("timeline: exit %d\n%s", tl.Code, tl.Stdout)
		}
		// A hold is still saved, with a warning and exit 1, and it holds:
		// the next check sends nothing.
		p := w.Run("pause", "-c", w.Config)
		if p.Code != 1 || !strings.Contains(p.Stdout, "example-loop: paused") ||
			!strings.Contains(p.Stderr, "warning: control saved; incident history write failed; diagnostic evidence is incomplete") {
			t.Fatalf("pause: exit %d\n%s%s", p.Code, p.Stdout, p.Stderr)
		}
		w.Advance(20 * time.Minute)
		r = w.Run("tick", "-c", w.Config, "--now", w.stamp())
		if r.Code != 1 || !strings.Contains(r.Stdout, `"reason":"paused"`) || len(sends(w)) != 1 {
			t.Fatalf("paused check: exit %d, %d sends\n%s%s", r.Code, len(sends(w)), r.Stdout, r.Stderr)
		}
	})

	t.Run("run graph with a newer record kind", func(t *testing.T) {
		t.Parallel()
		w, state := adopted(t)
		rewriteJSON(t, filepath.Join(state, "graph.json"), func(v map[string]any) {
			runs := v["runs"].([]any)
			runs[0].(map[string]any)["origin"] = "origin_from_a_newer_build"
		})
		graph, _ := os.ReadFile(filepath.Join(state, "graph.json"))

		// The graph is diagnostic only: the check does the same work, exits
		// 0 and carries one warning. The graph is left as it was.
		r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
		got := sends(w)
		if r.Code != 0 || !strings.Contains(r.Stdout, `"action":"reprompt"`) || len(got) != 1 || got[0].Kind != "retry" ||
			!strings.Contains(r.Stdout, graphWarning) {
			t.Fatalf("check under a newer graph: exit %d, sent %s\n%s%s", r.Code, describeSends(got), r.Stdout, r.Stderr)
		}
		if after, _ := os.ReadFile(filepath.Join(state, "graph.json")); string(after) != string(graph) {
			t.Fatal("the newer graph was rewritten")
		}
		// runs reads nothing from it and exits 1. It calls the graph
		// corrupt: this build cannot tell a record kind it does not know from
		// damage, so after a rollback "corrupt" may only mean "newer".
		rn := w.Run("runs", "-c", w.Config, "--json")
		var report struct {
			Loops []struct {
				Graph struct {
					Availability string `json:"availability"`
				} `json:"graph"`
				Current any `json:"current"`
			} `json:"loops"`
		}
		if err := json.Unmarshal([]byte(rn.Stdout), &report); err != nil || len(report.Loops) != 1 {
			t.Fatalf("runs: exit %d %v\n%s", rn.Code, err, rn.Stdout)
		}
		if g := report.Loops[0].Graph; rn.Code != 1 || g.Availability != "corrupt" || report.Loops[0].Current != nil {
			t.Fatalf("runs under a newer graph: exit %d, graph %q, current %v", rn.Code, g.Availability, report.Loops[0].Current)
		}
		if text := w.Run("runs", "-c", w.Config); text.Code != 1 || !strings.Contains(text.Stdout, "graph: corrupt") || !strings.Contains(text.Stdout, "waiting on: unknown (graph_unavailable)") {
			t.Fatalf("runs text: exit %d\n%s", text.Code, text.Stdout)
		}
	})
}

// graphWarning is what a check says when it cannot use the run graph.
const graphWarning = `"warnings":["run reference record unavailable; discovery evidence is incomplete"]`

// rewriteJSON edits a JSON object file in place.
func rewriteJSON(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	edit(v)
	if b, err = json.Marshal(v); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
