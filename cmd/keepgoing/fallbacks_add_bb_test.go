package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

const (
	bbPrimaryThread = "thr_example_primary"
	bbOtherThread   = "thr_example_other"
)

// newBBAddFixture is a disabled loop whose primary is a fictional BB thread.
func newBBAddFixture(t *testing.T) addFixture {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	return newAddFixture(t, "--agent", "bb", "--thread", bbPrimaryThread, "--provider", "claude-code")
}

// fakeBBLists answers the two read-only listings and records every call.
type fakeBBLists struct {
	calls   []string
	threads string
	models  string
	fail    bool
}

func (b *fakeBBLists) lookup(_ context.Context, _ string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, strings.Join(args, " "))
	if b.fail {
		return nil, errors.New("exit status 1")
	}
	if args[0] == "thread" {
		return []byte(b.threads), nil
	}
	return []byte(b.models), nil
}

const fakeBBThreadList = `[
 {"id":"thr_example_docs","providerId":"codex","title":"Docs pass\u0007 with\ttabs","status":"idle","visibility":"visible","archivedAt":null},
 {"id":"thr_example_archived","providerId":"codex","title":"Archived work","status":"idle","visibility":"visible","archivedAt":"2026-01-01T00:00:00Z"},
 {"id":"thr_example_hidden","providerId":"codex","title":"Hidden helper","status":"idle","visibility":"hidden","archivedAt":null},
 {"id":"not a thread id","providerId":"codex","title":"Malformed entry","status":"idle","visibility":"visible","archivedAt":null},
 {"id":"thr_example_pi","providerId":"pi","title":"Other provider","status":"idle","visibility":"visible","archivedAt":null},
 {"id":"thr_example_primary","providerId":"claude-code","title":"Example refactor","status":"active","visibility":"visible","archivedAt":null},
 {"id":"thr_example_untitled","providerId":"codex","title":null,"status":"idle","visibility":"visible","archivedAt":null}
]`

const fakeBBModelList = `[
 {"id":"m1","model":"example-large","displayName":"Example Large","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]},
 {"id":"m2","model":"example-small","displayName":"Example Small","isDefault":false,"supportedReasoningEfforts":[]},
 {"id":"m3","model":"bad name","displayName":"Unusable","isDefault":false}
]`

func TestFallbacksAddBBSameConversationOnAnotherModel(t *testing.T) {
	f := newBBAddFixture(t)
	args := []string{"--non-interactive", "--agent", "bb", "--thread", bbPrimaryThread, "--provider", "claude-code", "--model", "example-large", "--reasoning", "high", "--probe-model", "example-small"}
	code, out, errOut := f.add(t, "", args...) // the default lookup fails the test if bb is ever run
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.Adapter != "bb" || c.BB == nil || c.BB.Thread != bbPrimaryThread || c.BB.Model != "example-large" || c.BB.Reasoning != "high" || c.BB.CLI != cfg.Loop.BB.CLI {
		t.Fatalf("bad candidate: %+v %+v", c, c.BB)
	}
	if c.Provider != "claude-code" || c.Group != "example-large" || c.ID != "bb-example-large" || c.Probe.Kind != "claude" || c.ProbeModel(false) != "example-small" || c.ProbeModel(true) != "example-large" {
		t.Fatalf("bad labels or probe: %+v", c)
	}
	if len(cfg.Loop.Errors.FailoverOn) != 0 {
		t.Fatal("handoff must stay off")
	}
	for _, want := range []string{
		"BB thread (provider claude-code)", "given by ID (this loop's own conversation)", "chosen by BB; keepgoing cannot see or switch it",
		"example-large (not checked)", "Reasoning:     high", "Checked with:  the local claude CLI", "Check login:   that CLI's current login",
		"sets the thread's model and reasoning level with bb thread update,\n  then sends the handoff message. Nothing is sent or changed now.",
		"WARNING: Same conversation as the primary.", "It does not\n     test the account BB uses for the thread.", "with the same history", "Set its bb.model",
		"It shows BB's account\n  works only if BB uses that same login.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, bbPrimaryThread) {
		t.Errorf("thread ID printed without --show-ids:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 80 && !strings.Contains(line, f.root) && !strings.Contains(line, "keepgoing.test") {
			t.Errorf("line wider than 80 columns: %q", line)
		}
	}
	// The existing commands accept the candidate; the probe plan still withholds the thread.
	var planOut, planErr bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "check", "--id", c.ID, "--exact", "--dry-run", "-c", f.cfg}, &planOut, &planErr); code != exitOK || !strings.Contains(planOut.String(), "example-large") || strings.Contains(planOut.String(), bbPrimaryThread) {
		t.Fatalf("probe preview: %d %s %s", code, planOut.String(), planErr.String())
	}
}

