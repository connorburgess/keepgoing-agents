package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// exampleInterface is the documented interface of a fictional agent, the only
// thing exampleDefinition was written from:
//
//	example-agent run --json [--model M] [--session ID]
//	    Reads the prompt on standard input and prints JSON lines. The last
//	    line of type "result" has "status" (ok, failed or needs_input),
//	    "session", and on failure "error" with a "code" and an HTTP "status".
//	    Exits 0 when ok, 1 when failed, 2 when it needs input.
//	example-agent check --json [--model M]
//	    One small request, with the same result line.
//	example-agent limits --json
//	    Prints {"blocked": bool, "resets_at": RFC 3339 time}.
//
// The codes it documents: quota_exceeded, unauthorized, overloaded.
const exampleDefinitionYAML = `version: 1
loop:
  name: example-loop
  state_dir: state
  prompt: Continue the loop.
  adapter: custom
  custom:
    name: example-agent
    workdir: %[2]s
    env: {EXAMPLE_HOME: /fictional/account-a}
    values: {model: example-model}
    definition:
      settings: [model]
      start: ["%[1]s", run, --json, [--model, "{model}"]]
      resume: ["%[1]s", run, --json, [--model, "{model}"], --session, "{session}"]
      prompt: stdin
      output:
        format: jsonl
        select: {field: type, equals: result}
      session: {field: session}
      finished: {exit_codes: [0], field: status, equals: [ok]}
      waiting: {exit_codes: [2], field: status, equals: [needs_input]}
      errors:
        field: error.code
        classes: {quota_exceeded: rate_limit, unauthorized: auth, overloaded: overloaded}
        http_status_field: error.status
        exit_codes: {75: network}
      probe:
        argv: ["%[1]s", check, --json, [--model, "{model}"]]
      limits:
        argv: ["%[1]s", limits, --json]
        blocked: {field: blocked, equals: ["true"]}
        resets_at_field: resets_at
      capabilities: {exact_resume: true, can_be_stopped: true, structured_errors: true, structured_waiting: true}
`

// exampleAgent loads the example definition through the configuration
// parser, as a loop would.
func exampleAgent(t *testing.T, cli, workdir string, edit func(*config.Custom)) config.Agent {
	t.Helper()
	cfg, err := config.Parse([]byte(fmt.Sprintf(exampleDefinitionYAML, cli, workdir)))
	if err != nil {
		t.Fatalf("example definition: %v", err)
	}
	if edit != nil {
		edit(cfg.Loop.Custom)
	}
	return cfg.Loop.Agent
}

func exampleSpec(t *testing.T, edit func(*config.Custom)) (*Spec, Settings) {
	t.Helper()
	spec, set, ok := Resolve(exampleAgent(t, "example-agent", "/example/project", edit))
	if !ok {
		t.Fatal("the example definition did not resolve")
	}
	return spec, set
}

const exampleSessionToken = "sess_0123456789abcdef"

func exampleResult(status, extra string) string {
	return `{"type":"note","status":"ok"}` + "\n" + `{"type":"result","status":"` + status + `","session":"` + exampleSessionToken + `"` + extra + `}` + "\n"
}

// A defined agent is a specification like a built-in one.
func TestDefinitionBecomesASpecification(t *testing.T) {
	spec, set := exampleSpec(t, nil)
	if spec.Name != "custom" || spec.Provider != "example-agent" || spec.DefaultCLI != "example-agent" || spec.Prompt != PromptStdin || spec.MaxOutput != config.DefaultCustomOutput ||
		!reflect.DeepEqual(spec.SettingNames, []string{"model"}) || set.CLI != "example-agent" || set.Workdir != "/example/project" || set.Model != "example-model" || set.Env["EXAMPLE_HOME"] != "/fictional/account-a" {
		t.Fatalf("%+v %+v", spec, set)
	}
	if want := (Capabilities{ExactResume: true, Stoppable: true, StructuredErrors: true, StructuredWaiting: true}); spec.Capabilities != want {
		t.Fatalf("%+v", spec.Capabilities)
	}
	if got := strings.Join(spec.ObservationInputs(), ","); got != "DeferredInteraction,ErrorClass,Evidence,ResetsAt,State" {
		t.Fatalf("inputs %s", got)
	}
	if spec.Limits == nil || spec.Probe.Command == nil || spec.Probe.Judge == nil {
		t.Fatal("probe or limit lookup missing")
	}
	// It is not in the registry of built-in agents and is built per loop.
	if _, ok := SpecFor("custom"); ok {
		t.Fatal("a defined agent is registered as built in")
	}
	if KeepsSessionFile("custom") {
		t.Fatal("a defined agent's session identifier is not one diagnostics show")
	}
	if _, _, ok := Resolve(config.Agent{Adapter: "custom"}); ok {
		t.Fatal("resolved a custom agent with no definition")
	}
}

