package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/conformance"
)

// The test binary stands in for a wrapper's status command and for an agent
// CLI, so these tests run natively on every platform.
const (
	envCheckStatus = "KEEPGOING_TEST_ADAPTER_STATUS" // what the status command prints
	envCheckExit   = "KEEPGOING_TEST_ADAPTER_EXIT"
	envCheckSleep  = "KEEPGOING_TEST_ADAPTER_SLEEP_MS"
	envCheckTrap   = "KEEPGOING_TEST_ADAPTER_TRAP" // a file that records every start
)

func adapterCheckHelper() {
	status, trap := os.Getenv(envCheckStatus), os.Getenv(envCheckTrap)
	if status == "" && trap == "" {
		return
	}
	if trap != "" {
		if f, err := os.OpenFile(trap, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			f.Close()
		}
	}
	if ms, _ := strconv.Atoi(os.Getenv(envCheckSleep)); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	fmt.Println(status)
	code, _ := strconv.Atoi(os.Getenv(envCheckExit))
	os.Exit(code)
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func yamlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// checkLoop is a process-adapter loop whose three commands are this binary.
// trap records every time one of them is started.
func checkLoop(t *testing.T, status string, extra map[string]string) (cfg, dir, trap string) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	dir = t.TempDir()
	trap = filepath.Join(t.TempDir(), "started")
	self := yamlString(os.Args[0])
	env := fmt.Sprintf("      %s: %s\n      %s: %s\n", envCheckStatus, yamlString(status), envCheckTrap, yamlString(trap))
	for k, v := range extra {
		env += fmt.Sprintf("      %s: %s\n", k, yamlString(v))
	}
	cfg = filepath.Join(dir, "keepgoing.yaml")
	put(t, cfg, fmt.Sprintf("version: 1\nloop:\n  name: example-wrapper\n  state_dir: state\n  adapter: process\n  process:\n    status_command: [%[1]s, status]\n    resume_command: [%[1]s, resume]\n    stop_command: [%[1]s, stop]\n    timeout: 5s\n    env:\n%[2]s  prompt: Continue the fictional task.\n", self, env))
	return cfg, dir, trap
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

// For a process adapter the check runs the status command once and nothing
// else: no resume, no stop, and not one byte of the loop's files changes.
func TestAdapterCheckOnAProcessAdapterOnlyRunsTheStatusCommand(t *testing.T) {
	cfg, dir, trap := checkLoop(t, `{"state":"idle"}`, nil)
	// State a check would have left behind, to show it is not touched.
	state := filepath.Join(dir, "state")
	put(t, filepath.Join(state, "control.json"), `{"enabled":true}`)
	before := snapshotTree(t, dir)
	code, r, raw := adapterCheck(t, "-c", cfg)
	if code != exitOK || r.SchemaVersion != 1 || r.Adapter != "process" || r.Target != "primary" {
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
	if after := snapshotTree(t, dir); after != before {
		t.Fatal("the check changed a file")
	}
	if started, _ := os.ReadFile(trap); strings.TrimSpace(string(started)) != "status" {
		t.Fatalf("the check started %q; want the status command once", started)
	}
	if strings.Contains(raw, dir) || strings.Contains(raw, "Continue the fictional task") || strings.Contains(raw, filepath.Base(os.Args[0])) {
		t.Fatal("the report printed a path, a command or the task prompt")
	}
	// The text form says the same and names what was not run.
	var text, errs bytes.Buffer
	if code := run(context.Background(), []string{"adapter", "check", "-c", cfg}, &text, &errs); code != exitOK ||
		!strings.Contains(text.String(), "Nothing was sent, stopped or written") || !strings.Contains(text.String(), "pass     one-state") {
		t.Fatalf("%d %s", code, text.String())
	}
}

// Every other adapter's status read does something a check relies on, so it
// is refused with the reason before anything is built: no file changes, the
// saved BB log position is not moved, and no CLI is started.
func TestAdapterCheckRefusesAdaptersWhoseStatusReadHasSideEffects(t *testing.T) {
	for kind, tt := range map[string]struct{ agent, says string }{
		"bb":     {"  adapter: bb\n  bb:\n    thread: thr_example0001\n", "saved position"},
		"claude": {"  adapter: claude\n  claude:\n    cli: %[1]s\n    workdir: %[2]s\n    env:\n      %[3]s: %[4]s\n", "saves the session"},
		"codex":  {"  adapter: codex\n  codex:\n    cli: %[1]s\n    workdir: %[2]s\n    env:\n      %[3]s: %[4]s\n", "app-server"},
		"cursor": {"  adapter: cursor\n  cursor:\n    cli: %[1]s\n    workdir: %[2]s\n    env:\n      %[3]s: %[4]s\n", "saves the session"},
	} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "")
			t.Setenv("PATH", t.TempDir()) // no bb, claude, codex or agent to find
			dir := t.TempDir()
			trap := filepath.Join(t.TempDir(), "started")
			agent := tt.agent
			if strings.Contains(agent, "%") {
				agent = fmt.Sprintf(agent, yamlString(os.Args[0]), yamlString(dir), envCheckTrap, yamlString(trap))
			}
			cfg := filepath.Join(dir, "keepgoing.yaml")
			put(t, cfg, "version: 1\nloop:\n  name: example\n  state_dir: state\n"+agent+"  prompt: Continue the fictional task.\n")
			// What earlier checks left: a finished turn with a session to
			// save, and the position in a BB thread's log with a notice
			// the daemon has not taken yet.
			target := filepath.Join(dir, "state", "targets", "primary")
			put(t, filepath.Join(target, "run.json"), `{"pid":0,"startedAt":"2026-01-01T00:00:00Z","argv0":"stand-in"}`)
			put(t, filepath.Join(target, "exit"), "1\n")
			put(t, filepath.Join(target, "stdout"), `{"type":"result","subtype":"success","is_error":false,"session_id":"00000000-0000-4000-8000-0000000000c1"}`+"\n")
			put(t, filepath.Join(target, "bb-cursor.json"), `{"version":1,"thread":"thr_example0001","seq":41}`)
			before := snapshotTree(t, dir)
			var out, errs bytes.Buffer
			code := run(context.Background(), []string{"adapter", "check", "-c", cfg}, &out, &errs)
			if code != exitUsage || out.Len() != 0 || !strings.Contains(errs.String(), tt.says) || !strings.Contains(errs.String(), "Nothing was read, started or written") {
				t.Fatalf("%d %s %s", code, out.String(), errs.String())
			}
			if after := snapshotTree(t, dir); after != before {
				t.Fatal("a refused check changed a file")
			}
			if started, err := os.ReadFile(trap); err == nil {
				t.Fatalf("a refused check started the CLI: %s", started)
			}
			if strings.Contains(errs.String(), dir) || strings.Contains(errs.String(), "thr_example0001") {
				t.Fatalf("the refusal printed a path or a thread: %s", errs.String())
			}
		})
	}
	if notReadOnly("an-adapter-added-later") == "" {
		t.Fatal("an adapter nobody has reviewed is read")
	}
}

