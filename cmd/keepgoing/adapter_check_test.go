//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/conformance"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// checkLoop is a process-adapter loop whose wrapper is a shell script.
func checkLoop(t *testing.T, status string) (cfg, dir string) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	dir = t.TempDir()
	wrapper := filepath.Join(dir, "wrapper")
	testexec.Script(t, wrapper, `#!/bin/sh
d="`+dir+`"
case "$1" in
status) `+status+` ;;
resume) cat > "$d/received"; echo "$*" >> "$d/resume-args" ;;
stop) echo stop >> "$d/stops" ;;
esac
`)
	cfg = filepath.Join(dir, "keepgoing.yaml")
	write(t, cfg, fmt.Sprintf("version: 1\nloop:\n  name: example-wrapper\n  state_dir: state\n  adapter: process\n  process:\n    status_command: [%[1]s, status]\n    resume_command: [%[1]s, resume]\n    stop_command: [%[1]s, stop]\n    timeout: 2s\n  prompt: Continue the fictional task.\n", wrapper), 0o600)
	return cfg, dir
}

func adapterCheck(t *testing.T, args ...string) (int, adapterCheckReport, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(context.Background(), append([]string{"adapter", "check", "--json"}, args...), &out, &errs)
	var r adapterCheckReport
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
	}
	return code, r, out.String() + errs.String()
}

func resultOf(r adapterCheckReport, id string) conformance.Result {
	for _, res := range r.Results {
		if res.ID == id {
			return res
		}
	}
	return conformance.Result{}
}

// By default the check reads the real agent's status and nothing else: it
// sends nothing, stops nothing and writes nothing.
func TestAdapterCheckReadsStatusOnly(t *testing.T) {
	cfg, dir := checkLoop(t, `echo '{"state":"idle"}'`)
	before := snapshotTree(t, dir)
	code, r, raw := adapterCheck(t, "-c", cfg)
	if code != exitOK || r.SchemaVersion != 1 || r.Adapter != "process" || r.Target != "primary" || r.Sent {
		t.Fatalf("%d %s", code, raw)
	}
	if got := resultOf(r, "one-state"); got.Outcome != conformance.Pass || got.Detail != "read as idle" {
		t.Fatalf("%+v", got)
	}
	if !r.Declared.Stoppable || !r.Declared.StructuredErrors || !r.Declared.StructuredWaiting || r.Declared.ExactResume {
		t.Fatalf("%+v", r.Declared)
	}
	for _, res := range r.Results {
		if res.ID != "one-state" && res.Outcome != conformance.Skipped {
			t.Errorf("%s ran against the real agent: %s", res.ID, res.Outcome)
		}
	}
	if !strings.Contains(resultOf(r, "live-send").Detail, "allow it explicitly") || !strings.Contains(resultOf(r, "send-once").Detail, "stand-in") {
		t.Fatal(raw)
	}
	if after := snapshotTree(t, dir); after != before {
		t.Fatal("a read-only check sent, stopped or wrote something")
	}
	if strings.Contains(raw, dir) || strings.Contains(raw, "Continue the fictional task") {
		t.Fatal("the report printed a path or the task prompt")
	}
	// The text form says the same and names what was not run.
	var text, errs bytes.Buffer
	if code := run(context.Background(), []string{"adapter", "check", "-c", cfg}, &text, &errs); code != exitOK ||
		!strings.Contains(text.String(), "nothing was sent") || !strings.Contains(text.String(), "pass     one-state") {
		t.Fatalf("%d %s", code, text.String())
	}
}

