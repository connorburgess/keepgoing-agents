package config

import (
	"strings"
	"testing"
)

const customLoop = `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the loop.
  adapter: custom
  custom:
    name: example-agent
    workdir: /example/project
    values: {model: example-model}
    definition:
      settings: [model]
      start: [example-agent, run, --json, [--model, "{model}"]]
      resume: [example-agent, run, --json, --session, "{session}"]
      output: {format: jsonl, select: {field: type, equals: result}}
      session: {field: session}
      finished: {exit_codes: [0], field: status, equals: [ok]}
      waiting: {field: status, equals: [needs_input]}
      errors:
        field: error.code
        classes: {quota_exceeded: rate_limit}
      probe: {argv: [example-agent, check, --json]}
      limits:
        argv: [example-agent, limits, --json]
        blocked: {field: blocked, equals: ["true"]}
      capabilities: {exact_resume: true, can_be_stopped: true, structured_errors: true, structured_waiting: true}
`

func TestCustomAgentDefinition(t *testing.T) {
	cfg, err := Parse([]byte(customLoop))
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Loop.Custom
	d := c.Definition
	if cfg.Loop.Provider != "example-agent" || cfg.Loop.Group != "example-agent" || cfg.Loop.Model() != "example-model" || cfg.Loop.Describe() != "custom example-agent /example/project (example-model)" {
		t.Fatalf("provider %q group %q model %q describe %q", cfg.Loop.Provider, cfg.Loop.Group, cfg.Loop.Model(), cfg.Loop.Describe())
	}
	if d.Prompt != "stdin" || d.Output.MaxBytes != DefaultCustomOutput || len(d.Start) != 4 || d.Start[3].Optional != true || len(d.Start[3].Parts) != 2 || d.Start[0].Optional {
		t.Fatalf("%+v", d)
	}
	// A minimal definition: one command and an output format. Everything
	// else is undeclared, and exit code 0 means finished.
	minimal := `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the loop.
  adapter: custom
  custom:
    name: example-agent
    workdir: /example/project
    definition:
      start: [example-agent, run]
      output: {format: json}
`
	cfg, err = Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.Loop.Custom.Definition; len(d.Finished.ExitCodes) != 1 || d.Finished.ExitCodes[0] != 0 || d.Capabilities != (CustomCapabilities{}) {
		t.Fatalf("%+v", d)
	}

	// A fallback candidate with a probe in its definition is probed with it.
	fallbacks := "version: 1\ncandidates:\n  - id: example-fallback\n    provider: example-agent\n" + indent(customLoop[strings.Index(customLoop, "  adapter: custom"):], "  ")
	fb, err := ParseFallbacks([]byte(fallbacks))
	if err != nil {
		t.Fatal(err)
	}
	if c := fb.Candidates[0]; c.Probe.Kind != CustomAdapter || c.ProbeCLI() != "" || c.ProbeWorkdir() != "/example/project" || c.ProbeModel(true) != "example-model" {
		t.Fatalf("%+v", c.Probe)
	}
	// Without one it needs a command probe, like any agent keepgoing cannot check.
	noProbe := strings.Replace(fallbacks, "        probe: {argv: [example-agent, check, --json]}\n", "", 1)
	if _, err := ParseFallbacks([]byte(noProbe)); err == nil || !strings.Contains(err.Error(), "probe.kind is required") {
		t.Fatalf("a custom fallback without a probe: %v", err)
	}
	if _, err := ParseFallbacks([]byte(noProbe + "    probe: {kind: custom}\n")); err == nil || !strings.Contains(err.Error(), "needs a custom agent whose definition has a probe") {
		t.Fatalf("probe.kind custom without a probe: %v", err)
	}
	if _, err := ParseFallbacks([]byte(fallbacks + "    probe: {kind: custom, cli: other}\n")); err == nil || !strings.Contains(err.Error(), "the definition's probe says what runs") {
		t.Fatalf("probe.cli with a custom probe: %v", err)
	}
}

