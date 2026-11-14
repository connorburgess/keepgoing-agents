package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func decodeInitReport(t *testing.T, out string) initReport {
	t.Helper()
	var r initReport
	d := json.NewDecoder(strings.NewReader(out))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		t.Fatalf("JSON result: %v", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatal("extra output after JSON result", err)
	}
	return r
}

func TestInitJSONPreviewAndCreateFromStdin(t *testing.T) {
	f := newSetupFixture(t)
	task := "Fictional private task.\nVerify the example, then finish.\n"
	args := append(f.args(), "--task", "", "--task-file", "-", "--json", "--agent", "bb", "--thread", "thr_setup_fixture", "--provider", "codex")
	code, out, errOut := f.run(t, append(args, "--dry-run"), task)
	if code != 0 || strings.Contains(out+errOut, task) {
		t.Fatalf("preview: %d %s", code, errOut)
	}
	preview := decodeInitReport(t, out)
	if preview.SchemaVersion != 1 || preview.Outcome != "preview" || preview.TaskSource != "stdin" || preview.TaskBinding != "snapshot" || preview.Primary.Agent.BBThread != "thr_setup_fixture" || preview.Primary.Agent.Provider != "codex" || preview.Primary.Account != "inherited" || preview.Probes != nil || len(preview.Fallbacks) != 0 || preview.Fallbacks == nil || len(preview.Limits.FailoverOn) != 0 || preview.Limits.FailoverOn == nil {
		t.Fatalf("invalid preview: %+v", preview)
	}
	if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
		t.Fatal("preview wrote directories")
	}
	code, out, errOut = f.run(t, args, task)
	if code != 0 || strings.Contains(out+errOut, task) {
		t.Fatalf("create: %d %s", code, errOut)
	}
	created := decodeInitReport(t, out)
	if created.Outcome != "created" {
		t.Fatal("missing created result")
	}
	preview.Outcome = "created"
	if !reflect.DeepEqual(preview, created) {
		t.Fatal("preview and create plans differ")
	}
	cfg, err := config.Load(created.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if created.ConfigPath != filepath.Join(f.destination, config.FileName) || created.StateDir != cfg.Loop.StateDir || created.PromptPath != cfg.Loop.PromptFile || created.Primary.Agent != refOf(cfg.Loop.Agent) || created.Limits.Interval != cfg.Loop.Daemon.Interval.String() || created.Limits.MaxNudges != cfg.Loop.Progress.MaxNudges {
		t.Fatal("result disagrees with generated configuration")
	}
	prompt, err := os.ReadFile(created.PromptPath)
	if err != nil || !strings.Contains(string(prompt), strings.TrimSpace(task)) {
		t.Fatal("stdin task not preserved in prompt", err)
	}
	if _, err := os.Stat(created.StateDir); !os.IsNotExist(err) {
		t.Fatal("setup started runtime")
	}
	for _, command := range [][]string{created.Next.Doctor, created.Next.StartForeground, created.Next.StartBackground} {
		if command[0] != keepgoingExecutable() || command[len(command)-2] != "-c" || command[len(command)-1] != created.ConfigPath {
			t.Fatal("invalid argv", command)
		}
	}
	code, out, _ = f.run(t, args, "Fictional replacement")
	after, _ := os.ReadFile(created.PromptPath)
	if code == 0 || out != "" || !bytes.Equal(prompt, after) {
		t.Fatal("overwrite returned success or changed files")
	}
}

