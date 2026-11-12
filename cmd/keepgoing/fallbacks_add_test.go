package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// addFixture is a disabled loop created by guided setup, plus fictional
// account directories. Its lookPath names executables that do not exist, so
// no test can run a provider.
type addFixture struct {
	setupFixture
	cfg, fallbacks string
	bb             bbLookup // nil: any bb invocation fails the test
}

func newAddFixture(t *testing.T, initArgs ...string) addFixture {
	t.Helper()
	f := addFixture{setupFixture: newSetupFixture(t)}
	for _, d := range []string{"claude-b", "codex-b"} {
		if err := os.MkdirAll(filepath.Join(f.root, "accounts", d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	args := append(f.args(), initArgs...)
	if code, _, errOut := f.run(t, args, ""); code != 0 {
		t.Fatalf("init: %d %s", code, errOut)
	}
	f.cfg = filepath.Join(f.destination, "keepgoing.yaml")
	f.fallbacks = filepath.Join(f.destination, "fallbacks.yaml")
	return f
}

func (f addFixture) account(name string) string { return filepath.Join(f.root, "accounts", name) }

func (f addFixture) add(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	bb := f.bb
	if bb == nil {
		bb = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			t.Errorf("unexpected bb invocation: %v", args)
			return nil, errors.New("not allowed")
		}
	}
	code := fallbacksAdd(env{context.Background(), &out, &errOut}, append([]string{"-c", f.cfg}, args...), strings.NewReader(input), f.lookPath, bb)
	return code, out.String(), errOut.String()
}

func (f addFixture) files(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{f.cfg, f.fallbacks} {
		raw, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		b.WriteString(p + "\n" + string(raw) + "\n")
	}
	return b.String()
}

func TestFallbacksAddCreatesFileAndKeepsHandoffOff(t *testing.T) {
	f := newAddFixture(t)
	code, out, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--account-dir", f.account("claude-b"), "--group", "pool-b", "--model", "example-model", "--probe-model", "example-small")
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FallbackSet == nil || len(cfg.FallbackSet.Candidates) != 1 {
		t.Fatalf("fallbacks not loaded: %+v", cfg.FallbackSet)
	}
	if len(cfg.Loop.Errors.FailoverOn) != 0 {
		t.Fatalf("handoff must stay opt-in, got %v", cfg.Loop.Errors.FailoverOn)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.ID != "claude-pool-b" || c.Provider != "claude-code" || c.Group != "pool-b" || c.Model() != "example-model" || c.Probe.Kind != "claude" || c.ProbeModel(false) != "example-small" || c.Claude.Workdir != f.project {
		t.Fatalf("bad candidate: %+v", c)
	}
	if got := c.ProbeEnv()["CLAUDE_CONFIG_DIR"]; got != f.account("claude-b") {
		t.Fatalf("account directory: %q", got)
	}
	if r := cfg.FallbackSet.Redundancy; r.MinHealthy != 1 || r.Span != "group" || cfg.FallbackSet.Probe.DailyCap != 2 {
		t.Fatalf("bad rules: %+v %+v", r, cfg.FallbackSet.Probe)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(f.fallbacks); info.Mode().Perm() != 0o600 {
			t.Fatalf("fallbacks file mode %v", info.Mode().Perm())
		}
	}
	for _, want := range []string{"Handoff order", "<- new", "OFF. The fallback is saved but never used automatically.", "--id claude-pool-b --exact --dry-run", "may cost money", "failover_on: [rate_limit]", "does not log in"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The written loop is usable by the existing commands, still without provider calls.
	var listOut, listErr bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "check", "--id", "claude-pool-b", "--exact", "--dry-run", "-c", f.cfg}, &listOut, &listErr); code != exitOK || !strings.Contains(listOut.String(), "claude-pool-b") {
		t.Fatalf("probe preview: %d %s %s", code, listOut.String(), listErr.String())
	}
	entries, err := os.ReadDir(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "tick.lock" {
			t.Errorf("unexpected runtime state %s", e.Name())
		}
	}
}

func TestFallbacksAddWizardMatchesFlags(t *testing.T) {
	flags := newAddFixture(t)
	if code, _, errOut := flags.add(t, "", "--non-interactive", "--agent", "codex", "--account-dir", flags.account("codex-b"), "--group", "pool-b", "--model", "example-model", "--enable-handoff"); code != exitOK {
		t.Fatalf("flags: %d %s", code, errOut)
	}
	wizard := newAddFixture(t)
	// agent, account directory, group, model, probe model, handoff, confirmation
	input := "codex\n" + wizard.account("codex-b") + "\npool-b\nexample-model\n\nyes\nyes\n"
	code, out, errOut := wizard.add(t, input)
	if code != exitOK {
		t.Fatalf("wizard: %d %s\n%s", code, errOut, out)
	}
	normalize := func(f addFixture) string { return strings.ReplaceAll(f.files(t), f.root, "ROOT") }
	if a, b := normalize(flags), normalize(wizard); a != b {
		t.Fatalf("wizard and flags differ:\n%s\n---\n%s", a, b)
	}
	for _, want := range []string{"looked up only; nothing was run", "cannot be a fallback yet", "Step 1 of 4: Provider", "Step 4 of 4: Handoff", "turned ON by this change", "Write this fallback?"} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output missing %q", want)
		}
	}
	cfg, err := config.Load(wizard.cfg)
	if err != nil || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("handoff not enabled: %v %v", cfg, err)
	}
}