func TestFallbacksAddBBOtherThreadJSONAndIDs(t *testing.T) {
	f := newBBAddFixture(t)
	account := f.account("codex-b")
	args := []string{"--non-interactive", "--json", "--agent", "bb", "--thread", bbOtherThread, "--provider", "codex", "--group", "codex-team", "--probe-account-dir", account}
	code, previewOut, errOut := f.add(t, "", append(args, "--dry-run")...)
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	if strings.Contains(previewOut, bbOtherThread) || strings.Contains(previewOut, account) {
		t.Fatalf("JSON exposes the thread ID or probe account path: %s", previewOut)
	}
	var preview, added fallbackAddReport
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	pc := preview.Candidate
	if pc.Adapter != "bb" || pc.Account != "chosen_by_bb" || pc.Thread != "other" || pc.ThreadID != "" || pc.ModelSource != "unchecked" || pc.ProbeAccount != "explicit_directory" || pc.ProbeKind != "codex" || pc.Model != "" {
		t.Fatalf("bad preview: %+v", pc)
	}
	joined := strings.Join(preview.Warnings, "\n")
	if !strings.Contains(joined, "cannot see which account BB uses") || !strings.Contains(joined, "No model is set") {
		t.Fatalf("warnings: %v", preview.Warnings)
	}
	code, addedOut, errOut := f.add(t, "", args...)
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	if err := json.Unmarshal([]byte(addedOut), &added); err != nil {
		t.Fatal(err)
	}
	added.Outcome = "preview"
	a, _ := json.Marshal(preview)
	b, _ := json.Marshal(added)
	if !bytes.Equal(a, b) {
		t.Fatalf("preview and result differ:\n%s\n%s", a, b)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.BB.Thread != bbOtherThread || c.BB.Model != "" || c.ProbeEnv()["CODEX_HOME"] != account {
		t.Fatalf("bad candidate: %+v", c)
	}
	// IDs appear only on request, in text and in JSON.
	show := []string{"--non-interactive", "--dry-run", "--show-ids", "--agent", "bb", "--thread", "thr_example_third", "--provider", "codex", "--group", "codex-third"}
	if code, out, errOut := f.add(t, "", show...); code != exitOK || !strings.Contains(out, "Thread ID:") || !strings.Contains(out, "thr_example_third") {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if code, out, _ := f.add(t, "", append(show, "--json")...); code != exitOK || !strings.Contains(out, `"threadId":"thr_example_third"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestFallbacksAddBBRefusals(t *testing.T) {
	f := newBBAddFixture(t)
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "bb", "--thread", bbOtherThread, "--provider", "codex", "--model", "example-model", "--group", "codex-team", "--id", "existing"); code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	before := f.files(t)
	bb := func(extra ...string) []string { return append([]string{"--agent", "bb"}, extra...) }
	cases := map[string]struct {
		args []string
		want string
	}{
		"no thread":                 {bb("--provider", "codex"), "needs an existing thread ID"},
		"malformed thread":          {bb("--thread", "example", "--provider", "codex"), "needs an existing thread ID"},
		"no provider":               {bb("--thread", "thr_example_new"), "needs its thread's provider"},
		"provider without a probe":  {bb("--thread", "thr_example_new", "--provider", "pi"), "has no built-in probe"},
		"reasoning level":           {bb("--thread", "thr_example_new", "--provider", "codex", "--reasoning", "extreme"), "reasoning level must be one of"},
		"primary thread, no model":  {bb("--thread", bbPrimaryThread, "--provider", "claude-code"), "already the primary"},
		"fallback thread, no model": {bb("--thread", bbOtherThread, "--provider", "codex", "--group", "codex-b"), "already fallback \"existing\""},
		"fallback thread, same":     {bb("--thread", bbOtherThread, "--provider", "codex", "--group", "codex-b", "--model", "example-model"), "on model example-model"},
		"primary's quota group":     {bb("--thread", "thr_example_new", "--provider", "claude-code", "--group", "claude-code"), "is the primary's"},
		"used quota group":          {bb("--thread", "thr_example_new", "--provider", "codex", "--group", "codex-team"), "already used by fallback"},
		"model with a space":        {bb("--thread", "thr_example_new", "--provider", "codex", "--model", "two words"), "single name"},
		"account directory":         {bb("--thread", "thr_example_new", "--provider", "codex", "--account-dir", f.account("codex-b")), "--account-dir applies only to headless"},
		"project directory":         {bb("--thread", "thr_example_new", "--provider", "codex", "--workdir", f.project), "--workdir applies only to headless"},
		"missing probe account":     {bb("--thread", "thr_example_new", "--provider", "codex", "--probe-account-dir", filepath.Join(f.root, "absent")), "Probe account directory"},
		"thread for headless":       {[]string{"--agent", "claude", "--thread", "thr_example_new"}, "--thread applies only to --agent bb"},
		"reasoning for headless":    {[]string{"--agent", "claude", "--reasoning", "high"}, "--reasoning applies only to --agent bb"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := f.add(t, "", append([]string{"--non-interactive"}, tc.args...)...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, tc.want) || f.files(t) != before {
				t.Fatalf("exit %d, want refusal %q: %s", code, tc.want, errOut)
			}
			for _, id := range []string{bbPrimaryThread, bbOtherThread, "thr_example_new"} {
				if strings.Contains(errOut, id) {
					t.Errorf("refusal prints a thread ID: %s", errOut)
				}
			}
		})
	}
	t.Run("local probe CLI missing", func(t *testing.T) {
		var out, errOut bytes.Buffer
		onlyBB := func(name string) (string, error) {
			if name == "bb" {
				return f.lookPath(name)
			}
			return "", errors.New("not found")
		}
		args := append([]string{"-c", f.cfg, "--non-interactive"}, bb("--thread", "thr_example_new", "--provider", "codex")...)
		if code := fallbacksAdd(env{context.Background(), &out, &errOut}, args, strings.NewReader(""), onlyBB, nil); code != exitUsage || !strings.Contains(errOut.String(), "codex CLI was not found on PATH") || f.files(t) != before {
			t.Fatalf("%d %s", code, errOut.String())
		}
	})
	t.Run("not the owning thread", func(t *testing.T) {
		g := newBBAddFixture(t)
		t.Setenv("BB_THREAD_ID", "thr_example_owner")
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{"enable", "-c", g.cfg}, &out, &errOut); code != exitOK {
			t.Fatalf("enable: %s", errOut.String())
		}
		was := g.files(t)
		t.Setenv("BB_THREAD_ID", "thr_example_visitor")
		code, stdout, stderr := g.add(t, "", append([]string{"--non-interactive"}, bb("--thread", bbPrimaryThread, "--provider", "claude-code", "--model", "example-large")...)...)
		if code != exitFailed || stdout != "" || !strings.Contains(stderr, "Ownership") || g.files(t) != was {
			t.Fatalf("%d %q %s", code, stdout, stderr)
		}
	})
}

func TestFallbacksAddBBWizardPicksFromBBLists(t *testing.T) {
	f := newBBAddFixture(t)
	lists := &fakeBBLists{threads: fakeBBThreadList, models: fakeBBModelList}
	f.bb = lists.lookup
	input := strings.Join([]string{
		"bb",
		"yes",    // ask BB for the lists
		"3",      // a thread on a provider without a probe: asked again
		"9",      // out of range
		"ids",    // show IDs on request
		"1",      // this loop's own conversation, listed first
		"",       // same conversation: a model is required
		"3",      // out of range (the unusable entry is not offered)
		"1",      // example-large
		"medium", // not offered for that model
		"high",
		"",    // quota group default
		"",    // probe model
		"",    // probe account directory
		"yes", // turn handoff on
		"yes", // write
	}, "\n") + "\n"
	code, out, errOut := f.add(t, input)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if got := strings.Join(lists.calls, "|"); got != "thread list --json|provider models claude-code --json" {
		t.Fatalf("bb was run with: %s", got)
	}
	beforeIDs, _, found := strings.Cut(out, "! Choose a number between 1 and 4.") // printed just before "ids" is typed
	if !found {
		t.Fatal("marker missing")
	}
	if strings.Contains(beforeIDs, "thr_example") {
		t.Errorf("thread IDs listed before being asked for:\n%s", beforeIDs)
	}
	for _, want := range []string{
		"an existing thread can be one", "not supported here", "No message is sent and no thread is created or changed.",
		"1.  Example refactor", "used by this loop", "Docs pass with tabs", "(untitled)",
		"! Provider \"pi\" has no built-in probe.", "! Choose a number between 1 and 4.",
		"thr_example_docs", // after "ids"
		"Note: this is the same conversation as the primary.",
		"1.  example-large  Example Large  BB's default", "2.  example-small",
		"! This thread is already the primary.", "! Choose a number between 1 and 2.",
		"Reasoning level (low/high, blank = unchanged)", "! Choose one of: low, high, or leave it blank.",
		"A quota group names one usage limit.", "Quota group [example-large]",
		"Example refactor (this loop's own conversation)", "example-large (from BB's model list)", "turned ON by this change",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output missing %q", want)
		}
	}
	for _, hidden := range []string{"Archived work", "Hidden helper", "Malformed entry", "Unusable", "\a"} {
		if strings.Contains(out, hidden) {
			t.Errorf("wizard offered or echoed %q", hidden)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.BB.Thread != bbPrimaryThread || c.BB.Model != "example-large" || c.BB.Reasoning != "high" || c.Provider != "claude-code" || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("bad result: %+v %+v", c, c.BB)
	}
}

func TestFallbacksAddBBWizardWithoutLists(t *testing.T) {
	answers := strings.Join([]string{
		"example", // not a thread ID
		"thr_example_new",
		"pi", // provider without a probe
		"codex",
		"",    // model: keep the thread's own
		"max", // reasoning
		"",    // quota group default
		"",    // probe model
		"",    // probe account directory
		"no",  // handoff
		"yes", // write
	}, "\n") + "\n"
	for name, setup := range map[string]func(*fakeBBLists) string{
		"declined":      func(*fakeBBLists) string { return "bb\nno\n" },
		"listing fails": func(b *fakeBBLists) string { b.fail = true; return "bb\nyes\n" },
		"error envelope": func(b *fakeBBLists) string {
			b.threads, b.models = `{"ok":false,"error":{"code":"unavailable"}}`, `{"ok":false}`
			return "bb\nyes\n"
		},
		"nothing to offer": func(b *fakeBBLists) string { b.threads, b.models = `[]`, `[]`; return "bb\nyes\n" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newBBAddFixture(t)
			lists := &fakeBBLists{}
			f.bb = lists.lookup
			code, out, errOut := f.add(t, setup(lists)+answers)
			if code != exitOK {
				t.Fatalf("%d %s\n%s", code, errOut, out)
			}
			if name == "declined" && len(lists.calls) != 0 {
				t.Fatalf("bb was run after the lists were declined: %v", lists.calls)
			}
			for _, call := range lists.calls {
				if call != "thread list --json" && call != "provider models codex --json" {
					t.Fatalf("unexpected bb command: %s", call)
				}
			}
			for _, want := range []string{"! Enter a number from the list or a thread ID (thr_...).", "! Provider \"pi\" has no built-in probe.", "It is not checked here; blank keeps the thread's own model.", "keepgoing cannot see which account BB uses for this thread.", "the thread's own (not changed)", "sets the thread's reasoning level with bb thread update,"} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "thr_example_new\n") && strings.Contains(strings.SplitN(out, "Preview for loop", 2)[1], "thr_example_new") {
				t.Errorf("preview prints the thread ID:\n%s", out)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			c := cfg.FallbackSet.Candidates[0]
			if c.BB.Thread != "thr_example_new" || c.Provider != "codex" || c.BB.Model != "" || c.BB.Reasoning != "max" || c.Group != "codex" {
				t.Fatalf("bad candidate: %+v %+v", c, c.BB)
			}
		})
	}
}

func TestRunBBLookupIsBoundedAndReadsOnlyStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stand-in")
	}
	dir := t.TempDir()
	ok := filepath.Join(dir, "bb-ok")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\necho 'noise' >&2\nprintf '[{\"id\":\"thr_example_docs\",\"providerId\":\"codex\",\"title\":\"Docs\"}]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	threads, err := bbThreads(context.Background(), runBBLookup, ok)
	if err != nil || len(threads) != 1 || threads[0].ID != "thr_example_docs" {
		t.Fatalf("%v %+v", err, threads)
	}
	failing := filepath.Join(dir, "bb-fail")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nprintf '[]'\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := bbThreads(context.Background(), runBBLookup, failing); err == nil {
		t.Fatal("a failed listing was accepted")
	}
	if _, err := bbThreads(context.Background(), runBBLookup, filepath.Join(dir, "absent")); err == nil {
		t.Fatal("a missing executable was accepted")
	}
	big := filepath.Join(dir, "bb-big")
	if err := os.WriteFile(big, []byte("#!/bin/sh\nhead -c 9000000 /dev/zero\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runBBLookup(context.Background(), big, "thread", "list", "--json"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized listing: %v", err)
	}
	if _, err := os.Stat(ok); err != nil {
		t.Fatal(err)
	}
}