func TestInitJSONAccountsAndOrderedFallbacks(t *testing.T) {
	f := newSetupFixture(t)
	account := filepath.Join(f.root, "fictional-private-account")
	if err := os.Mkdir(account, 0o700); err != nil {
		t.Fatal(err)
	}
	args := append(f.args(), "--json", "--dry-run", "--account-dir", account, "--model", "example-model", "--fallback", "claude:second="+account, "--fallback", "codex:third", "--probe-daily-cap", "7", "--wake-every", "2h")
	code, out, errOut := f.run(t, args, "")
	if code != 0 || strings.Contains(out+errOut, account) || strings.Contains(out, "Complete and verify") || strings.Contains(out, "CODEX_HOME") || strings.Contains(out, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("account report: %d %s", code, errOut)
	}
	r := decodeInitReport(t, out)
	if r.Primary.Account != "explicit_directory" || r.Primary.Agent.Model != "example-model" || len(r.Fallbacks) != 2 || r.Fallbacks[0].Target.Agent.Group != "second" || r.Fallbacks[0].Target.Account != "explicit_directory" || r.Fallbacks[1].Target.Agent.Group != "third" || r.Fallbacks[1].Target.Account != "inherited" || r.Probes == nil || r.Probes.DailyCapPerCandidate != 7 || !r.Probes.ExactOutsideCap || !r.Probes.ManualOutsideCap || r.Limits.WakeEvery != "2h0m0s" || !reflect.DeepEqual(r.Limits.FailoverOn, []string{"rate_limit"}) {
		t.Fatalf("bad policy: %+v", r)
	}
}

func TestInitJSONAndStdinRejectInvalidInputWithoutOutputOrWrites(t *testing.T) {
	f := newSetupFixture(t)
	for _, tt := range []struct {
		name  string
		extra []string
		input string
	}{
		{"JSON wizard", []string{"--json", "--non-interactive=false"}, ""},
		{"stdin wizard", []string{"--task", "", "--task-file", "-", "--non-interactive=false"}, ""},
		{"conflicting tasks", []string{"--json", "--task-file", "-"}, "fictional secret"},
		{"empty stdin", []string{"--json", "--task", "", "--task-file", "-"}, " \n"},
		{"oversized stdin", []string{"--json", "--task", "", "--task-file", "-"}, strings.Repeat("x", initTaskLimit+1)},
		{"invalid UTF8", []string{"--json", "--task", "", "--task-file", "-"}, "\xff"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := f.run(t, append(f.args(), tt.extra...), tt.input)
			if code == 0 || out != "" || errOut == "" {
				t.Fatalf("invalid input: %d %q %q", code, out, errOut)
			}
			if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
				t.Fatal("invalid input wrote files")
			}
		})
	}
}

func TestInitTaskReadBoundsErrorsAndCancellation(t *testing.T) {
	for _, size := range []int{initTaskLimit, initTaskLimit + 2} {
		r := strings.NewReader(strings.Repeat("x", size))
		task, err := readInitTask(context.Background(), r)
		if size == initTaskLimit && (err != nil || len(task) != size) {
			t.Fatal("rejected exact bound", err)
		}
		if size > initTaskLimit && (err == nil || r.Len() != 1 || task != "") {
			t.Fatal("did not bound input")
		}
	}
	_, err := readInitTask(context.Background(), privateInitReader{})
	if err == nil || strings.Contains(err.Error(), "fictional-secret") {
		t.Fatal("reader error leaked")
	}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { _, err := readInitTask(ctx, &startedInitReader{Reader: r, started: started}); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stdin read did not begin")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stdin read did not return")
	}
}

type privateInitReader struct{}

func (privateInitReader) Read([]byte) (int, error) { return 0, errors.New("fictional-secret") }

func TestInitJSONWriteFailureDoesNotReportCreated(t *testing.T) {
	f := newSetupFixture(t)
	var out, errOut bytes.Buffer
	// Simulate another creator winning after validation but before writeInit.
	lookup := func(name string) (string, error) {
		if err := os.MkdirAll(f.destination, 0o700); err != nil {
			t.Fatal(err)
		}
		return f.lookPath(name)
	}
	code := setupLoop(env{context.Background(), &out, &errOut}, append(f.args(), "--json"), strings.NewReader(""), lookup)
	if code == 0 || out.Len() != 0 {
		t.Fatal("write failure claimed creation")
	}
	entries, err := os.ReadDir(f.destination)
	if err != nil || len(entries) != 0 {
		t.Fatal("competing directory modified", err)
	}
}