func TestFallbacksAddWritesNothingWithoutConfirmation(t *testing.T) {
	for name, tail := range map[string]string{"declined": "no\n", "default": "\n", "input ended": ""} {
		t.Run(name, func(t *testing.T) {
			f := newAddFixture(t)
			before := f.files(t)
			code, out, _ := f.add(t, "claude\n\npool-b\n\n\nno\n"+tail)
			if name == "input ended" {
				if code != exitUsage {
					t.Fatalf("exit %d", code)
				}
			} else if code != exitOK || !strings.Contains(out, "Cancelled: nothing written.") {
				t.Fatalf("exit %d: %s", code, out)
			}
			if f.files(t) != before {
				t.Fatal("files changed without confirmation")
			}
			if _, err := os.Stat(filepath.Join(f.destination, "state")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unconfirmed add touched runtime state")
			}
		})
	}
}

func TestFallbacksAddJSONPreviewMatchesResult(t *testing.T) {
	f := newAddFixture(t)
	before := f.files(t)
	args := []string{"--non-interactive", "--json", "--agent", "codex", "--account-dir", f.account("codex-b"), "--group", "pool-b", "--enable-handoff"}
	code, previewOut, errOut := f.add(t, "", append(args, "--dry-run")...)
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	if f.files(t) != before {
		t.Fatal("dry run wrote files")
	}
	if strings.Contains(previewOut, f.account("codex-b")) || strings.Contains(previewOut, "not-executable") {
		t.Fatalf("JSON exposes account or executable paths: %s", previewOut)
	}
	var preview, added fallbackAddReport
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SchemaVersion != 1 || preview.Kind != "fallback_add" || preview.Outcome != "preview" || preview.Candidate.Account != "explicit_directory" || preview.Candidate.ID != "codex-pool-b" || !preview.Handoff.Automatic || !preview.Handoff.Changed || !preview.Files.FallbacksCreated || !preview.Files.ConfigChanged || !preview.Redundancy.Reachable {
		t.Fatalf("bad preview: %+v", preview)
	}
	if len(preview.Order) != 1 || !preview.Order[0].New || preview.Order[0].Position != 1 || preview.Primary.Group != "codex" {
		t.Fatalf("bad order: %+v", preview)
	}
	if got := strings.Join(preview.Next.ExactProbe[1:], " "); got != "fallbacks check --id codex-pool-b --exact -c "+f.cfg {
		t.Fatalf("next command: %s", got)
	}
	code, addedOut, errOut := f.add(t, "", args...)
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	if err := json.Unmarshal([]byte(addedOut), &added); err != nil {
		t.Fatal(err)
	}
	if added.Outcome != "added" {
		t.Fatalf("outcome %q", added.Outcome)
	}
	added.Outcome = "preview"
	a, _ := json.Marshal(preview)
	b, _ := json.Marshal(added)
	if !bytes.Equal(a, b) {
		t.Fatalf("preview and result differ:\n%s\n%s", a, b)
	}
	if _, err := config.Load(f.cfg); err != nil {
		t.Fatal(err)
	}
}