// What a real wrapper gets wrong is reported in words it can act on, without
// repeating what the wrapper printed.
func TestAdapterCheckReportsWhatAWrapperGetsWrong(t *testing.T) {
	for name, tt := range map[string]struct {
		status string
		extra  map[string]string
		limit  string
		want   conformance.Outcome
		says   string
	}{
		"waiting on a person":   {`{"state":"idle","awaitingInput":true}`, nil, "30s", conformance.Pass, "read as idle, awaiting input"},
		"pending":               {`{"state":"pending"}`, nil, "30s", conformance.Pass, "read as pending"},
		"stopping":              {`{"state":"stopping"}`, nil, "30s", conformance.Pass, "read as stopping"},
		"a coded failure":       {`{"state":"error","errorClass":"rate_limit"}`, nil, "30s", conformance.Pass, "read as error, class rate_limit"},
		"prose for a class":     {`{"state":"error","errorClass":"usage limit reached"}`, nil, "30s", conformance.Pass, "read as error, class unknown"},
		"a failed exit":         {`{"state":"idle"}`, map[string]string{envCheckExit: "3"}, "30s", conformance.Pass, "read as error, class unknown"},
		"not the status format": {`the agent looks idle`, nil, "30s", conformance.Pass, "read as unknown; the status read reported an error"},
		"too slow":              {`{"state":"idle"}`, map[string]string{envCheckSleep: "1500"}, "300ms", conformance.Fail, "Give the status command a timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, dir, _ := checkLoop(t, tt.status, tt.extra)
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

func TestAdapterCheckArgumentsAndContractList(t *testing.T) {
	cfg, dir, trap := checkLoop(t, `{"state":"idle"}`, nil)
	for _, args := range [][]string{
		{"-c", cfg, "--target", "no-such-candidate"}, {"-c", filepath.Join(dir, "absent.yaml")}, {"-c", cfg, "extra"},
		{"-c", cfg, "--limit", "0s"}, {"-c", cfg, "--send"}, {"-c", cfg, "--unknown"},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"adapter", "check"}, args...), &out, &errs); code != exitUsage || out.Len() != 0 || strings.Contains(errs.String(), dir) {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), errs.String())
		}
	}
	// There is no way to send a prompt, and no argument above ran anything.
	if started, err := os.ReadFile(trap); err == nil {
		t.Fatalf("a refused invocation started a command: %s", started)
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"adapter"}, &out, &errs); code != exitUsage {
		t.Fatal(code)
	}
	// The contract list needs no configuration and names every case.
	code, r, raw := adapterCheck(t, "--list")
	if code != exitOK || len(r.Results) != len(conformance.Cases()) || len(r.Results) < 15 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, res := range r.Results {
		if res.ID == "" || res.Contract == "" || res.Outcome != "" || res.ID == "live-send" {
			t.Fatalf("%+v", res)
		}
	}
}
