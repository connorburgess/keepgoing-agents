package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// A native executable trap proves setup only discovers the provider. It does
// not quietly invoke a version/auth/model command and ignore its failure.
func initCursorInvocationTrap() {
	if path := os.Getenv("KEEPGOING_INIT_CURSOR_CALLED"); path != "" {
		if os.WriteFile(path, nil, 0600) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func TestInitCursorPreviewCreateAndDiscovery(t *testing.T) {
	for _, goal := range []bool{false, true} {
		t.Run(map[bool]string{false: "task", true: "goal"}[goal], func(t *testing.T) {
			f := newSetupFixture(t)
			f.destination = filepath.Join(f.root, "private", "café 项目")
			marker := filepath.Join(f.root, "provider-called")
			t.Setenv("KEEPGOING_INIT_CURSOR_CALLED", marker)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var discovered []string
			lookup := func(name string) (string, error) {
				discovered = append(discovered, name)
				if name == "agent" {
					return self, nil
				}
				return "", exec.ErrNotFound
			}
			task := "Fictional private café\n项目 acceptance."
			args := append(f.args(), "--agent", "cursor", "--task", "", "--task-file", "-", "--json", "--model", "example-model")
			if goal {
				args = append(args, "--with-goal")
			}
			invoke := func(args []string) initReport {
				t.Helper()
				var out, errOut bytes.Buffer
				code := setupLoop(env{context.Background(), &out, &errOut}, args, strings.NewReader(task), lookup)
				if code != 0 || strings.Contains(out.String()+errOut.String(), task) {
					t.Fatalf("setup %d: %s %s", code, &out, &errOut)
				}
				return decodeInitReport(t, out.String())
			}
			preview := invoke(append(args, "--dry-run"))
			if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
				t.Fatal("preview wrote files")
			}
			if preview.Primary.Agent.Adapter != "cursor" || preview.Primary.Agent.Provider != "cursor" || preview.Primary.Agent.Group != "cursor" || preview.Primary.Agent.Model != "example-model" || preview.Primary.Account != "inherited" || preview.Probes != nil || len(preview.Fallbacks) != 0 || len(preview.Limits.FailoverOn) != 0 {
				t.Fatalf("incorrect report: %+v", preview)
			}
			if !strings.Contains(strings.Join(preview.Limitations, " "), initCursorNotice) {
				t.Fatal("missing Cursor limits")
			}
			created := invoke(args)
			preview.Outcome = "created"
			if goal {
				preview.Goal.Record = created.Goal.Record
			}
			if !reflect.DeepEqual(preview, created) {
				t.Fatal("preview and create differ")
			}
			cfg, err := config.Load(created.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Loop.Cursor == nil || cfg.Loop.Cursor.CLI != self || cfg.Loop.Cursor.Workdir != f.project || cfg.Loop.Cursor.Model != "example-model" || len(cfg.Loop.Cursor.Env) != 0 || cfg.FallbackSet != nil || cfg.Loop.PromptGoal != goal {
				t.Fatalf("incorrect Cursor setup: %+v", cfg.Loop)
			}
			ctl, err := control.Load(cfg.Loop.StateDir)
			if err != nil || ctl.Enabled {
				t.Fatal("setup enabled supervision", err)
			}
			path := created.PromptPath
			if goal {
				path = created.TaskPath
			}
			saved, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(saved), task) {
				t.Fatal("private task not preserved", err)
			}
			if !reflect.DeepEqual(discovered, []string{"agent", "agent"}) {
				t.Fatalf("wrong executable discovery: %q", discovered)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("setup invoked provider")
			}
		})
	}
}

func TestInitCursorWizardDiscoveryAndExistingDefaults(t *testing.T) {
	for _, tt := range []struct {
		name      string
		installed []string
		want      string
	}{
		{"cursor only", []string{"agent"}, "cursor"},
		{"codex preferred", []string{"agent", "codex", "claude", "bb"}, "codex"},
		{"claude preferred", []string{"agent", "claude", "bb"}, "claude"},
		{"bb preferred", []string{"agent", "bb"}, "bb"},
		{"desktop only", []string{"cursor"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSetupFixture(t)
			var queried []string
			lookup := func(name string) (string, error) {
				queried = append(queried, name)
				for _, v := range tt.installed {
					if name == v {
						return f.lookPath(name)
					}
				}
				return "", exec.ErrNotFound
			}
			args := []string{"--name", "example-loop", "--project", f.project, "--directory", f.destination, "--task", "Fictional task", "--with-goal=false"}
			if tt.want == "bb" {
				args = append(args, "--thread", "thr_fixture", "--provider", "example")
			}
			var out, errOut bytes.Buffer
			code := setupLoop(env{context.Background(), &out, &errOut}, args, strings.NewReader(strings.Repeat("\n", 20)), lookup)
			for _, v := range queried {
				if v == "cursor" {
					t.Fatal("discovered desktop launcher instead of CLI")
				}
			}
			if tt.want == "" {
				if code == 0 {
					t.Fatal("desktop launcher accepted")
				}
				if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
					t.Fatal("invalid discovery wrote files")
				}
				return
			}
			if code != 0 {
				t.Fatalf("wizard: %d %s", code, &errOut)
			}
			cfg, err := config.Load(filepath.Join(f.destination, "keepgoing.yaml"))
			if err != nil || cfg.Loop.Adapter != tt.want {
				t.Fatalf("default: %+v %v", cfg, err)
			}
			if tt.want == "cursor" {
				if strings.Contains(out.String(), "Next fallback") || strings.Contains(out.String(), "Existing account directory") || strings.Contains(out.String(), "Scheduled probes per fallback") {
					t.Fatal("wizard asked unsupported Cursor questions")
				}
				if !strings.Contains(out.String(), initCursorNotice) || cfg.Loop.Cursor.CLI != filepath.Join(f.root, "not-executable", "agent") || cfg.Loop.Group != "cursor" {
					t.Fatal("missing Cursor identity or limits")
				}
			}
		})
	}
}

func TestInitCursorRejectsUnsupportedOptionsWithoutWrites(t *testing.T) {
	for _, tt := range []struct {
		name   string
		extra  []string
		reason string
	}{
		{"account", []string{"--account-dir", "fictional-account"}, "does not support --account-dir"},
		{"fallback", []string{"--fallback", "codex:another-pool"}, "does not support --fallback"},
		{"cursor fallback", []string{"--agent", "codex", "--fallback", "cursor:another-pool"}, "fallback must be claude or codex"},
		{"thread", []string{"--thread", "thr_fixture"}, "only used with bb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSetupFixture(t)
			args := append(f.args(), "--agent", "cursor", "--json")
			code, out, errOut := f.run(t, append(args, tt.extra...), "")
			if code == 0 || out != "" || !strings.Contains(errOut, tt.reason) {
				t.Fatalf("rejection: %d %q %q", code, out, errOut)
			}
			if _, err := os.Stat(filepath.Dir(f.destination)); !os.IsNotExist(err) {
				t.Fatal("unsupported option wrote setup")
			}
		})
	}
}