func TestFallbacksAddInsertsIntoExistingOrderAndKeepsComments(t *testing.T) {
	f := newAddFixture(t, "--fallback", "codex:pool-b")
	for _, p := range []string{f.cfg, f.fallbacks} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, append([]byte("# fictional operator note\n"), raw...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	was, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore, _ := os.ReadFile(f.cfg)
	code, out, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--account-dir", f.account("claude-b"), "--position", "1", "--enable-handoff")
	if code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.FallbackSet.Candidates
	if len(got) != 2 || got[0].ID != "claude-b" || got[1].ID != "fallback-1" {
		t.Fatalf("bad order: %+v", got)
	}
	if !reflect.DeepEqual(got[1], was.FallbackSet.Candidates[0]) || cfg.FallbackSet.Probe != was.FallbackSet.Probe || cfg.FallbackSet.Redundancy != was.FallbackSet.Redundancy {
		t.Fatal("existing candidate or rules changed")
	}
	raw, _ := os.ReadFile(f.fallbacks)
	if !strings.HasPrefix(string(raw), "# fictional operator note\n") {
		t.Fatalf("comment lost:\n%s", raw)
	}
	// Handoff was already on, so keepgoing.yaml is left byte-identical.
	if cfgAfter, _ := os.ReadFile(f.cfg); !bytes.Equal(cfgBefore, cfgAfter) {
		t.Fatal("keepgoing.yaml rewritten without a change to make")
	}
	if !strings.Contains(out, "automatic on rate_limit (already on; unchanged)") || !strings.Contains(out, "position 1 of 2") || !strings.Contains(out, "Note: --enable-handoff had no effect") {
		t.Fatalf("output:\n%s", out)
	}

	// Enabling handoff edits keepgoing.yaml in place and keeps its comments.
	g := newAddFixture(t)
	raw, _ = os.ReadFile(g.cfg)
	if err := os.WriteFile(g.cfg, append([]byte("# fictional operator note\n"), raw...), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := config.Load(g.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := g.add(t, "", "--non-interactive", "--agent", "claude", "--enable-handoff"); code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	raw, _ = os.ReadFile(g.cfg)
	next, err := config.Load(g.cfg)
	if err != nil || !strings.HasPrefix(string(raw), "# fictional operator note\n") {
		t.Fatalf("%v\n%s", err, raw)
	}
	if strings.Join(next.Loop.Errors.FailoverOn, ",") != "rate_limit" || next.Loop.Fallbacks != g.fallbacks {
		t.Fatalf("handoff or fallbacks path not set: %+v", next.Loop)
	}
	// Everything else in the loop is unchanged.
	next.Loop.Errors.FailoverOn, next.Loop.Fallbacks = old.Loop.Errors.FailoverOn, old.Loop.Fallbacks
	if !reflect.DeepEqual(next.Loop, old.Loop) {
		t.Fatalf("unrelated settings changed:\n%+v\n%+v", next.Loop, old.Loop)
	}
}

func TestFallbacksAddRefusesUnsupportedCombinations(t *testing.T) {
	f := newAddFixture(t)
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--group", "pool-b", "--id", "existing"); code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	before := f.files(t)
	other := []string{"--agent", "codex", "--account-dir", f.account("codex-b")}
	with := func(extra ...string) []string { return append(append([]string{}, other...), extra...) }
	cases := map[string]struct {
		args []string
		want string
	}{
		"cursor":             {[]string{"--agent", "cursor"}, "Cursor cannot be a fallback yet"},
		"process":            {[]string{"--agent", "process"}, "claude, codex and bb only"},
		"unknown agent":      {[]string{"--agent", "example"}, "Unknown agent"},
		"primary group":      {with("--group", "codex"), "is the primary's"},
		"shared group":       {with("--group", "pool-b"), "already used by fallback \"existing\""},
		"repeated id":        {with("--id", "existing"), "already in use"},
		"reserved id":        {with("--id", "primary"), "already in use"},
		"invalid id":         {with("--id", "Not Valid"), "--id must be 1-64 lowercase letters"},
		"invalid group":      {with("--group", "pool c"), "Quota group labels"},
		"missing account":    {[]string{"--agent", "codex", "--account-dir", filepath.Join(f.root, "accounts", "absent")}, "Account directory: not an existing directory"},
		"position too large": {with("--position", "3"), "between 1 and 2"},
		"model with space":   {with("--model", "two words"), "single name"},
		"model as flag":      {with("--probe-model", "--danger"), "single name"},
		"missing workdir":    {with("--workdir", filepath.Join(f.root, "absent")), "Project directory"},
		"cap with a file":    {with("--probe-daily-cap", "4"), "applies only when this command creates"},
		"same login":         {[]string{"--agent", "codex", "--group", "pool-c"}, "same login as the primary"},
		"same login, model":  {[]string{"--agent", "claude", "--group", "pool-c"}, "same login as fallback \"existing\""},
		"json interactive":   {[]string{"--json"}, "--json requires --non-interactive"},
		"positional":         {[]string{"--agent", "codex", "extra"}, "unexpected positional"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			args := tc.args
			if name != "json interactive" {
				args = append([]string{"--non-interactive"}, args...)
			}
			code, out, errOut := f.add(t, "", args...)
			if code != exitUsage || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit %d, want refusal %q: %s", code, tc.want, errOut)
			}
			if out != "" || f.files(t) != before || strings.Contains(errOut, "candidates[") {
				t.Fatalf("refusal printed a result, changed files or exposed internals: %s %s", out, errOut)
			}
		})
	}
	t.Run("missing CLI", func(t *testing.T) {
		var out, errOut bytes.Buffer
		missing := func(string) (string, error) { return "", errors.New("not found") }
		code := fallbacksAdd(env{context.Background(), &out, &errOut}, append([]string{"-c", f.cfg, "--non-interactive"}, other...), strings.NewReader(""), missing, nil)
		if code != exitUsage || !strings.Contains(errOut.String(), "codex CLI not found on PATH") || f.files(t) != before {
			t.Fatalf("%d %s", code, errOut.String())
		}
	})
	t.Run("cursor primary", func(t *testing.T) {
		c := addFixture{setupFixture: newSetupFixture(t)}
		args := []string{"--non-interactive", "--name", "example-cursor", "--agent", "cursor", "--project", c.project, "--directory", c.destination, "--task", "Complete the fictional example task."}
		if code, _, errOut := c.run(t, args, ""); code != 0 {
			t.Fatalf("init: %s", errOut)
		}
		c.cfg, c.fallbacks = filepath.Join(c.destination, "keepgoing.yaml"), filepath.Join(c.destination, "fallbacks.yaml")
		was := c.files(t)
		code, _, errOut := c.add(t, "", "--non-interactive", "--agent", "codex")
		if code != exitUsage || !strings.Contains(errOut, "Cursor primary supports standalone loops only") || c.files(t) != was {
			t.Fatalf("%d %s", code, errOut)
		}
	})
}

func TestFallbacksAddRefusesStaleOrBusyLoop(t *testing.T) {
	f := newAddFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planFallbackAdd(cfg, fallbackAddOptions{cfgPath: f.cfg, agent: "claude", group: "pool-b", probeCap: 2, set: map[string]bool{}}, f.lookPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	before := f.files(t)
	if code, err := writeFallbackAdd(plan); code != exitSkipped || err == nil || f.files(t) != before {
		t.Fatalf("write while a check holds the lock: %d %v", code, err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.cfg, append(raw, []byte("# reviewed elsewhere\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	before = f.files(t)
	if code, err := writeFallbackAdd(plan); code != exitSkipped || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
		t.Fatalf("write after the configuration changed: %d %v", code, err)
	}
}

func TestFallbacksAddCommandHelpAndListHint(t *testing.T) {
	f := newAddFixture(t)
	before := f.files(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"fallbacks", "add", "--help"}, &out, &errOut); code != exitOK || !strings.Contains(errOut.String(), "never logs in") {
		t.Fatalf("help: %d %s", code, errOut.String())
	}
	if f.files(t) != before {
		t.Fatal("help changed files")
	}
	// Dispatch through the real command table; --json never prompts.
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"fallbacks", "add", "-c", f.cfg, "--non-interactive", "--json", "--dry-run", "--agent", "claude"}, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "claude CLI not found on PATH") {
		// The test host has no claude on PATH guarantee either way; only a
		// preview or the missing-CLI refusal is acceptable, never a write.
		if code != exitOK || !strings.Contains(out.String(), `"outcome":"preview"`) {
			t.Fatalf("dispatch: %d %s %s", code, out.String(), errOut.String())
		}
	}
	if f.files(t) != before {
		t.Fatal("dry run through the command table changed files")
	}
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--group", "pool-b"); code != exitOK {
		t.Fatalf("%d: %s", code, errOut)
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"fallbacks", "list", "-c", f.cfg}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "Not probed yet: claude-pool-b.") || !strings.Contains(out.String(), "fallbacks check --dry-run "+lineContinuation()+"\n    -c "+commandQuote(f.cfg)) {
		t.Fatalf("list: %d %s %s", code, out.String(), errOut.String())
	}
}

