package ciwait

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/netretry"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

type fakeGH struct {
	lists []string // successive `gh run list` responses
	n     int
	jobs  map[string]string
}

func (f *fakeGH) gh(_ context.Context, args ...string) ([]byte, error) {
	switch {
	case args[0] == "pr":
		return []byte(`{"headRefName":"cb/example","headRefOid":"` + sha + `"}`), nil
	case args[0] == "run" && args[1] == "list":
		i := f.n
		if i >= len(f.lists) {
			i = len(f.lists) - 1
		}
		f.n++
		return []byte(f.lists[i]), nil
	case args[0] == "run" && args[1] == "view":
		return []byte(f.jobs[args[2]]), nil
	}
	return nil, fmt.Errorf("unexpected gh %v", args)
}

func run(id int, workflow, status, conclusion, head string) string {
	return fmt.Sprintf(`{"databaseId":%d,"headSha":%q,"status":%q,"conclusion":%q,"workflowName":%q,"url":"https://example.com/runs/%d"}`, id, head, status, conclusion, workflow, id)
}

func opts() Options {
	clock := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	return Options{
		Repo: "example-org/example-app", PR: 7, Interval: time.Minute, Timeout: 30 * time.Minute, NoRunsAfter: 3 * time.Minute,
		Net:   netretry.Options{Online: func(context.Context) bool { return true }},
		Sleep: func(context.Context, time.Duration) { clock = clock.Add(time.Minute) },
		Now:   func() time.Time { return clock },
	}
}

func TestFollowsNewerRunAfterCancellationAndWaitsForAllWorkflows(t *testing.T) {
	other := strings.Repeat("f", 40)
	f := &fakeGH{
		lists: []string{
			"[" + run(10, "Checks", "in_progress", "", sha) + "," + run(9, "Checks", "completed", "success", other) + "]",
			"[" + run(11, "Checks", "queued", "", sha) + "," + run(10, "Checks", "completed", "cancelled", sha) + "," + run(12, "Lint", "in_progress", "", sha) + "]",
			"[" + run(11, "Checks", "completed", "success", sha) + "," + run(10, "Checks", "completed", "cancelled", sha) + "," + run(12, "Lint", "completed", "success", sha) + "]",
		},
		jobs: map[string]string{"11": `{"jobs":[{"name":"Build and test","conclusion":"success"}]}`, "12": `{"jobs":[{"name":"gofmt","conclusion":"success"}]}`},
	}
	rep, err := Wait(context.Background(), f.gh, opts())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Conclusion != "success" || len(rep.Runs) != 2 || rep.Runs[0].ID != 11 || rep.Runs[0].Superseded[0] != 10 || rep.Runs[0].Jobs[0].Name != "Build and test" {
		t.Fatalf("%+v", rep)
	}
	if rep.ExitCode() != 0 || rep.Branch != "cb/example" {
		t.Errorf("exit=%d branch=%s", rep.ExitCode(), rep.Branch)
	}
}

func TestFailureAndNoRunsAndTimeout(t *testing.T) {
	f := &fakeGH{lists: []string{"[" + run(5, "Checks", "completed", "failure", sha) + "]"}, jobs: map[string]string{"5": `{"jobs":[{"name":"Build","conclusion":"failure"}]}`}}
	if rep, _ := Wait(context.Background(), f.gh, opts()); rep.Conclusion != "failure" || rep.ExitCode() != 1 {
		t.Errorf("failure: %+v", rep)
	}
	f = &fakeGH{lists: []string{"[]"}}
	if rep, _ := Wait(context.Background(), f.gh, opts()); rep.Conclusion != "no_runs" || rep.ExitCode() != ExitNoRuns {
		t.Errorf("no runs: %+v", rep)
	}
	f = &fakeGH{lists: []string{"[" + run(5, "Checks", "in_progress", "", sha) + "]"}}
	if rep, _ := Wait(context.Background(), f.gh, opts()); rep.Conclusion != "timeout" || rep.ExitCode() != ExitTimeout {
		t.Errorf("timeout: %+v", rep)
	}
}

func TestRequiresCommit(t *testing.T) {
	o := opts()
	o.PR = 0
	if _, err := Wait(context.Background(), (&fakeGH{}).gh, o); err == nil {
		t.Error("expected an error without --pr or --sha/--branch")
	}
}
