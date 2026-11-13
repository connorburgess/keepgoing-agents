package bb

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func TestSpawnRunsOneCommandWithThePromptOnStdin(t *testing.T) {
	a, calls := newFake(`{}`, `[]`, false)
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{stdin, args})
		return []byte(`{"id":"thr_new_example","status":"active","title":"private fixture text","projectId":"proj_example","environmentId":"env_example"}`), nil
	}
	r := SpawnRequest{Place: Place{Project: "proj_example", Environment: "env_example"}, Provider: "codex", Model: "example-model", Reasoning: "high", Permission: "accept-edits",
		Title: "keepgoing: example-loop (continued, 0a1b2c3d)", Prompt: "fictional brief\nsecond line"}
	id, err := a.Spawn(context.Background(), r)
	if err != nil || id != "thr_new_example" || len(*calls) != 1 {
		t.Fatalf("%q %v %d calls", id, err, len(*calls))
	}
	want := []string{"thread", "spawn", "--project", "proj_example", "--environment", "env_example", "--provider", "codex", "--permission-mode", "accept-edits", "--title", r.Title, "--prompt-file", "-", "--json", "--model", "example-model", "--reasoning-level", "high"}
	if got := (*calls)[0]; !reflect.DeepEqual(got.args, want) || got.stdin != r.Prompt {
		t.Fatalf("args %v stdin %q", got.args, got.stdin)
	}
	// The prompt never appears in argv, and no parent, fork or new environment is asked for.
	for _, arg := range (*calls)[0].args {
		if strings.Contains(arg, "fictional brief") || arg == "--parent-thread" || arg == "--parent-self" || arg == "--origin-kind" || arg == "--new-environment" || arg == "--source-thread" {
			t.Fatalf("unexpected argument %q", arg)
		}
	}
	// Model and reasoning are optional.
	r.Model, r.Reasoning = "", ""
	a.Spawn(context.Background(), r)
	if got := (*calls)[1].args; len(got) != 15 {
		t.Fatalf("args %v", got)
	}
}

func TestSpawnOutcomes(t *testing.T) {
	ok := SpawnRequest{Place: Place{Project: "proj_example", Environment: "env_example"}, Provider: "codex", Permission: "accept-edits", Title: "keepgoing: example-loop (continued, 0a1b2c3d)", Prompt: "fictional brief"}
	for _, tc := range []struct {
		name   string
		req    SpawnRequest
		out    string
		fail   bool
		want   error
		wantID string
		runs   int
	}{
		{"created", ok, `{"id":"thr_new_example"}`, false, nil, "thr_new_example", 1},
		// Only the bb CLI's own invocation-mistake codes prove nothing was sent.
		{"the CLI rejected the invocation", ok, `{"ok":false,"error":{"code":"unknown_option","message":"private fixture text"}}`, true, ErrSpawnRefused, "", 1},
		{"the CLI rejected a value", ok, `{"ok":false,"error":{"code":"invalid_value"}}`, true, ErrSpawnRefused, "", 1},
		// A server error says nothing about whether the thread was created first.
		{"a server error", ok, `{"ok":false,"error":{"code":"provider_unavailable","message":"private fixture text"}}`, true, ErrSpawnUncertain, "", 1},
		{"a server error with exit 0", ok, `{"ok":false,"error":{"code":"http_500"}}`, false, ErrSpawnUncertain, "", 1},
		{"an error with no known code", ok, `{"ok":false,"error":{"code":"error"}}`, true, ErrSpawnUncertain, "", 1},
		{"killed with no output", ok, ``, true, ErrSpawnUncertain, "", 1},
		{"output that is not JSON", ok, `private fixture text`, false, ErrSpawnUncertain, "", 1},
		{"an ok envelope without a thread", ok, `{"ok":true}`, false, ErrSpawnUncertain, "", 1},
		{"an id that is not a thread id", ok, `{"id":"--help"}`, false, ErrSpawnUncertain, "", 1},
		{"a created thread reported with a failing exit", ok, `{"id":"thr_new_example"}`, true, ErrSpawnUncertain, "", 1},
		{"no project", SpawnRequest{Place: Place{Environment: "env_example"}, Provider: "codex", Permission: "accept-edits", Title: "t", Prompt: "p"}, ``, false, ErrSpawnRefused, "", 0},
		{"unsafe environment", SpawnRequest{Place: Place{Project: "proj_example", Environment: "--machine"}, Provider: "codex", Permission: "accept-edits", Title: "t", Prompt: "p"}, ``, false, ErrSpawnRefused, "", 0},
		{"no provider", SpawnRequest{Place: ok.Place, Permission: "accept-edits", Title: "t", Prompt: "p"}, ``, false, ErrSpawnRefused, "", 0},
		{"no prompt", SpawnRequest{Place: ok.Place, Provider: "codex", Permission: "accept-edits", Title: "t"}, ``, false, ErrSpawnRefused, "", 0},
		{"no permission mode: never left to a project default", SpawnRequest{Place: ok.Place, Provider: "codex", Title: "t", Prompt: "p"}, ``, false, ErrSpawnRefused, "", 0},
		{"an unknown permission mode", SpawnRequest{Place: ok.Place, Provider: "codex", Permission: "everything", Title: "t", Prompt: "p"}, ``, false, ErrSpawnRefused, "", 0},
	} {
		a, _ := newFake(`{}`, `[]`, false)
		runs := 0
		a.run = func(context.Context, string, ...string) ([]byte, error) {
			runs++
			if tc.fail {
				return []byte(tc.out), errors.New("exit 1")
			}
			return []byte(tc.out), nil
		}
		id, err := a.Spawn(context.Background(), tc.req)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) || id != tc.wantID || runs != tc.runs {
			t.Errorf("%s: id=%q err=%v runs=%d", tc.name, id, err, runs)
		}
	}
}