func TestFallbacksAddRequiresLoopOwnerAndNoPendingTransfer(t *testing.T) {
	f := newAddFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", f.cfg}, &out, &errOut); code != exitOK {
		t.Fatalf("enable: %d %s", code, errOut.String())
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Plan as the owner; the write must still refuse once the caller differs.
	plan, err := planFallbackAdd(cfg, fallbackAddOptions{cfgPath: f.cfg, agent: "claude", group: "pool-b", probeCap: 2, enableHandoff: true, set: map[string]bool{}}, f.lookPath)
	if err != nil {
		t.Fatal(err)
	}
	before := f.files(t)
	t.Setenv("BB_THREAD_ID", "thr_example_other")
	for name, args := range map[string][]string{
		"write":   {"--non-interactive", "--agent", "claude", "--group", "pool-b", "--enable-handoff"},
		"preview": {"--non-interactive", "--agent", "claude", "--group", "pool-b", "--dry-run", "--json"},
		"wizard":  {},
	} {
		code, stdout, stderr := f.add(t, "claude\n\npool-b\n\n\nyes\nyes\n", args...)
		if code != exitFailed || stdout != "" || !strings.Contains(stderr, "owned by BB thread thr_example_owner") || f.files(t) != before {
			t.Fatalf("%s by a non-owner: %d %q %s", name, code, stdout, stderr)
		}
	}
	if code, err := writeFallbackAdd(plan); code != exitFailed || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
		t.Fatalf("write after ownership moved: %d %v", code, err)
	}
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	if code, _, stderr := f.add(t, "", "--non-interactive", "--agent", "claude", "--group", "pool-b"); code != exitOK {
		t.Fatalf("owner refused: %d %s", code, stderr)
	}

	for name, ctl := range map[string]control.File{
		"pending transfer":   {AwaitingUser: true, PendingTransfer: &control.PendingBBTransfer{}},
		"transferred target": {BBTarget: "thr_example_moved"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "")
			g := newAddFixture(t)
			cfg, err := config.Load(g.cfg)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planFallbackAdd(cfg, fallbackAddOptions{cfgPath: g.cfg, agent: "claude", group: "pool-b", probeCap: 2, set: map[string]bool{}}, g.lookPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			before := g.files(t)
			code, stdout, stderr := g.add(t, "", "--non-interactive", "--agent", "claude", "--group", "pool-b")
			if code != exitSkipped || stdout != "" || !strings.Contains(stderr, "nothing written") || g.files(t) != before {
				t.Fatalf("%d %q %s", code, stdout, stderr)
			}
			if code, err := writeFallbackAdd(plan); code != exitSkipped || err == nil || g.files(t) != before {
				t.Fatalf("write: %d %v", code, err)
			}
		})
	}
}