// What a real wrapper gets wrong is reported in words it can act on.
func TestAdapterCheckReportsWhatAWrapperGetsWrong(t *testing.T) {
	for name, tt := range map[string]struct {
		status, limit string
		want          conformance.Outcome
		says          string
	}{
		"waiting on a person":   {`echo '{"state":"idle","awaitingInput":true}'`, "30s", conformance.Pass, "read as idle, awaiting input"},
		"a coded failure":       {`echo '{"state":"error","errorClass":"rate_limit"}'`, "30s", conformance.Pass, "read as error, class rate_limit"},
		"prose for a class":     {`echo '{"state":"error","errorClass":"usage limit reached"}'`, "30s", conformance.Pass, "read as error, class unknown"},
		"not the status format": {`echo "the agent looks idle"`, "30s", conformance.Pass, "read as unknown; the status read reported an error"},
		"too slow":              {`sleep 1; echo '{"state":"idle"}'`, "200ms", conformance.Fail, "Give the status command a timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, dir := checkLoop(t, tt.status)
			code, r, raw := adapterCheck(t, "-c", cfg, "--limit", tt.limit)
			got := resultOf(r, "one-state")
			if got.Outcome != tt.want || !strings.Contains(got.Detail, tt.says) || (code == exitOK) != (tt.want == conformance.Pass) {
				t.Fatalf("%d %+v", code, got)
			}
			if strings.Contains(raw, dir) || strings.Contains(raw, "looks idle") || strings.Contains(raw, "usage limit reached") {
				t.Fatalf("the report repeated the wrapper's output or a path: %s", raw)
			}
		})
	}
}

// A prompt reaches the real agent only with --send, only once, and only while
// supervision cannot act on it.
func TestAdapterCheckSendsOnlyWhenAskedAndHeld(t *testing.T) {
	cfg, dir := checkLoop(t, `echo '{"state":"idle"}'`)
	received := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "received"))
		return string(b)
	}
	stateDir := filepath.Join(dir, "state")
	if err := control.Save(stateDir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// An enabled loop with no hold would act on the check prompt's turn.
	code, _, raw := adapterCheck(t, "-c", cfg, "--send")
	if code != exitUsage || received() != "" || !strings.Contains(raw, "nothing was sent") {
		t.Fatalf("%d %s", code, raw)
	}
	for name, hold := range map[string]control.File{"paused": {Enabled: true, Paused: true}, "disabled": {}, "completed": {Enabled: true, Completed: true}} {
		os.Remove(filepath.Join(dir, "received"))
		os.Remove(filepath.Join(dir, "resume-args"))
		if err := control.Save(stateDir, hold); err != nil {
			t.Fatal(err)
		}
		saved, _ := os.ReadFile(filepath.Join(stateDir, control.FileName))
		code, r, raw := adapterCheck(t, "-c", cfg, "--send")
		if code != exitOK || !r.Sent || resultOf(r, "live-send").Outcome != conformance.Pass || received() != conformance.CheckPrompt {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		args, _ := os.ReadFile(filepath.Join(dir, "resume-args"))
		if strings.Count(string(args), "\n") != 1 || strings.Contains(string(args), "conformance-token") {
			t.Fatalf("%s: the prompt was sent %d times or as an argument: %s", name, strings.Count(string(args), "\n"), args)
		}
		if after, _ := os.ReadFile(filepath.Join(stateDir, control.FileName)); !bytes.Equal(after, saved) {
			t.Fatalf("%s: the check changed a control", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "stops")); !os.IsNotExist(err) {
			t.Fatalf("%s: the check stopped the agent", name)
		}
	}
}

func TestAdapterCheckArgumentsAndContractList(t *testing.T) {
	cfg, dir := checkLoop(t, `echo '{"state":"idle"}'`)
	for _, args := range [][]string{
		{"-c", cfg, "--target", "no-such-candidate"}, {"-c", filepath.Join(dir, "absent.yaml")}, {"-c", cfg, "extra"},
		{"-c", cfg, "--limit", "0s"}, {"--list", "--send"}, {"-c", cfg, "--unknown"},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"adapter", "check"}, args...), &out, &errs); code != exitUsage || out.Len() != 0 || strings.Contains(errs.String(), dir) {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), errs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"adapter"}, &out, &errs); code != exitUsage {
		t.Fatal(code)
	}
	// The contract list needs no configuration and names every case.
	code, r, raw := adapterCheck(t, "--list")
	if code != exitOK || len(r.Results) != len(conformance.Cases()) || len(r.Results) < 12 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, res := range r.Results {
		if res.ID == "" || res.Contract == "" || res.Outcome != "" {
			t.Fatalf("%+v", res)
		}
	}
}