func indent(s, by string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = by + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

// Validation refuses a definition that could run a shell line, pass a prompt
// as an argument, compare text, or claim something it gives no way to
// observe. Each case changes one thing in a valid definition.
func TestCustomAgentDefinitionRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"no workdir":                      {"    workdir: /example/project\n", "", "custom.workdir is required"},
		"a name with spaces":              {"name: example-agent", "name: Example Agent", "custom.name must be"},
		"a built-in name":                 {"name: example-agent", "name: claude", "name of a built-in adapter"},
		"no start":                        {"      start: [example-agent, run, --json, [--model, \"{model}\"]]\n", "", "custom.definition.start must have"},
		"a command line, not argv":        {"start: [example-agent, run, --json, [--model, \"{model}\"]]", "start: \"example-agent run --json\"", "cannot unmarshal"},
		"the executable from a setting":   {"start: [example-agent,", "start: [\"{model}\",", "the first argument is the executable"},
		"the prompt as an argument":       {"start: [example-agent, run,", "start: [example-agent, \"{prompt}\",", "a prompt is never passed as an argument"},
		"prompt: argument":                {"      output:", "      prompt: argument\n      output:", "prompt must be stdin or file"},
		"prompt file without the path":    {"      output:", "      prompt: file\n      output:", "must refer to {prompt_file}"},
		"prompt_file with stdin":          {"start: [example-agent, run,", "start: [example-agent, \"{prompt_file}\",", "{prompt_file} needs prompt: file"},
		"session outside resume":          {"start: [example-agent, run,", "start: [example-agent, \"{session}\",", "{session} is available to resume only"},
		"an undeclared setting":           {"[--model, \"{model}\"]]", "[--model, \"{model}\"], \"{effort}\"]", "{effort} is not a declared setting"},
		"a value for an unknown setting":  {"values: {model: example-model}", "values: {model: example-model, effort: high}", "the definition accepts no such setting"},
		"a multi-line value":              {"values: {model: example-model}", "values: {model: \"a\\nb\"}", "must be one line"},
		"a reserved setting name":         {"settings: [model]", "settings: [model, session]", "is reserved"},
		"an unmatched brace":              {"start: [example-agent, run,", "start: [example-agent, \"{model\",", "unmatched brace"},
		"no output format":                {"output: {format: jsonl, select: {field: type, equals: result}}", "output: {}", "output.format must be json or jsonl"},
		"text output":                     {"format: jsonl", "format: text", "output.format must be json or jsonl"},
		"an output bound that is too big": {"format: jsonl,", "format: jsonl, max_bytes: 999999999,", "max_bytes must be between"},
		"comparing text":                  {"equals: [needs_input]", "equals: [\"needs input from the user\"]", "only codes are compared, never text"},
		"an error code that is text":      {"classes: {quota_exceeded: rate_limit}", "classes: {\"quota exceeded\": rate_limit}", "only codes are compared, never text"},
		"a class outside the allowlist":   {"classes: {quota_exceeded: rate_limit}", "classes: {quota_exceeded: throttled}", "unknown error class"},
		"mapping a code to none":          {"classes: {quota_exceeded: rate_limit}", "classes: {quota_exceeded: none}", "unknown error class"},
		"a field path with a wildcard":    {"field: error.code", "field: \"error.*\"", "not a field path"},
		"a field without values":          {"waiting: {field: status, equals: [needs_input]}", "waiting: {field: status}", "field and equals go together"},
		"a waiting rule with nothing":     {"waiting: {field: status, equals: [needs_input]}", "waiting: {}", "gives nothing to observe"},
		"two session sources":             {"session: {field: session}", "session: {field: session, file: .example/session}", "exactly one of field and file"},
		"a session file outside":          {"session: {field: session}", "session: {file: ../session}", "inside the working directory"},
		"an absolute session file":        {"session: {field: session}", "session: {file: /tmp/session}", "inside the working directory"},
		"finished and error on one code":  {"classes: {quota_exceeded: rate_limit}", "classes: {quota_exceeded: rate_limit}\n        exit_codes: {0: network}", "not a failing exit code"},
		"limits blocked by exit code":     {"blocked: {field: blocked, equals: [\"true\"]}", "blocked: {exit_codes: [3]}", "limits.blocked gives nothing to observe"},
		// Capabilities need the rule that lets them be observed.
		"exact resume without a session":  {"      session: {field: session}\n", "", "exact_resume needs a session source"},
		"exact resume without resume":     {"      resume: [example-agent, run, --json, --session, \"{session}\"]\n", "", "exact_resume needs a session source"},
		"resume that ignores the session": {"--session, \"{session}\"]", "--continue]", "exact_resume needs a session source"},
		"structured errors without rules": {"      errors:\n        field: error.code\n        classes: {quota_exceeded: rate_limit}\n", "", "structured_errors needs errors rules"},
		"structured waiting without rule": {"      waiting: {field: status, equals: [needs_input]}\n", "", "structured_waiting needs a waiting rule"},
		"child work":                      {"can_be_stopped: true,", "can_be_stopped: true, child_work: true,", "child_work cannot be declared yet"},
		"retries itself":                  {"can_be_stopped: true,", "can_be_stopped: true, retries_itself: true,", "retries_itself cannot be declared yet"},
		"fails over itself":               {"can_be_stopped: true,", "can_be_stopped: true, fails_over_itself: true,", "fails_over_itself cannot be declared yet"},
		"schedules itself":                {"can_be_stopped: true,", "can_be_stopped: true, schedules_itself: true,", "schedules_itself cannot be declared yet"},
		// And a rule without its capability would be silently ignored.
		"errors without the capability":  {"structured_errors: true, ", "", "errors is used only with capabilities.structured_errors"},
		"waiting without the capability": {", structured_waiting: true", "", "waiting is used only with capabilities.structured_waiting"},
		"resume without the capability":  {"exact_resume: true, ", "", "resume and session are used only with capabilities.exact_resume"},
		"an unknown key":                 {"      session: {field: session}\n", "      session: {field: session}\n      shell: \"example-agent | tee log\"\n", "field shell not found"},
		"a second agent block":           {"  adapter: custom\n", "  adapter: custom\n  claude: {workdir: /example/project}\n", "claude is set but the adapter is custom"},
	} {
		if !strings.Contains(customLoop, tc.old) {
			t.Fatalf("%s: the valid definition has no %q", name, tc.old)
		}
		_, err := Parse([]byte(strings.Replace(customLoop, tc.old, tc.new, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A setting used outside an optional group must have a value.
	required := strings.Replace(strings.Replace(customLoop, `[--model, "{model}"]]`, `"--model={model}"]`, 1), "    values: {model: example-model}\n", "", 1)
	if _, err := Parse([]byte(required)); err == nil || !strings.Contains(err.Error(), "{model} has no value") {
		t.Errorf("a required setting with no value: %v", err)
	}
	// The adapter without its block, and the block under another adapter.
	if _, err := Parse([]byte(strings.Replace(customLoop, "  adapter: custom\n", "  adapter: bb\n  bb: {thread: thr_example123}\n", 1))); err == nil || !strings.Contains(err.Error(), "custom is set but the adapter is bb") {
		t.Errorf("custom under bb: %v", err)
	}
}