func TestFallbacksAddRefusesSymlinkedSources(t *testing.T) {
	f := newAddFixture(t)
	links := filepath.Join(f.root, "links")
	if err := os.Mkdir(links, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(links, "keepgoing.yaml")
	if err := os.Symlink(f.cfg, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := f.files(t)
	var out, errOut bytes.Buffer
	code := fallbacksAdd(env{context.Background(), &out, &errOut}, []string{"-c", link, "--non-interactive", "--agent", "claude"}, strings.NewReader(""), f.lookPath, nil)
	if code != exitUsage || !strings.Contains(errOut.String(), "symbolic link") || f.files(t) != before {
		t.Fatalf("%d %s", code, errOut.String())
	}
	if entries, _ := os.ReadDir(links); len(entries) != 1 {
		t.Fatalf("files created beside the link: %v", entries)
	}

	// A fallbacks file reached through a link is refused as well.
	if code, _, stderr := f.add(t, "", "--non-interactive", "--agent", "claude"); code != exitOK {
		t.Fatalf("%d %s", code, stderr)
	}
	real := filepath.Join(f.destination, "real-fallbacks.yaml")
	if err := os.Rename(f.fallbacks, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.fallbacks); err != nil {
		t.Fatal(err)
	}
	was, _ := os.ReadFile(real)
	code, _, stderr := f.add(t, "", "--non-interactive", "--agent", "codex", "--account-dir", f.account("codex-b"))
	if now, _ := os.ReadFile(real); code != exitUsage || !strings.Contains(stderr, "symbolic link") || !bytes.Equal(was, now) {
		t.Fatalf("%d %s", code, stderr)
	}
}

// An absent errors.failover_on defaults to rate_limit, so the first fallback
// would start handoff as a side effect unless the command decides it.
func TestFallbacksAddNeverStartsHandoffImplicitly(t *testing.T) {
	strip := func(t *testing.T, f addFixture, replacement string) {
		t.Helper()
		raw, err := os.ReadFile(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, l := range lines {
			if strings.Contains(l, "failover_on") {
				if replacement == "" {
					lines = append(lines[:i], lines[i+1:]...)
				} else {
					lines[i] = l[:strings.Index(l, "failover_on")] + replacement
				}
				break
			}
		}
		if err := os.WriteFile(f.cfg, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		if cfg, err := config.Load(f.cfg); err != nil || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" {
			t.Fatalf("fixture must have an effective rate_limit setting: %v", err)
		}
	}
	for name, setting := range map[string]string{"absent key": "", "explicit key": "failover_on: [rate_limit]"} {
		t.Run(name+"/kept off", func(t *testing.T) {
			f := newAddFixture(t)
			strip(t, f, setting)
			args := []string{"--non-interactive", "--agent", "claude", "--group", "pool-b"}
			code, out, stderr := f.add(t, "", append(args, "--dry-run", "--json")...)
			if code != exitOK {
				t.Fatalf("%d %s", code, stderr)
			}
			var r fallbackAddReport
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
			if h := r.Handoff; h.Automatic || h.WasAutomatic || h.Changed || !h.FailoverOnWritten || len(h.FailoverOn) != 0 {
				t.Fatalf("handoff must stay off and say what is written: %+v", h)
			}
			code, out, stderr = f.add(t, "", args...)
			if code != exitOK || !strings.Contains(out, "OFF. The fallback is saved but never used automatically.") || !strings.Contains(out, "explicit empty setting is written") || !strings.Contains(out, "loop.errors.failover_on") {
				t.Fatalf("%d %s\n%s", code, stderr, out)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil || cfg.FallbackSet == nil || len(cfg.Loop.Errors.FailoverOn) != 0 {
				t.Fatalf("handoff went live: %v %+v", err, cfg)
			}
		})
		t.Run(name+"/enabled", func(t *testing.T) {
			f := newAddFixture(t)
			strip(t, f, setting)
			code, out, stderr := f.add(t, "", "--non-interactive", "--json", "--agent", "claude", "--group", "pool-b", "--enable-handoff")
			if code != exitOK {
				t.Fatalf("%d %s", code, stderr)
			}
			var r fallbackAddReport
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
			if h := r.Handoff; !h.Automatic || h.WasAutomatic || !h.Changed || h.FailoverOnWritten != (setting == "") {
				t.Fatalf("%+v", h)
			}
			raw, _ := os.ReadFile(f.cfg)
			cfg, err := config.Load(f.cfg)
			if err != nil || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" || !strings.Contains(string(raw), "failover_on: [rate_limit]") {
				t.Fatalf("explicit setting missing: %v\n%s", err, raw)
			}
		})
	}
	// The wizard asks, explains the write, and defaults to off.
	f := newAddFixture(t)
	strip(t, f, "")
	code, out, stderr := f.add(t, "claude\n\npool-b\n\n\n\nyes\n")
	if code != exitOK || !strings.Contains(out, "answering no writes an") || !strings.Contains(out, "Handoff:    off") {
		t.Fatalf("%d %s\n%s", code, stderr, out)
	}
	if cfg, err := config.Load(f.cfg); err != nil || len(cfg.Loop.Errors.FailoverOn) != 0 {
		t.Fatalf("wizard default started handoff: %v", err)
	}
}

func TestFallbacksAddSameLoginNeedsADifferentModel(t *testing.T) {
	f := newAddFixture(t, "--model", "example-model") // codex primary on the CLI's current login
	link := filepath.Join(f.root, "accounts", "codex-b-link")
	linked := os.Symlink(f.account("codex-b"), link) == nil
	if code, _, stderr := f.add(t, "", "--non-interactive", "--agent", "codex", "--account-dir", f.account("codex-b"), "--group", "pool-b"); code != exitOK {
		t.Fatalf("%d %s", code, stderr)
	}
	before := f.files(t)
	refused := map[string][]string{
		"primary login, default model":   {"--agent", "codex", "--group", "pool-c"},
		"primary login, same model":      {"--agent", "codex", "--group", "pool-c", "--model", "example-model"},
		"fallback's directory, no model": {"--agent", "codex", "--group", "pool-c", "--account-dir", f.account("codex-b")},
	}
	if linked {
		refused["fallback's directory through a link"] = []string{"--agent", "codex", "--group", "pool-c", "--account-dir", link}
	}
	for name, args := range refused {
		code, out, stderr := f.add(t, "", append([]string{"--non-interactive", "--dry-run"}, args...)...)
		if code != exitUsage || out != "" || !strings.Contains(stderr, "is the same login as") || f.files(t) != before {
			t.Fatalf("%s: %d %s", name, code, stderr)
		}
	}
	args := []string{"--non-interactive", "--dry-run", "--agent", "codex", "--group", "pool-c", "--model", "other-model"}
	code, out, stderr := f.add(t, "", args...)
	if code != exitOK || !strings.Contains(out, "WARNING: Same login as the primary.") || !strings.Contains(out, "Probe model:  same as the model") {
		t.Fatalf("%d %s\n%s", code, stderr, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 80 && !strings.Contains(line, f.root) {
			t.Errorf("preview line wider than 80 columns: %q", line)
		}
	}
	code, out, stderr = f.add(t, "", append(args, "--json")...)
	var r fallbackAddReport
	if err := json.Unmarshal([]byte(out), &r); code != exitOK || err != nil || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Same login as the primary") {
		t.Fatalf("%d %s %v %+v", code, stderr, err, r.Warnings)
	}
	// The wizard says so at the account step, offers a usable group and re-asks for the model.
	code, out, stderr = f.add(t, "codex\n\n\n\nexample-model\nother-model\n\n\nno\nyes\n")
	if code != exitOK || !strings.Contains(out, "Note: this is the same login as the primary.") || !strings.Contains(out, "Quota group [codex-2]") || strings.Count(out, "! This is the same login as the primary") != 2 {
		t.Fatalf("%d %s\n%s", code, stderr, out)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil || len(cfg.FallbackSet.Candidates) != 2 || cfg.FallbackSet.Candidates[1].Group != "codex-2" || cfg.FallbackSet.Candidates[1].Model() != "other-model" {
		t.Fatalf("%v %+v", err, cfg.FallbackSet)
	}
}

func TestFallbacksAddWizardRepromptsInsteadOfStopping(t *testing.T) {
	f := newAddFixture(t, "--fallback", "claude:pool-b")
	home := filepath.Join(f.root, "accounts")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	input := strings.Join([]string{
		"cursor", "process", "codex", // unsupported agents, then a supported one
		filepath.Join(f.root, "absent"), "~/codex-b", // missing directory, then one under the home directory
		"codex", "pool-b", "pool c", "", // primary's group, a used group, bad characters, then the default
		"two words", "example-model", // bad model name
		"--flag", "", // bad probe model
		"first", "9", "1", // bad positions
		"perhaps", "yes", // unclear confirmation
	}, "\n") + "\n"
	code, out, stderr := f.add(t, input)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, stderr, out)
	}
	for _, want := range []string{"! Cursor cannot be a fallback yet", "! Guided add supports", "! Account directory: not an existing directory", "is the primary's", "already used by fallback", "! Quota group labels", "Quota group [codex-b]", "! The model must be", "! The probe model must be", "! The position must be a number between 1 and 2.", "! Please answer yes or no."} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output missing %q", want)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.ID != "codex-b" || c.Group != "codex-b" || c.Model() != "example-model" || c.ProbeEnv()["CODEX_HOME"] != f.account("codex-b") {
		t.Fatalf("bad candidate: %+v", c)
	}
}

func TestFallbacksAddKeepsCommentsAndModes(t *testing.T) {
	f := newAddFixture(t)
	body := strings.Join([]string{
		"# header note",
		"version: 1",
		"candidates:",
		"  - id: first # inline note",
		"    adapter: claude",
		"    group: pool-b",
		"    claude: {workdir: " + f.project + "}",
		"    probe: {kind: claude}",
		"",
		"  # between candidates",
		"  - id: second",
		"    adapter: claude",
		"    group: pool-c",
		"    claude:",
		"      workdir: " + f.project,
		"      env: {CLAUDE_CONFIG_DIR: " + f.account("claude-b") + "}",
		"    probe: {kind: claude}",
		"",
	}, "\r\n")
	if err := os.WriteFile(f.fallbacks, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.cfg)
	edited := strings.Replace(string(raw), "name: example-loop", "name: example-loop # loop note\n    fallbacks: fallbacks.yaml", 1)
	if err := os.WriteFile(f.cfg, []byte("# config note\n"+edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := f.add(t, "", "--non-interactive", "--agent", "codex", "--account-dir", f.account("codex-b"), "--position", "2", "--enable-handoff"); code != exitOK {
		t.Fatalf("%d %s", code, stderr)
	}
	got, _ := os.ReadFile(f.fallbacks)
	text := string(got)
	for _, want := range []string{"# header note\n", "id: first # inline note\n", "# between candidates\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("fallbacks file lost %q:\n%s", want, text)
		}
	}
	if !(strings.Index(text, "id: first") < strings.Index(text, "id: codex-b") && strings.Index(text, "id: codex-b") < strings.Index(text, "# between candidates") && strings.Index(text, "# between candidates") < strings.Index(text, "id: second")) {
		t.Errorf("comment no longer sits above the candidate it described:\n%s", text)
	}
	// Documented normalization: CRLF becomes LF and blank lines between entries are dropped.
	if strings.Contains(text, "\r") || strings.Contains(text, "inline note\n\n") || strings.Contains(text, "\n\n    #") {
		t.Errorf("unexpected line endings or blank lines:\n%q", text)
	}
	cfgText, _ := os.ReadFile(f.cfg)
	for _, want := range []string{"# config note\n", "name: example-loop # loop note\n", "failover_on: [rate_limit]"} {
		if !strings.Contains(string(cfgText), want) {
			t.Errorf("keepgoing.yaml lost %q:\n%s", want, cfgText)
		}
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{f.fallbacks: 0o640, f.cfg: 0o644} {
			if info, _ := os.Stat(path); info.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v", filepath.Base(path), info.Mode().Perm(), want)
			}
		}
	}
	if entries, _ := os.ReadDir(f.destination); len(entries) != 4 { // keepgoing.yaml, fallbacks.yaml, prompt.md, state
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestFallbacksAddWriteFailuresLeaveAConsistentLoop(t *testing.T) {
	failing := func(target string) func(string, []byte) error {
		return func(path string, data []byte) error {
			if filepath.Base(path) == target {
				return errors.New("injected failure")
			}
			return replaceFile(path, data)
		}
	}
	plan := func(t *testing.T, f addFixture, o fallbackAddOptions) *fallbackAddPlan {
		t.Helper()
		cfg, err := config.Load(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		o.cfgPath, o.probeCap, o.set = f.cfg, 2, map[string]bool{}
		p, err := planFallbackAdd(cfg, o, f.lookPath)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Cleanup(func() { fallbackAddReplace = replaceFile })

	t.Run("fallbacks file appears before the write", func(t *testing.T) {
		f := newAddFixture(t)
		p := plan(t, f, fallbackAddOptions{agent: "claude", group: "pool-b"})
		if err := os.WriteFile(f.fallbacks, []byte("# someone else's file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := f.files(t)
		if code, err := writeFallbackAdd(p); code != exitFailed || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
			t.Fatalf("%d %v", code, err)
		}
	})
	t.Run("config update fails after creating the file", func(t *testing.T) {
		f := newAddFixture(t)
		p := plan(t, f, fallbackAddOptions{agent: "claude", group: "pool-b", enableHandoff: true})
		before := f.files(t)
		fallbackAddReplace = failing("keepgoing.yaml")
		code, err := writeFallbackAdd(p)
		fallbackAddReplace = replaceFile
		if code != exitFailed || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
			t.Fatalf("%d %v", code, err)
		}
		if _, err := os.Stat(f.fallbacks); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created fallbacks file was left behind")
		}
		if _, err := config.Load(f.cfg); err != nil {
			t.Fatal(err)
		}
	})
	existing := func(t *testing.T) addFixture {
		f := newAddFixture(t, "--fallback", "claude:pool-b")
		raw, _ := os.ReadFile(f.cfg)
		off := strings.Replace(string(raw), "- rate_limit", "", 1)
		off = strings.Replace(off, "failover_on:", "failover_on: []", 1)
		if err := os.WriteFile(f.cfg, []byte(off), 0o600); err != nil {
			t.Fatal(err)
		}
		if cfg, err := config.Load(f.cfg); err != nil || len(cfg.Loop.Errors.FailoverOn) != 0 {
			t.Fatalf("fixture: %v\n%s", err, off)
		}
		return f
	}
	t.Run("fallbacks update fails", func(t *testing.T) {
		f := existing(t)
		p := plan(t, f, fallbackAddOptions{agent: "codex", account: f.account("codex-b"), enableHandoff: true})
		before := f.files(t)
		fallbackAddReplace = failing("fallbacks.yaml")
		code, err := writeFallbackAdd(p)
		fallbackAddReplace = replaceFile
		if code != exitFailed || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
			t.Fatalf("%d %v", code, err)
		}
	})
	t.Run("handoff update fails after the fallback is added", func(t *testing.T) {
		f := existing(t)
		p := plan(t, f, fallbackAddOptions{agent: "codex", account: f.account("codex-b"), enableHandoff: true})
		cfgBefore, _ := os.ReadFile(f.cfg)
		fallbackAddReplace = failing("keepgoing.yaml")
		code, err := writeFallbackAdd(p)
		fallbackAddReplace = replaceFile
		if code != exitFailed || err == nil || !strings.Contains(err.Error(), "the fallback was added") || strings.Contains(err.Error(), "nothing written") {
			t.Fatalf("%d %v", code, err)
		}
		cfgAfter, _ := os.ReadFile(f.cfg)
		cfg, loadErr := config.Load(f.cfg)
		if !bytes.Equal(cfgBefore, cfgAfter) || loadErr != nil || len(cfg.FallbackSet.Candidates) != 2 || len(cfg.Loop.Errors.FailoverOn) != 0 {
			t.Fatalf("loop is not in the reported state: %v", loadErr)
		}
	})
}