// Command lines, written out. A setting with no value takes its optional
// group with it, and a value is always exactly one argument.
func TestDefinitionCommandLines(t *testing.T) {
	spec, set := exampleSpec(t, nil)
	for _, tc := range []struct {
		name, session string
		values        map[string]string
		want          []string
	}{
		{"start", "", set.Options, []string{"example-agent", "run", "--json", "--model", "example-model"}},
		{"resume", exampleSessionToken, set.Options, []string{"example-agent", "run", "--json", "--model", "example-model", "--session", exampleSessionToken}},
		{"start without a model", "", nil, []string{"example-agent", "run", "--json"}},
		{"resume without a model", exampleSessionToken, nil, []string{"example-agent", "run", "--json", "--session", exampleSessionToken}},
		{"a value with spaces and quotes stays one argument", "", map[string]string{"model": `a b"; rm -rf / #`}, []string{"example-agent", "run", "--json", "--model", `a b"; rm -rf / #`}},
	} {
		s := set
		s.Options = tc.values
		if got := spec.Argv(s, tc.session, "/private/prompt"); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	// The probe: the prompt is on standard input, never an argument, and the
	// probe's own model replaces the turn's.
	argv, stdin := spec.Probe.Command(ProbeRequest{CLI: "ignored", Model: "cheap-model", Prompt: "PROBE PROMPT"})
	if !reflect.DeepEqual(argv, []string{"example-agent", "check", "--json", "--model", "cheap-model"}) || stdin != "PROBE PROMPT" {
		t.Errorf("probe: %q stdin %q", argv, stdin)
	}
	if argv, _ := spec.Probe.Command(ProbeRequest{Prompt: "PROBE PROMPT"}); !reflect.DeepEqual(argv, []string{"example-agent", "check", "--json"}) {
		t.Errorf("probe without a model: %q", argv)
	}

	// With prompt: file the command names the private prompt file and
	// standard input is empty.
	spec, set = exampleSpec(t, func(c *config.Custom) {
		c.Definition.Prompt = "file"
		c.Definition.Start = append(c.Definition.Start, config.Arg{Parts: []string{"--prompt-file={prompt_file}"}})
		c.Definition.Probe.Argv = append(c.Definition.Probe.Argv, config.Arg{Parts: []string{"--prompt-file", "{prompt_file}"}, Optional: true})
	})
	if got := spec.Argv(set, "", "/private/prompt"); spec.Prompt != PromptFile || !reflect.DeepEqual(got, []string{"example-agent", "run", "--json", "--model", "example-model", "--prompt-file=/private/prompt"}) {
		t.Errorf("file prompt: %q", got)
	}
	argv, stdin = spec.Probe.Command(ProbeRequest{Model: "m", Prompt: "PROBE PROMPT", PromptFile: "/private/probe-prompt"})
	if stdin != "" || !reflect.DeepEqual(argv, []string{"example-agent", "check", "--json", "--model", "m", "--prompt-file", "/private/probe-prompt"}) {
		t.Errorf("file probe: %q stdin %q", argv, stdin)
	}
	for _, line := range [][]string{spec.Argv(set, "", "/private/prompt"), argv} {
		if strings.Contains(strings.Join(line, " "), "PROBE PROMPT") {
			t.Errorf("the prompt is in argv: %q", line)
		}
	}
}

// What a finished turn means comes from exit codes and declared codes in JSON
// fields only. Anything else, however plainly worded, is unknown.
func TestDefinitionReadings(t *testing.T) {
	spec, _ := exampleSpec(t, nil)
	turn := func(out string, code int) Status {
		return Status{Phase: Finished, ExitCode: code, ExitCodeKnown: true, Stdout: []byte(out)}
	}
	for _, tc := range []struct {
		name      string
		turn      Status
		session   string
		completed bool
		waiting   bool
		class     decide.ErrorClass
	}{
		{"finished", turn(exampleResult("ok", ""), 0), exampleSessionToken, true, false, decide.ErrUnknown},
		{"ok in the output but a failing exit code", turn(exampleResult("ok", ""), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"exit 0 without the result line", turn(`{"type":"note","status":"ok"}`, 0), "", false, false, decide.ErrUnknown},
		{"waiting on a person", turn(exampleResult("needs_input", ""), 2), exampleSessionToken, false, true, decide.ErrUnknown},
		{"needs_input with another exit code is not the declared wait", turn(exampleResult("needs_input", ""), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"a declared code", turn(exampleResult("failed", `,"error":{"code":"quota_exceeded","status":429}`), 1), exampleSessionToken, false, false, decide.ErrRateLimit},
		{"another declared code", turn(exampleResult("failed", `,"error":{"code":"unauthorized"}`), 1), exampleSessionToken, false, false, decide.ErrAuth},
		{"an undeclared code with a status the code table knows", turn(exampleResult("failed", `,"error":{"code":"internal","status":429}`), 1), exampleSessionToken, false, false, decide.ErrRateLimit},
		{"an undeclared code", turn(exampleResult("failed", `,"error":{"code":"internal"}`), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"a declared exit code", turn(``, 75), "", false, false, decide.ErrNetwork},
		// Prose that says what happened, in every place prose could be.
		{"prose in the code field", turn(exampleResult("failed", `,"error":{"code":"quota exceeded: rate limit reached, try again later"}`), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"prose in another field", turn(exampleResult("failed", `,"message":"quota_exceeded rate_limit 429 unauthorized"`), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"prose that is not JSON", turn("quota_exceeded\nstatus: ok\nrate limit reached\n", 1), "", false, false, decide.ErrUnknown},
		{"a status that is text", turn(exampleResult("failed", `,"error":{"status":"429 Too Many Requests"}`), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"the code under another object", turn(exampleResult("failed", `,"details":{"error":{"code":"quota_exceeded"}}`), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		{"a declared code on a line that is not the result", turn(`{"type":"note","error":{"code":"quota_exceeded"}}`+"\n"+exampleResult("failed", ""), 1), exampleSessionToken, false, false, decide.ErrUnknown},
		// A session identity must be a token; it becomes an argument.
		{"a session that looks like a flag", turn(`{"type":"result","status":"ok","session":"--dangerous"}`, 0), "", true, false, decide.ErrUnknown},
		{"a session with spaces", turn(`{"type":"result","status":"ok","session":"a b"}`, 0), "", true, false, decide.ErrUnknown},
		{"a session that is not text", turn(`{"type":"result","status":"ok","session":{"id":"x"}}`, 0), "", true, false, decide.ErrUnknown},
	} {
		r := spec.Read(tc.turn)
		if r.Session != tc.session || r.Completed != tc.completed || r.Waiting != tc.waiting || r.Class != tc.class {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if tc.class != decide.ErrUnknown && r.Evidence.Source != "custom_result" {
			t.Errorf("%s: no evidence: %+v", tc.name, r.Evidence)
		}
	}

	// Undeclared means unknown: without the rules or their capabilities the
	// same output says nothing.
	bare, _ := exampleSpec(t, func(c *config.Custom) {
		d := &c.Definition
		d.Errors, d.Waiting, d.Session, d.Resume = nil, nil, nil, nil
		d.Capabilities = config.CustomCapabilities{}
	})
	if r := bare.Read(turn(exampleResult("failed", `,"error":{"code":"quota_exceeded","status":429}`), 1)); r.Class != decide.ErrUnknown || r.Session != "" || r.Evidence.Source != "" {
		t.Errorf("undeclared errors were classified: %+v", r)
	}
	if r := bare.Read(turn(exampleResult("needs_input", ""), 2)); r.Waiting || r.Completed {
		t.Errorf("undeclared waiting was read: %+v", r)
	}
	if got := strings.Join(bare.ObservationInputs(), ","); got != "ErrorClass,Evidence,ResetsAt,State" {
		t.Errorf("inputs %s", got)
	}

	// The probe is judged by the same rules.
	for _, tc := range []struct {
		out   string
		code  int
		ok    bool
		class decide.ErrorClass
	}{
		{exampleResult("ok", ""), 0, true, ""},
		{exampleResult("failed", `,"error":{"code":"unauthorized"}`), 1, false, decide.ErrAuth},
		{exampleResult("needs_input", ""), 2, false, decide.ErrUnknown},
		{"quota_exceeded", 1, false, decide.ErrUnknown},
		{strings.Repeat("x", config.DefaultCustomOutput+1), 0, false, decide.ErrUnknown},
	} {
		if v := spec.Probe.Judge([]byte(tc.out), tc.code); v.OK != tc.ok || v.Class != tc.class || len(v.Detail) > 40 {
			t.Errorf("probe exit %d: %+v", tc.code, v)
		}
	}
}

// The session identity may come from a file the agent writes under its
// working directory: one small token in a regular file.
func TestDefinitionSessionFile(t *testing.T) {
	work := t.TempDir()
	agent := exampleAgent(t, "example-agent", work, func(c *config.Custom) {
		c.Definition.Session = &config.SessionSource{File: ".example/session"}
	})
	spec, _, _ := Resolve(agent)
	read := func() string {
		return spec.Read(Status{Phase: Finished, ExitCodeKnown: true, Stdout: []byte(exampleResult("ok", ""))}).Session
	}
	if got := read(); got != "" {
		t.Fatalf("no file: %q", got)
	}
	os.MkdirAll(filepath.Join(work, ".example"), 0o700)
	path := filepath.Join(work, ".example", "session")
	for content, want := range map[string]string{
		"sess_file_1\n":                 "sess_file_1",
		"--flag\n":                      "",
		"two words\n":                   "",
		strings.Repeat("a", 300) + "\n": "",
		"":                              "",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != want {
			t.Errorf("%q: session %q", content[:min(len(content), 20)], got)
		}
	}
	// A directory in its place is not read.
	os.Remove(path)
	os.Mkdir(path, 0o700)
	if got := read(); got != "" {
		t.Errorf("a directory was read as a session: %q", got)
	}
}

// doctor's summary of what a definition lets keepgoing do.
func TestAbilitiesFollowTheDefinition(t *testing.T) {
	full, _ := exampleSpec(t, nil)
	can, cannot := full.Abilities()
	if !strings.Contains(strings.Join(can, "; "), "continue the same session") || !strings.Contains(strings.Join(can, "; "), "hold for a person") || !strings.Contains(strings.Join(can, "; "), "so it can be a fallback") ||
		len(cannot) != 2 {
		t.Fatalf("can %q\ncannot %q", can, cannot)
	}
	bare, _ := exampleSpec(t, func(c *config.Custom) {
		d := &c.Definition
		d.Errors, d.Waiting, d.Session, d.Resume, d.Probe, d.Limits = nil, nil, nil, nil, nil, nil
		d.Capabilities = config.CustomCapabilities{}
	})
	can, cannot = bare.Abilities()
	if len(can) != 2 || len(cannot) != 8 || !strings.Contains(strings.Join(cannot, "; "), "every failure is unknown") || !strings.Contains(strings.Join(cannot, "; "), "stop a running turn") {
		t.Fatalf("can %q\ncannot %q", can, cannot)
	}
}