func TestPlaceOfReadsOnlyTheProjectAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, thread, out string
		fail              bool
		want              Place
		wantErr           bool
	}{
		{"placed", "thr_example123", `{"thread":{"id":"thr_example123","projectId":"proj_example","environmentId":"env_example","title":"private fixture text"}}`, false, Place{"proj_example", "env_example"}, false},
		{"another thread's reply", "thr_example123", `{"thread":{"id":"thr_other","projectId":"proj_example","environmentId":"env_example"}}`, false, Place{}, true},
		{"no environment", "thr_example123", `{"thread":{"id":"thr_example123","projectId":"proj_example"}}`, false, Place{}, true},
		{"unsafe project", "thr_example123", `{"thread":{"id":"thr_example123","projectId":"--all","environmentId":"env_example"}}`, false, Place{}, true},
		{"error envelope", "thr_example123", `{"ok":false,"error":{"code":"thread_not_found"}}`, true, Place{}, true},
		{"command failure", "thr_example123", ``, true, Place{}, true},
		{"not JSON", "thr_example123", `private fixture text`, false, Place{}, true},
		{"unsafe thread id is never run", "--self", ``, false, Place{}, true},
	} {
		a, _ := newFake(`{}`, `[]`, false)
		a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
			if tc.thread == "--self" {
				t.Fatal("ran bb with an unsafe thread id")
			}
			if stdin != "" || !reflect.DeepEqual(args, []string{"thread", "show", tc.thread, "--json"}) {
				t.Fatalf("unexpected command %v", args)
			}
			if tc.fail {
				return []byte(tc.out), errors.New("exit 1")
			}
			return []byte(tc.out), nil
		}
		got, err := a.PlaceOf(context.Background(), tc.thread)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
		if err != nil && strings.Contains(err.Error(), "private fixture text") {
			t.Errorf("%s: error carries provider text", tc.name)
		}
	}
}

