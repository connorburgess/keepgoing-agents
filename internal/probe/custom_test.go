//go:build unix

package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

const customCandidate = `version: 1
candidates:
  - id: example-fallback
    adapter: custom
    provider: example-agent
    custom:
      name: example-agent
      workdir: %[2]s
      env: {EXAMPLE_HOME: /fictional/account-b}
      values: {model: example-model}
      definition:
        settings: [model]
        start: ["%[1]s", run, --json%[4]s]
        prompt: %[3]s
        output: {format: jsonl, select: {field: type, equals: result}}
        finished: {exit_codes: [0], field: status, equals: [ok]}
        errors:
          field: error.code
          classes: {quota_exceeded: rate_limit}
        probe:
          argv: ["%[1]s", check, --json, [--model, "{model}"]%[4]s]
        limits:
          argv: ["%[1]s", limits, --json]
          blocked: {field: blocked, equals: ["true"]}
          resets_at_field: resets_at
        capabilities: {structured_errors: true}
    probe: {model: cheap-model}
`

// A defined agent's probe runs the definition's command, with the prompt
// passed the way the definition says and never as an argument, in the
// candidate's environment, and is judged by the definition's rules.
func TestCustomProbe(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "example-agent")
	testexec.Script(t, cli, `#!/bin/sh
test "$EXAMPLE_HOME" = /fictional/account-b || exit 9
if [ "$1" = limits ]; then cat "`+dir+`/limits"; exit 0; fi
printf '%s\n' "$@" > "`+dir+`/args"
cat > "`+dir+`/stdin"
for a in "$@"; do case "$a" in --prompt-file=*) cat "${a#--prompt-file=}" > "`+dir+`/prompt-file";; esac; done
cat "`+dir+`/out"
exit $(cat "`+dir+`/exit")
`)
	candidate := func(prompt, extra string) config.Candidate {
		t.Helper()
		fb, err := config.ParseFallbacks([]byte(fmt.Sprintf(customCandidate, cli, dir, prompt, extra)))
		if err != nil {
			t.Fatal(err)
		}
		return fb.Candidates[0]
	}
	set := func(out, exit, limits string) {
		t.Helper()
		for name, content := range map[string]string{"out": out, "exit": exit, "limits": limits} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	const ok = `{"type":"result","status":"ok"}`

	c := candidate("stdin", "")
	set(ok, "0", `{"blocked":false}`)
	r := Run(context.Background(), c, Scheduled, 10*time.Second)
	if !r.OK || r.Kind != "custom" || r.Model != "cheap-model" || r.Detail != "finished" || r.Usage != nil {
		t.Fatalf("%+v", r)
	}
	if read("args") != "check\n--json\n--model\ncheap-model\n" || read("stdin") != Prompt || strings.Contains(read("args"), Prompt) {
		t.Fatalf("args %q stdin %q", read("args"), read("stdin"))
	}
	// The exact tier probes the model the turns use.
	if r := Run(context.Background(), c, Exact, 10*time.Second); !r.OK || r.Model != "example-model" || !strings.Contains(read("args"), "example-model") {
		t.Fatalf("%+v args %q", r, read("args"))
	}
	// A declared code classifies the failure; the limit lookup is not asked.
	set(`{"type":"result","status":"failed","error":{"code":"quota_exceeded"}}`, "1", `{"blocked":true}`)
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); r.OK || r.ErrorClass != "rate_limit" || r.ResetsAt != nil || r.Detail != "classified error" {
		t.Fatalf("%+v", r)
	}
	// An unclassified failure is asked about, and prose decides nothing.
	set(`{"type":"result","status":"failed","message":"rate limit reached"}`, "1", `{"blocked":true,"resets_at":"2026-01-01T12:00:00Z"}`)
	r = Run(context.Background(), c, Scheduled, 10*time.Second)
	if r.OK || r.ErrorClass != "rate_limit" || r.ResetsAt == nil || !r.ResetsAt.Equal(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) || r.Detail != "limits lookup: " || r.Usage != nil {
		t.Fatalf("%+v", r)
	}
	set(`{"type":"result","status":"failed","message":"rate limit reached"}`, "1", `{"blocked":false}`)
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); r.OK || r.ErrorClass != "unknown" {
		t.Fatalf("%+v", r)
	}

	// prompt: file gives the probe a private prompt file and empty stdin.
	c = candidate("file", `, "--prompt-file={prompt_file}"`)
	set(ok, "0", `{"blocked":false}`)
	if r := Run(context.Background(), c, Scheduled, 10*time.Second); !r.OK || read("stdin") != "" || read("prompt-file") != Prompt || strings.Contains(read("args"), Prompt) {
		t.Fatalf("%+v stdin %q file %q args %q", r, read("stdin"), read("prompt-file"), read("args"))
	}
	// The prompt file is removed afterwards.
	for _, arg := range strings.Split(read("args"), "\n") {
		if path, found := strings.CutPrefix(arg, "--prompt-file="); found {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("the probe's prompt file was left behind: %v", err)
			}
		}
	}
}
