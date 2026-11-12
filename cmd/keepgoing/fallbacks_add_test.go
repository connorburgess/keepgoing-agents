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
	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// addFixture is a disabled loop created by guided setup, plus fictional
// account directories. Its lookPath names executables that do not exist, so
// no test can run a provider.
type addFixture struct {
	setupFixture
	cfg, fallbacks string
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
	code := fallbacksAdd(env{context.Background(), &out, &errOut}, append([]string{"-c", f.cfg}, args...), strings.NewReader(input), f.lookPath)
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
			code, out, _ := f.add(t, "codex\n\npool-b\n\n\nno\n"+tail)
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
	if !strings.Contains(out, "automatic on rate_limit (unchanged)") || !strings.Contains(out, "position 1 of 2") {
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
	cases := map[string]struct {
		args []string
		want string
	}{
		"cursor":             {[]string{"--agent", "cursor"}, "Cursor cannot be a fallback yet"},
		"bb":                 {[]string{"--agent", "bb"}, "headless claude and codex only"},
		"process":            {[]string{"--agent", "process"}, "headless claude and codex only"},
		"unknown agent":      {[]string{"--agent", "example"}, "unknown agent"},
		"primary group":      {[]string{"--agent", "codex"}, "is the primary's"},
		"shared group":       {[]string{"--agent", "codex", "--group", "pool-b"}, "already used by fallback \"existing\""},
		"repeated id":        {[]string{"--agent", "codex", "--group", "pool-c", "--id", "existing"}, "already in use"},
		"reserved id":        {[]string{"--agent", "codex", "--group", "pool-c", "--id", "primary"}, "already in use"},
		"invalid id":         {[]string{"--agent", "codex", "--group", "pool-c", "--id", "Not Valid"}, "lowercase letters"},
		"invalid group":      {[]string{"--agent", "codex", "--group", "pool c"}, "quota group labels"},
		"missing account":    {[]string{"--agent", "codex", "--group", "pool-c", "--account-dir", filepath.Join(f.root, "accounts", "absent")}, "account directory"},
		"position too large": {[]string{"--agent", "codex", "--group", "pool-c", "--position", "3"}, "position must be between 1 and 2"},
		"model with space":   {[]string{"--agent", "codex", "--group", "pool-c", "--model", "two words"}, "single model name"},
		"model as flag":      {[]string{"--agent", "codex", "--group", "pool-c", "--probe-model", "--danger"}, "single model name"},
		"missing workdir":    {[]string{"--agent", "codex", "--group", "pool-c", "--workdir", filepath.Join(f.root, "absent")}, "project directory"},
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
			if out != "" || f.files(t) != before {
				t.Fatalf("refusal printed a result or changed files: %s", out)
			}
		})
	}
	t.Run("missing CLI", func(t *testing.T) {
		var out, errOut bytes.Buffer
		missing := func(string) (string, error) { return "", errors.New("not found") }
		code := fallbacksAdd(env{context.Background(), &out, &errOut}, []string{"-c", f.cfg, "--non-interactive", "--agent", "codex", "--group", "pool-c"}, strings.NewReader(""), missing)
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
	plan, err := planFallbackAdd(cfg, fallbackAddOptions{cfgPath: f.cfg, agent: "claude", group: "pool-b", probeCap: 2}, f.lookPath)
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
	if code := run(context.Background(), []string{"fallbacks", "list", "-c", f.cfg}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "Not probed yet: claude-pool-b.") {
		t.Fatalf("list: %d %s %s", code, out.String(), errOut.String())
	}
}