func TestFindByTitleMatchesTheWholeTitleOnly(t *testing.T) {
	title := "keepgoing: example-loop (continued, 0a1b2c3d)"
	place := Place{Project: "proj_example", Environment: "env_example"}
	for _, tc := range []struct {
		name, out string
		fail      bool
		want      []string
		wantErr   bool
	}{
		{"no threads", `[]`, false, []string{}, false},
		{"one match among others", `[{"id":"thr_a","title":"another thread"},{"id":"thr_b","title":"` + title + `"},{"id":"thr_c","title":null}]`, false, []string{"thr_b"}, false},
		{"a longer or shorter title is not a match", `[{"id":"thr_a","title":"` + title + ` copy"},{"id":"thr_b","title":"keepgoing: example-loop (continued, 0a1b2c3"}]`, false, []string{}, false},
		{"two matches, sorted", `[{"id":"thr_z","title":"` + title + `"},{"id":"thr_a","title":"` + title + `"}]`, false, []string{"thr_a", "thr_z"}, false},
		{"a match with an unusable id", `[{"id":"not a thread","title":"` + title + `"}]`, false, nil, true},
		{"null is not an empty listing", `null`, false, nil, true},
		{"an envelope is not a listing", `{"ok":true}`, false, nil, true},
		{"error envelope", `{"ok":false,"error":{"code":"unavailable"}}`, true, nil, true},
		{"command failure", `[]`, true, nil, true},
	} {
		a, _ := newFake(`{}`, `[]`, false)
		a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
			if stdin != "" || !reflect.DeepEqual(args, []string{"thread", "list", "--project", "proj_example", "--environment", "env_example", "--include-hidden", "--json"}) {
				t.Fatalf("unexpected command %v", args)
			}
			if tc.fail {
				return []byte(tc.out), errors.New("exit 1")
			}
			return []byte(tc.out), nil
		}
		got, err := a.FindByTitle(context.Background(), place, title)
		if (err != nil) != tc.wantErr || (!tc.wantErr && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: %v %v", tc.name, got, err)
		}
	}
	a, _ := newFake(`{}`, `[]`, false)
	a.run = func(context.Context, string, ...string) ([]byte, error) { t.Fatal("ran bb"); return nil, nil }
	for _, bad := range []Place{{}, {Project: "proj_example"}, {Project: "--all", Environment: "env_example"}} {
		if _, err := a.FindByTitle(context.Background(), bad, title); err == nil {
			t.Errorf("place %+v accepted", bad)
		}
	}
	if _, err := a.FindByTitle(context.Background(), place, ""); err == nil {
		t.Error("empty title accepted")
	}
}

// A refusal is believed only from a bb that ended on its own. The same
// invocation-mistake envelope from a command that was killed, timed out or
// cancelled is an unknown outcome, and so is a bb that could not be started.
func TestSpawnRefusalNeedsACommandThatEndedOnItsOwn(t *testing.T) {
	req := SpawnRequest{Place: Place{Project: "proj_example", Environment: "env_example"}, Provider: "codex", Permission: "accept-edits", Title: "t", Prompt: "p"}
	const refusal = `{"ok":false,"error":{"code":"invalid_value"}}`
	expired, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		out  string
		err  error
		want error
	}{
		{"exit 1", context.Background(), refusal, errors.New("exit status 1"), ErrSpawnRefused},
		{"exit 0", context.Background(), refusal, nil, ErrSpawnRefused},
		{"stopped at the command's limit", context.Background(), refusal, fmt.Errorf("%w: %w", errStopped, errors.New("signal: killed")), ErrSpawnUncertain},
		{"deadline exceeded", context.Background(), refusal, context.DeadlineExceeded, ErrSpawnUncertain},
		{"the check's own limit passed", expired, refusal, errors.New("exit status 1"), ErrSpawnUncertain},
		{"not started, with an envelope", context.Background(), refusal, &exec.Error{Name: "bb", Err: exec.ErrNotFound}, ErrSpawnUncertain},
		{"not started", context.Background(), ``, &exec.Error{Name: "bb", Err: exec.ErrNotFound}, ErrSpawnNotStarted},
	} {
		a, _ := newFake(`{}`, `[]`, false)
		a.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.out), tc.err }
		_, err := a.Spawn(tc.ctx, req)
		if !errors.Is(err, tc.want) || (tc.want != ErrSpawnRefused && errors.Is(err, ErrSpawnRefused)) || !errors.Is(tc.want, ErrSpawnUncertain) == errors.Is(err, ErrSpawnUncertain) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// The real runner with a bb that does not exist.
	missing := New(config.BB{CLI: filepath.Join(t.TempDir(), "no-such-bb")})
	if _, err := missing.Spawn(context.Background(), req); !errors.Is(err, ErrSpawnNotStarted) || !errors.Is(err, ErrSpawnUncertain) {
		t.Fatalf("missing bb: %v", err)
	}
}