func TestInitStdinTextOutputAndJSONFileInput(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		f := newSetupFixture(t)
		task := "Fictional private multiline task.\nFinish the example."
		args := append(f.args(), "--task", "", "--task-file", "-")
		if jsonMode {
			path := filepath.Join(f.root, "private-source.md")
			if err := os.WriteFile(path, []byte(task), 0o600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--task-file", path, "--json")
		}
		code, out, errOut := f.run(t, args, task)
		if code != 0 || strings.Contains(out+errOut, task) {
			t.Fatalf("%d %s", code, errOut)
		}
		if jsonMode {
			if decodeInitReport(t, out).TaskSource != "file" || strings.Contains(out, "private-source.md") {
				t.Fatal("file source report")
			}
		} else if !strings.Contains(out, "Created private setup") {
			t.Fatal("missing ordinary instructions")
		}
	}
}

func TestInitJSONOutputFailureRetainsCreatedSetup(t *testing.T) {
	f := newSetupFixture(t)
	var errs bytes.Buffer
	code := setupLoop(env{context.Background(), failingInitWriter{}, &errs}, append(f.args(), "--json"), strings.NewReader(""), f.lookPath)
	if code != exitFailed || strings.Contains(errs.String(), "fictional-secret") {
		t.Fatal("failed output not reported safely")
	}
	if _, err := config.Load(filepath.Join(f.destination, config.FileName)); err != nil {
		t.Fatal("created setup not retained", err)
	}
}

type failingInitWriter struct{}

func (failingInitWriter) Write([]byte) (int, error) { return 0, errors.New("fictional-secret") }

type startedInitReader struct {
	io.Reader
	started chan struct{}
}

func (r *startedInitReader) Read(b []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	return r.Reader.Read(b)
}

func TestInitTaskFileSnapshotBinding(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonMode), func(t *testing.T) {
			f := newSetupFixture(t)
			source := filepath.Join(f.root, "private-source.md")
			original := "Complete the fictional first task."
			if err := os.WriteFile(source, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append(f.args(), "--task", "", "--task-file", source)
			if jsonMode {
				args = append(args, "--json")
			}
			for _, dryRun := range []bool{true, false} {
				callArgs := append([]string{}, args...)
				if dryRun {
					callArgs = append(callArgs, "--dry-run")
				}
				code, out, errOut := f.run(t, callArgs, "")
				if code != 0 || strings.Contains(out+errOut, original) || strings.Contains(out+errOut, source) {
					t.Fatalf("setup failed or exposed input: code %d", code)
				}
				if jsonMode {
					r := decodeInitReport(t, out)
					if r.TaskSource != "file" || r.TaskBinding != "snapshot" || !slices.Contains(r.Limitations, initTaskBindingNotice) {
						t.Fatal("missing snapshot metadata")
					}
				} else if !strings.Contains(out, initTaskBindingNotice) {
					t.Fatal("missing snapshot explanation")
				}
			}
			cfg, err := config.Load(filepath.Join(f.destination, config.FileName))
			if err != nil {
				t.Fatal(err)
			}
			before, err := cfg.Loop.PromptText()
			if err != nil || !strings.Contains(before, original) {
				t.Fatal("task was not imported", err)
			}
			if err := os.WriteFile(source, []byte("An unrelated fictional replacement."), 0o600); err != nil {
				t.Fatal(err)
			}
			after, err := cfg.Loop.PromptText()
			if err != nil || before != after {
				t.Fatal("original source edit changed the saved prompt", err)
			}
			revised := strings.Replace(before, original, "Complete the revised fictional task.", 1)
			if err := os.WriteFile(cfg.Loop.PromptFile, []byte(revised), 0o600); err != nil {
				t.Fatal(err)
			}
			after, err = cfg.Loop.PromptText()
			if err != nil || after != revised {
				t.Fatal("saved prompt edit was not reread", err)
			}
		})
	}
}
