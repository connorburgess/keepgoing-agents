package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/prompt"
)

func TestInitGoalPreviewCreateAndRevisedPrompt(t *testing.T) {
	for _, adapter := range []string{"bb", "claude", "codex", "cursor"} {
		t.Run(adapter, func(t *testing.T) {
			f := newSetupFixture(t)
			f.destination = filepath.Join(f.root, "private", "café 项目")
			task := "Private fictional objective: update the example. Acceptance: verify its checks."
			source := filepath.Join(f.root, "original-task.md")
			if err := os.WriteFile(source, []byte(task), 0600); err != nil {
				t.Fatal(err)
			}
			args := append(f.args(), "--task", "", "--task-file", source, "--agent", adapter, "--with-goal", "--json")
			if adapter == "bb" {
				args = append(args, "--thread", "thr_goal_fixture", "--provider", "codex")
			}
			code, out, stderr := f.run(t, append(args, "--dry-run"), "")
			if code != 0 || strings.Contains(out+stderr, task) {
				t.Fatalf("preview: %d %s", code, stderr)
			}
			preview := decodeInitReport(t, out)
			if preview.Goal == nil || !preview.Goal.PromptContext || preview.Goal.Record != nil || preview.TaskBinding != "snapshot" || preview.TaskPath != filepath.Join(f.destination, "task.md") {
				t.Fatalf("preview: %+v", preview)
			}
			if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
				t.Fatal("preview changed filesystem")
			}
			code, out, stderr = f.run(t, args, "")
			if code != 0 || strings.Contains(out+stderr, task) || strings.Contains(out+stderr, source) {
				t.Fatalf("create: %d %s", code, stderr)
			}
			created := decodeInitReport(t, out)
			if created.Goal == nil || created.Goal.Record == nil || created.Goal.Record.Revision != 1 {
				t.Fatal("missing recorded goal")
			}
			preview.Outcome = "created"
			preview.Goal.Record = created.Goal.Record
			if !reflect.DeepEqual(preview, created) {
				t.Fatal("preview/create disagree")
			}
			cfg, err := config.Load(created.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			ctl, err := control.Load(cfg.Loop.StateDir)
			if err != nil || ctl.Enabled {
				t.Fatal("goal setup enabled supervision", err)
			}
			g, err := control.LoadGoal(cfg.Loop.StateDir)
			if err != nil || g == nil || g.Summary() != *created.Goal.Record {
				t.Fatal("goal summary does not match storage", err)
			}
			spec := g.Revisions[0].Spec
			if spec.Title != "example-loop" || !reflect.DeepEqual(spec.ObjectiveRefs, []string{created.TaskPath}) || !reflect.DeepEqual(spec.AcceptanceRefs, spec.ObjectiveRefs) {
				t.Fatal("goal does not reference the task snapshot")
			}
			if err := os.WriteFile(source, []byte("Changed original input"), 0600); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(created.TaskPath)
			if err != nil || string(saved) != task+"\n" {
				t.Fatal("snapshot changed with its original input")
			}
			scaffold, err := cfg.Loop.PromptText()
			if err != nil || strings.Contains(scaffold, task) || !strings.Contains(scaffold, "current goal context") || !strings.Contains(scaffold, "complete -c") || !strings.Contains(scaffold, "await-user -c") {
				t.Fatal("goal scaffold contains task or lacks policy", err)
			}
			message, err := prompt.ComposeWake(cfg.Loop)
			if err != nil || message.GoalID != g.ID || message.GoalRevision != 1 || strings.Contains(message.Text, task) {
				t.Fatal("first goal message", err)
			}
			// A completed hold must survive a goal edit. Fresh readers compose the new
			// revision, but the edit itself does not resume or declare task acceptance.
			if _, err := control.Update(cfg.Loop.StateDir, func(c *control.File) { c.Completed = true }); err != nil {
				t.Fatal(err)
			}
			spec.Title = "Revised private title"
			l, err := lock.Acquire(cfg.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = control.WriteGoal(cfg.Loop.StateDir, cfg.Loop.Name, "", spec, g.ID, 1)
			l.Release()
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := config.Load(created.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			message, err = prompt.ComposeWake(fresh.Loop)
			if err != nil || message.GoalRevision != 2 || !strings.Contains(message.Text, spec.Title) {
				t.Fatal("fresh goal revision not composed", err)
			}
			held, err := control.Load(cfg.Loop.StateDir)
			if err != nil || !held.Completed || held.Enabled {
				t.Fatal("goal edit changed holds")
			}
			var statusOut, statusErr bytes.Buffer
			if code := run(context.Background(), []string{"goal", "show", "-c", created.ConfigPath, "--json"}, &statusOut, &statusErr); code != 0 || strings.Contains(statusOut.String()+statusErr.String(), spec.Title) {
				t.Fatal("ordinary goal output leaked text")
			}
			if runtime.GOOS != "windows" {
				for _, rel := range []string{"task.md", "prompt.md", "state/goal.json"} {
					info, err := os.Stat(filepath.Join(f.destination, rel))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("private file mode", rel, err)
					}
				}
			}
		})
	}
}

func TestInitGoalWizardAndExplicitOptOut(t *testing.T) {
	for _, answer := range []string{"", "no"} {
		t.Run("choice-"+answer, func(t *testing.T) {
			f := newSetupFixture(t)
			args := append(f.args(), "--non-interactive=false")
			// All existing wizard inputs are supplied as flags or accept defaults.
			// Goal choice is first because task/name/project/agent already have flags.
			input := strings.Join([]string{answer, "", "", "", "", "", "", "", "", "", "", ""}, "\n")
			code, out, stderr := f.run(t, args, input)
			if code != 0 {
				t.Fatalf("wizard: %d %s", code, stderr)
			}
			cfg, err := config.Load(filepath.Join(f.destination, config.FileName))
			if err != nil || cfg.Loop.PromptGoal != (answer == "") {
				t.Fatal("wizard goal choice", err)
			}
			if !strings.Contains(out, " doctor -c ") || !strings.Contains(out, " start --foreground -c ") {
				t.Fatal("missing review/start instructions")
			}
		})
	}
}

func TestInitGoalRejectsInvalidTaskAndCleansFailedCreation(t *testing.T) {
	for _, task := range []string{strings.Repeat("x", initTaskLimit+1), "invalid \xff text"} {
		f := newSetupFixture(t)
		code, _, _ := f.run(t, append(f.args(), "--with-goal", "--task", task), "")
		if code == 0 {
			t.Fatal("invalid goal task accepted")
		}
		if _, err := os.Stat(f.destination); !os.IsNotExist(err) {
			t.Fatal("invalid task wrote setup")
		}
	}
	f := newSetupFixture(t)
	// Obtain a valid planned configuration, then make the goal write fail after
	// the configuration and state directory exist. No disabled partial setup is
	// left by this ordinary failure; existing destinations remain untouched.
	o := initOptions{name: "example-loop", project: f.project, directory: f.destination, task: "Fictional task and acceptance", agent: "codex", withGoal: true}
	o.stall = config.DefaultStallAfter
	o.wake = config.DefaultMinWakeInterval
	o.interval = config.DefaultDaemonInterval
	o.backoff = config.DefaultBackoff
	o.nudges = config.DefaultMaxNudges
	o.reprompts = config.DefaultMaxReprompts
	o.probeCap = 2
	files, _, err := planInit(&o, f.lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writeInit(f.destination, files, &control.GoalSpec{}); err == nil {
		t.Fatal("invalid goal write succeeded")
	}
	if _, err := os.Stat(f.destination); !os.IsNotExist(err) {
		t.Fatal("failed setup was not cleaned up", err)
	}
}

func TestInitGoalTaskFileBounds(t *testing.T) {
	for _, kind := range []string{"directory", "oversized", "invalid-utf8"} {
		t.Run(kind, func(t *testing.T) {
			f := newSetupFixture(t)
			path := filepath.Join(f.root, "private-source")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), initTaskLimit+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-utf8":
				if err := os.WriteFile(path, []byte{255}, 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := f.run(t, append(f.args(), "--task", "", "--task-file", path, "--with-goal", "--json"), "")
			if code == 0 || out != "" || strings.Contains(stderr, path) {
				t.Fatal("invalid goal source not privately rejected")
			}
			if _, err := os.Stat(f.destination); !os.IsNotExist(err) {
				t.Fatal("invalid source wrote setup")
			}
		})
	}
}
