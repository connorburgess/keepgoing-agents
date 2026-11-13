package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

var _ adapter.Adapter = (*Headless)(nil)

// The registry holds the agents ported to the contract, each complete.
func TestRegistryHoldsCompleteSpecifications(t *testing.T) {
	var names []string
	for _, s := range Specs() {
		names = append(names, s.Name)
		if s.Provider == "" || s.DefaultCLI == "" || len(s.SettingNames) == 0 || s.Prompt != PromptStdin ||
			builtinSettings[s.Name] == nil || s.Argv == nil || s.Read == nil || s.Probe.Command == nil || s.Probe.Judge == nil {
			t.Errorf("%s: incomplete specification", s.Name)
		}
		// What the shared runner provides for every agent on it.
		if !s.Capabilities.Stoppable || !s.Capabilities.ExactResume || !s.Capabilities.StructuredErrors {
			t.Errorf("%s: %+v", s.Name, s.Capabilities)
		}
		// Nothing in the repository establishes these for either ported
		// agent, so neither declares them.
		if s.Capabilities.RetriesItself || s.Capabilities.StructuredWaiting || s.Capabilities.ChildWork || s.Capabilities.SchedulesItself || s.Capabilities.FailsOverItself {
			t.Errorf("%s declares a capability it has no evidence for: %+v", s.Name, s.Capabilities)
		}
		if got, ok := SpecFor(s.Name); !ok || got != s {
			t.Errorf("SpecFor(%q)", s.Name)
		}
		if got, ok := SpecForProvider(s.Provider); !ok || got != s {
			t.Errorf("SpecForProvider(%q)", s.Provider)
		}
		if !KeepsSessionFile(s.Name) {
			t.Errorf("%s keeps a session file", s.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"claude", "codex"}) {
		t.Fatalf("registry = %v", names)
	}
	// Cursor is not on the contract yet; BB and process never will be.
	for _, name := range []string{"cursor", "bb", "process", "", "antigravity"} {
		if _, ok := SpecFor(name); ok {
			t.Errorf("SpecFor(%q) is registered", name)
		}
	}
	if !KeepsSessionFile("cursor") || KeepsSessionFile("bb") || KeepsSessionFile("process") {
		t.Error("KeepsSessionFile")
	}
	// A definition without the agent's own settings builds nothing.
	if h, ok := New(config.Agent{Adapter: "claude"}, t.TempDir()); ok || h != nil {
		t.Error("built a runner without settings")
	}
	if h, ok := New(config.Agent{Adapter: "codex", Claude: &config.Claude{}}, t.TempDir()); ok || h != nil {
		t.Error("built a codex runner from claude settings")
	}
	if _, ok := New(config.Agent{Adapter: "cursor", Cursor: &config.Cursor{}}, t.TempDir()); ok {
		t.Error("built a cursor runner from the registry")
	}
}

// The command line of every turn and probe, written out in full: a change to
// any argument, or to its position, fails here.
func TestSpecificationCommandLines(t *testing.T) {
	claude := config.Agent{Adapter: "claude", Claude: &config.Claude{CLI: "claude", Workdir: "/example/project", Model: "example-model", Effort: "high",
		PermissionMode: "acceptEdits", MaxBudgetUSD: 2.5, ExtraArgs: []string{"--example-flag", "value"}}}
	codex := config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: "codex", Workdir: "/example/project", Model: "example-model", Sandbox: "workspace-write",
		SkipGitRepoCheck: true, ExtraArgs: []string{"--example-flag"}}}
	const session = "00000000-0000-4000-8000-0000000000aa"
	for _, tc := range []struct {
		name    string
		agent   config.Agent
		session string
		want    string
	}{
		{"claude start", claude, "", "claude -p --output-format json --model example-model --effort high --permission-mode acceptEdits --max-budget-usd 2.5 --example-flag value"},
		{"claude resume", claude, session, "claude -p --output-format json --model example-model --effort high --permission-mode acceptEdits --max-budget-usd 2.5 --resume " + session + " --example-flag value"},
		{"claude bare", config.Agent{Adapter: "claude", Claude: &config.Claude{CLI: "claude"}}, "", "claude -p --output-format json"},
		{"codex start", codex, "", "codex exec --json -m example-model --skip-git-repo-check -s workspace-write --example-flag -"},
		{"codex resume", codex, session, `codex exec resume --json -m example-model --skip-git-repo-check -c sandbox_mode="workspace-write" --example-flag ` + session + " -"},
		{"codex bare", config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: "codex"}}, "", "codex exec --json -"},
	} {
		spec, _ := SpecFor(tc.agent.Adapter)
		_, set, _ := Resolve(tc.agent)
		if got := strings.Join(spec.Argv(set, tc.session, ""), " "); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
		// The runner built from the same definition produces the same line.
		dir := t.TempDir()
		h, ok := New(tc.agent, dir)
		if !ok {
			t.Fatalf("%s: no runner", tc.name)
		}
		h.dir.SaveSession(tc.session)
		if got := strings.Join(h.Args(), " "); got != tc.want {
			t.Errorf("%s through the runner: %s", tc.name, got)
		}
	}
	for _, tc := range []struct {
		kind, model string
		want        []string
	}{
		{"claude", "example-model", []string{"claude", "-p", "PROMPT", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--restricted", "--strict-mcp-config", "--tools", "", "--max-turns", "1", "--model", "example-model"}},
		{"claude", "", []string{"claude", "-p", "PROMPT", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--restricted", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}},
		{"codex", "example-model", []string{"codex", "exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-m", "example-model", "PROMPT"}},
		{"codex", "", []string{"codex", "exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "PROMPT"}},
	} {
		spec, _ := SpecFor(tc.kind)
		// A built-in probe's fixed prompt is an argument; nothing is on stdin.
		if got, stdin := spec.Probe.Command(ProbeRequest{CLI: tc.kind, Model: tc.model, Prompt: "PROMPT"}); !reflect.DeepEqual(got, tc.want) || stdin != "" {
			t.Errorf("%s probe (%q): %q", tc.kind, tc.model, got)
		}
	}
}

// What each specification reads from a finished turn, as data.
func TestSpecificationReadings(t *testing.T) {
	finished := func(out string, code int) Status {
		return Status{Phase: Finished, ExitCode: code, ExitCodeKnown: true, Stdout: []byte(out)}
	}
	for _, tc := range []struct {
		name, kind string
		turn       Status
		session    string
		completed  bool
		class      decide.ErrorClass
		source     string
	}{
		{"claude ok", "claude", finished(`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-a"}`, 0), "sess-a", true, decide.ErrUnknown, "claude_result"},
		{"claude rate limit", "claude", finished(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-a","api_error_status":429}`, 1), "sess-a", false, decide.ErrRateLimit, "claude_result"},
		{"claude no result object", "claude", finished(`fictional text`, 1), "", false, decide.ErrUnknown, ""},
		{"codex ok", "codex", finished(`{"type":"thread.started","thread_id":"sess-b"}`+"\n"+`{"type":"turn.completed"}`, 0), "sess-b", true, decide.ErrUnknown, "codex_events"},
		{"codex coded failure", "codex", finished(`{"type":"thread.started","thread_id":"sess-b"}`+"\n"+`{"type":"error","message":"x","code":"rate_limit_exceeded"}`+"\n"+`{"type":"turn.failed","error":{"message":"x","code":"usage_limit_reached"}}`, 1), "sess-b", false, decide.ErrRateLimit, "codex_events"},
		{"codex nothing structured", "codex", finished(`fictional text`, 1), "", false, decide.ErrUnknown, ""},
	} {
		spec, _ := SpecFor(tc.kind)
		r := spec.Read(tc.turn)
		if r.Session != tc.session || r.Completed != tc.completed || r.Class != tc.class || r.Evidence.Source != tc.source || r.Waiting {
			t.Errorf("%s: %+v", tc.name, r)
		}
	}
}

// The shared runner acts on a Reading the same way for any agent. A stand-in
// specification shows the parts neither ported agent uses: waiting on a
// person, and a limit lookup that is asked only when the class is unknown.
func TestRunnerActsOnAnyReading(t *testing.T) {
	lookups := 0
	reading := Reading{}
	spec := &Spec{
		Name:         "stand-in",
		Capabilities: Capabilities{StructuredWaiting: true},
		Argv:         func(Settings, string, string) []string { return []string{"stand-in-cli"} },
		Read:         func(Status) Reading { return reading },
		Limits: &LimitLookup{EvidenceSource: "codex_rate_limits", EvidenceCode: "blocked", Read: func(_ context.Context, cli string, _ []string) (CodexLimits, error) {
			lookups++
			if cli != "stand-in-cli" {
				t.Errorf("lookup cli %q", cli)
			}
			return CodexLimits{Blocked: true}, nil
		}},
	}
	dir := t.TempDir()
	h := &Headless{spec: spec, set: Settings{CLI: "stand-in-cli"}, dir: Dir(dir)}
	finish := func(code string) {
		t.Helper()
		for name, content := range map[string]string{"run.json": `{"pid":1,"startedAt":"2026-01-01T00:00:00Z","argv0":"stand-in-cli"}`, "exit": code + "\n", "stdout": ""} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	observe := func() decide.Observation {
		t.Helper()
		obs, err := h.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}
	if h.Name() != "stand-in" || h.Spec() != spec {
		t.Fatal("name or spec")
	}
	// Completed and exit 0: idle. Waiting on a person is passed through.
	finish("0")
	reading = Reading{Session: "sess-c", Completed: true, Waiting: true, Class: decide.ErrUnknown}
	if obs := observe(); obs.State != decide.Idle || !obs.DeferredInteraction || obs.Evidence.Source != "runner_exit" || h.dir.Session() != "sess-c" || lookups != 0 {
		t.Fatalf("%+v session=%q lookups=%d", obs, h.dir.Session(), lookups)
	}
	// An agent that does not declare structured waiting is not believed.
	spec.Capabilities.StructuredWaiting = false
	if obs := observe(); obs.DeferredInteraction {
		t.Fatalf("undeclared waiting was passed on: %+v", obs)
	}
	if got := strings.Join(spec.ObservationInputs(), ","); got != "ErrorClass,Evidence,ResetsAt,State" {
		t.Fatalf("inputs %s", got)
	}
	spec.Capabilities.StructuredWaiting = true
	if got := strings.Join(spec.ObservationInputs(), ","); got != "DeferredInteraction,ErrorClass,Evidence,ResetsAt,State" {
		t.Fatalf("inputs %s", got)
	}
	// Once the supervisor has saved the hold, the same turn stops raising it,
	// and a later turn can raise it again.
	if err := h.AdoptInteraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if obs := observe(); obs.DeferredInteraction || obs.State != decide.Idle {
		t.Fatalf("an adopted wait was raised again: %+v", obs)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(`{"pid":1,"stopKey":"another-turn","startedAt":"2026-01-02T00:00:00Z","argv0":"stand-in-cli"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs := observe(); !obs.DeferredInteraction {
		t.Fatalf("a later turn's wait was not raised: %+v", obs)
	}
	// A turn the reader does not call completed is an error with its class.
	finish("3")
	reading = Reading{Class: decide.ErrAuth, Evidence: evidence.Clean(evidence.Detail{Source: "claude_result"})}
	if obs := observe(); obs.State != decide.Error || obs.ErrorClass != decide.ErrAuth || obs.Evidence.Source != "claude_result" || lookups != 0 || h.dir.Session() != "sess-c" {
		t.Fatalf("%+v lookups=%d", obs, lookups)
	}
	// Unclassified: the structured lookup is asked, once, and decides.
	reading = Reading{Class: decide.ErrUnknown}
	if obs := observe(); obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit || obs.Evidence.Source != "codex_rate_limits" || lookups != 1 {
		t.Fatalf("%+v lookups=%d", obs, lookups)
	}
}

// settingsTypes is the configuration struct each registered agent's settings
// are read from.
var settingsTypes = map[string]any{"claude": config.Claude{}, "codex": config.Codex{}}

// Each declaration in a specification is tied to the code it describes, so it
// cannot drift: the setting names to the configuration's YAML keys, the
// provider and CLI to the defaults configuration applies, the prompt to what
// the shared runner implements, and each capability to behaviour.
func TestDeclarationsMatchTheCodeTheyDescribe(t *testing.T) {
	const session = "00000000-0000-4000-8000-0000000000aa"
	for _, s := range Specs() {
		// SettingNames are exactly the YAML keys of the agent's settings.
		typ, ok := settingsTypes[s.Name]
		if !ok {
			t.Fatalf("%s: add its settings type to settingsTypes", s.Name)
		}
		var keys []string
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			key, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
			keys = append(keys, key)
		}
		if !reflect.DeepEqual(keys, s.SettingNames) {
			t.Errorf("%s: configuration accepts %v; the specification lists %v", s.Name, keys, s.SettingNames)
		}

		// Provider and DefaultCLI are what a loop that sets neither gets.
		cfg, err := config.Parse([]byte(fmt.Sprintf("version: 1\nloop:\n  name: example-loop\n  state_dir: state\n  adapter: %s\n  %s: {workdir: /example/project}\n  prompt: Continue the loop.\n", s.Name, s.Name)))
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		_, set, ok := Resolve(cfg.Loop.Agent)
		if !ok || cfg.Loop.Provider != s.Provider || set.CLI != s.DefaultCLI || set.Workdir != "/example/project" {
			t.Errorf("%s: configuration defaults to provider %q and CLI %q; the specification says %q and %q", s.Name, cfg.Loop.Provider, set.CLI, s.Provider, s.DefaultCLI)
		}
		// The account variable reaches a turn through the settings' env.
		if s.AccountEnv == "" || !strings.Contains(strings.Join(s.SettingNames, ","), "env") {
			t.Errorf("%s: an account variable needs an env setting", s.Name)
		}

		// ExactResume: the resume command carries the saved session, and a
		// first turn carries none.
		start, resume := strings.Join(s.Argv(set, "", ""), " "), strings.Join(s.Argv(set, session, ""), " ")
		if s.Capabilities.ExactResume != (strings.Contains(resume, session) && !strings.Contains(start, session) && start != resume) {
			t.Errorf("%s: ExactResume=%v but start is %q and resume is %q", s.Name, s.Capabilities.ExactResume, start, resume)
		}
	}
	if len(settingsTypes) != len(Specs()) {
		t.Errorf("settingsTypes lists %d agents, the registry %d", len(settingsTypes), len(Specs()))
	}

	// StructuredErrors: a coded failure is classified and comes with evidence.
	coded := map[string]string{
		"claude": `{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429}`,
		"codex":  `{"type":"turn.failed","error":{"message":"x","code":"usage_limit_reached"}}`,
	}
	for _, s := range Specs() {
		r := s.Read(Status{Phase: Finished, ExitCode: 1, ExitCodeKnown: true, Stdout: []byte(coded[s.Name])})
		if got := r.Class != decide.ErrUnknown && r.Evidence.Source != ""; got != s.Capabilities.StructuredErrors {
			t.Errorf("%s: StructuredErrors=%v but a coded failure read as %+v", s.Name, s.Capabilities.StructuredErrors, r)
		}
	}

	// Prompt: the shared runner passes it on standard input and builds
	// nothing for a specification that asks for anything else.
	// Stoppable: a turn of an agent that does not declare it is not stopped.
	standIn := &Spec{
		Name: "stand-in", Prompt: PromptStdin,
		Argv: func(Settings, string, string) []string { return []string{"stand-in-cli"} },
		Read: func(Status) Reading { return Reading{} },
	}
	specs = append(specs, standIn)
	builtinSettings["stand-in"] = func(config.Agent) (Settings, bool) { return Settings{}, true }
	defer func() { specs = specs[:len(specs)-1]; delete(builtinSettings, "stand-in") }()
	h, ok := New(config.Agent{Adapter: "stand-in"}, t.TempDir())
	if !ok {
		t.Fatal("no runner for a specification with the prompt on stdin")
	}
	var unsupported adapter.ErrUnsupported
	if err := h.Stop(context.Background()); !errors.As(err, &unsupported) {
		t.Errorf("stopping an agent that does not declare Stoppable: %v", err)
	}
	standIn.Capabilities.Stoppable = true
	if err := h.Stop(context.Background()); err != nil {
		t.Errorf("stopping with nothing running: %v", err)
	}
	standIn.Prompt = "argument"
	if _, ok := New(config.Agent{Adapter: "stand-in"}, t.TempDir()); ok {
		t.Error("built a runner for a prompt the shared runner cannot pass")
	}
	names := make([]string, 0, len(coded))
	for name := range coded {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"claude", "codex"}) {
		t.Errorf("coded samples for %v", names)
	}
}
