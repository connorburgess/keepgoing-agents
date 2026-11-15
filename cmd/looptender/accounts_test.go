package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func accountFixture(t *testing.T, provider string) (string, accounts.Entry) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "catalog")
	if err := os.WriteFile(filepath.Join(root, "fixture-cli"), []byte("fictional stand-in"), 0700); err != nil {
		t.Fatal(err)
	}
	var e accounts.Entry
	err = accounts.Update(dir, func(f *accounts.File) error {
		e = accounts.NewEntry(*f, accounts.Entry{Label: "Example workshop", Provider: provider, Mode: "inherited", CLI: filepath.Join(root, "fixture-cli"), Workdir: root}, "thr_example_actual", time.Now().UTC(), os.Environ())
		f.Entries = append(f.Entries, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, e
}
func TestAccountsFixedCommandsContextAndCaller(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	for _, provider := range []string{"claude-code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir, e := accountFixture(t, provider)
			calls := [][]string{}
			run := func(ctx context.Context, cli string, args, environment []string, cwd string) ([]byte, error) {
				if cli != e.CLI || cwd != e.Workdir || !slices.Contains(environment, "BB_THREAD_ID=thr_example_actual") {
					t.Fatal("execution context or caller changed")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded call")
				}
				calls = append(calls, args)
				if args[0] == "--version" {
					if provider == "codex" {
						return []byte("codex-cli 0.77.0"), nil
					}
					return []byte("2.1.318 (Claude Code)"), nil
				}
				return []byte("{\"loggedIn\":true,\"subscriptionType\":\"max\",\"email\":\"private@example.com\",\"token\":\"fictional-sensitive-value\"}"), nil
			}
			if err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", run, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			want := []string{"auth", "status", "--json"}
			if provider == "codex" {
				want = []string{"login", "status"}
			}
			if len(calls) != 2 || !reflect.DeepEqual(calls[1], want) {
				t.Fatalf("unexpected calls %v", calls)
			}
			f, _ := accounts.Load(dir)
			a := f.Entries[0]
			if a.Attempt.Auth != "reported_present" || a.Attempt.Caller != "thr_example_actual" || a.UpdatedBy != "thr_example_actual" {
				t.Fatal("missing provenance")
			}
			out, _ := json.Marshal(accounts.Project(f, time.Now()))
			for _, secret := range []string{"private@example.com", "fictional-sensitive-value", "thr_example_actual", e.CLI} {
				if bytes.Contains(out, []byte(secret)) {
					t.Fatal("private projection")
				}
			}
		})
	}
}
func TestAccountsStatusFailureAndConcurrentRevision(t *testing.T) {
	for _, kind := range []string{"timeout", "cancel", "unknown_version", "bad_status", "changed", "env_changed", "busy"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := accountFixture(t, "claude-code")
			calls := 0
			var guard interface{ Release() error }
			if kind == "busy" {
				g, err := accounts.CheckGuard(dir, e.ID)
				if err != nil {
					t.Fatal(err)
				}
				guard = g
				defer guard.Release()
			}
			run := func(ctx context.Context, cli string, args, environment []string, cwd string) ([]byte, error) {
				calls++
				if kind == "timeout" {
					return nil, context.DeadlineExceeded
				}
				if kind == "cancel" {
					return nil, context.Canceled
				}
				if kind == "unknown_version" {
					return []byte("unexpected secret output"), nil
				}
				if args[0] == "--version" {
					return []byte("2.1.318 (Claude Code)"), nil
				}
				if kind == "changed" {
					_ = accounts.Update(dir, func(f *accounts.File) error {
						f.Entries[0] = accounts.Revise(*f, e, e, "thr_example_other", time.Now(), os.Environ())
						return nil
					})
				}
				if kind == "env_changed" {
					t.Setenv("ANTHROPIC_API_KEY", "changed-fictional-key")
				}
				if kind == "bad_status" {
					return []byte("{\"email\":\"private@example.com\"}"), nil
				}
				return []byte("{\"loggedIn\":true}"), nil
			}
			err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", run, time.Now())
			if kind == "changed" || kind == "env_changed" {
				if !errors.Is(err, accounts.ErrChanged) {
					t.Fatal("changed context accepted")
				}
				return
			}
			if kind == "busy" {
				if err == nil || calls != 0 {
					t.Fatal("overlapping check")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f, _ := accounts.Load(dir)
			if f.Entries[0].Attempt.Auth != "unavailable" || f.Entries[0].Last != nil {
				t.Fatal("failure became login")
			}
			if kind == "unknown_version" && calls != 1 {
				t.Fatal("status ran for unknown version")
			}
		})
	}
}
func TestAccountsChangedEnvironmentNeverRunsProvider(t *testing.T) {
	dir, e := accountFixture(t, "claude-code")
	t.Setenv("ANTHROPIC_API_KEY", "new-fictional-key")
	err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", func(context.Context, string, []string, []string, string) ([]byte, error) {
		t.Fatal("changed context invoked")
		return nil, nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f, _ := accounts.Load(dir)
	if f.Entries[0].Attempt.Reason != "context_changed" {
		t.Fatal("context mismatch lost")
	}
}
func TestAccountsRefreshDoesNotExecuteSelectedCLI(t *testing.T) {
	reader, root := dockFixtureReader(t)
	dir, e := accountFixture(t, "claude-code")
	reader.accountsDir = dir
	// A non-executable selected path would make any provider call fail. Reads
	// still expose only the retained selection and never invoke a status runner.
	_ = e
	_ = root
	a := reader.read(context.Background(), false)
	b := reader.read(context.Background(), true)
	if a.Accounts == nil || len(a.Accounts.Cards) != 1 || b.Accounts.Cards[0].Auth.State != "unchecked" {
		t.Fatal("catalog not projected independently")
	}
	if a.Accounts.Cards[0].Uses != "unavailable" {
		t.Fatal("invented use association")
	}
	raw, _ := json.Marshal(a.Accounts)
	if strings.Contains(string(raw), dir) {
		t.Fatal("private path exposed")
	}
}
func TestAccountsCLIRefusesUnknownFlagsAndRecordsCaller(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dir := filepath.Join(root, "catalog")
	var out, errOut bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &errOut}
	code := accountsCommand(e, []string{"add", "--directory", dir, "--provider", "bb", "--selection", "externally_managed", "--label", "Example external", "--json"}, nil)
	if code != 0 {
		t.Fatalf("add failed %s", errOut.String())
	}
	f, err := accounts.Load(dir)
	if err != nil || f.Entries[0].UpdatedBy != "thr_example_actual" {
		t.Fatal("caller lost")
	}
	if accountsCommand(e, []string{"list", "--directory", dir, "--cli", "arbitrary"}, nil) != exitUsage {
		t.Fatal("arbitrary flag accepted")
	}
}

// Opt-in fictional browser fixture; no production runtime paths or provider calls.
func TestAccountsBrowserFixture(t *testing.T) {
	destination := os.Getenv("LOOPTENDER_ACCOUNT_BROWSER_FIXTURE")
	if destination == "" {
		t.Skip("browser fixture not requested")
	}
	reader, _ := dockFixtureReader(t)
	dir, e := accountFixture(t, "claude-code")
	reader.accountsDir = dir
	if err := accounts.Update(dir, func(f *accounts.File) error {
		at := reader.clock()
		f.Entries[0].Label = "Example studio"
		o := accounts.Observation{Revision: e.Revision, Context: e.Context, At: at.Add(-time.Minute), Auth: "reported_present", Plan: "max", Version: "2.1.318"}
		f.Entries[0].Attempt = &o
		f.Entries[0].Last = &o
		second := accounts.NewEntry(*f, e, "thr_example_actual", at, os.Environ())
		second.Label = "Example workshop with a deliberately long name"
		old := o
		old.Revision = second.Revision
		old.Context = second.Context
		old.At = at.Add(-time.Hour)
		old.Plan = ""
		failed := old
		failed.At = at
		failed.Auth = "unavailable"
		failed.Reason = "timeout"
		second.Last = &old
		second.Attempt = &failed
		external := accounts.NewEntry(*f, accounts.Entry{Label: "Example external", Provider: "bb", Mode: "externally_managed"}, "", at, nil)
		f.Entries = append(f.Entries, second, external)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := reader.read(context.Background(), false)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(destination, raw, 0600) != nil {
		t.Fatal("fixture write")
	}
}

func TestAccountsFailureKeepsOriginalObservation(t *testing.T) {
	dir, e := accountFixture(t, "claude-code")
	first := time.Now().UTC().Add(-time.Minute)
	good := func(context.Context, string, []string, []string, string) ([]byte, error) {
		return []byte("2.1.318 (Claude Code)"), nil
	}
	run := func(ctx context.Context, cli string, args, env []string, wd string) ([]byte, error) {
		if args[0] == "--version" {
			return good(ctx, cli, args, env, wd)
		}
		return []byte("{\"loggedIn\":true,\"subscriptionType\":\"max\"}"), nil
	}
	if err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", run, first); err != nil {
		t.Fatal(err)
	}
	failure := func(context.Context, string, []string, []string, string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	if err := accountCheck(context.Background(), dir, e.ID, "thr_example_actual", failure, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f, _ := accounts.Load(dir)
	entry := f.Entries[0]
	if entry.Last == nil || !entry.Last.At.Equal(first) || entry.Last.Plan != "max" || entry.Attempt.Reason != "timeout" {
		t.Fatal("previous observation replaced or refreshed")
	}
}
func TestAccountsPlanAndExitParsing(t *testing.T) {
	for _, tc := range []struct {
		provider, raw string
		err           error
		auth, plan    string
	}{
		{"claude-code", "{\"loggedIn\":true,\"subscriptionType\":\"max\"}", nil, "reported_present", "max"},
		{"claude-code", "{\"loggedIn\":true,\"subscriptionType\":\"private@example.com\"}", nil, "reported_present", ""},
		{"claude-code", "{\"loggedIn\":true}", errors.New("failed"), "unavailable", ""},
		{"claude-code", "{\"loggedIn\":false}", nil, "reported_absent", ""},
		{"claude-code", "{\"loggedIn\":\"true\"}", nil, "unavailable", ""},
		{"codex", "{\"loggedIn\":false,\"subscriptionType\":\"max\"}", nil, "reported_present", ""},
		{"codex", "anything", errors.New("failed"), "unavailable", ""},
	} {
		o := accountLogin(accounts.Entry{Provider: tc.provider}, accounts.Observation{Auth: "unavailable"}, []byte(tc.raw), tc.err)
		if o.Auth != tc.auth || o.Plan != tc.plan {
			t.Fatalf("parse result %s %s", o.Auth, o.Plan)
		}
	}
}

func TestAccountsAddCheckAcrossToolInvocations(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	t.Setenv("CODEX_TOOL_CALL_ID", "example-add")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "fixture-cli.exe")
	if err := os.WriteFile(cli, []byte("fictional stand-in"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "catalog")
	var out, errOut bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &errOut}
	if code := accountsCommand(e, []string{"add", "--directory", dir, "--provider", "claude-code", "--selection", "explicit_directory", "--account-dir", root, "--cli", cli, "--workdir", root, "--label", "Example studio", "--json"}, nil); code != exitOK {
		t.Fatalf("add: %s", errOut.String())
	}
	f, err := accounts.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	selected := f.Entries[0]
	t.Setenv("CODEX_TOOL_CALL_ID", "example-check")
	calls := 0
	run := func(_ context.Context, _ string, args, environment []string, _ string) ([]byte, error) {
		calls++
		if !slices.Contains(environment, "BB_THREAD_ID=thr_example_actual") || !slices.Contains(environment, "CODEX_TOOL_CALL_ID=example-check") {
			t.Fatal("execution environment changed")
		}
		if args[0] == "--version" {
			return []byte("2.1.318 (Claude Code)"), nil
		}
		if !reflect.DeepEqual(args, []string{"auth", "status", "--json"}) {
			t.Fatal("unexpected command")
		}
		return []byte(`{"loggedIn":true,"subscriptionType":"max"}`), nil
	}
	if code := accountsCommand(e, []string{"check", "--directory", dir, "--id", selected.ID, "--json"}, run); code != exitOK {
		t.Fatalf("check: %s", errOut.String())
	}
	f, err = accounts.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Entries[0]
	if calls != 2 || got.Attempt == nil || got.Attempt.Auth != "reported_present" || got.UpdatedBy != "thr_example_actual" || got.Attempt.Caller != "thr_example_actual" || got.Revision != selected.Revision {
		t.Fatal("tool metadata invalidated selection or caller changed")
	}
}
