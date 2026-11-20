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

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

func TestInitAntigravityPreviewCreateAndDiscovery(t *testing.T) {
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
				if name == "agy" {
					return self, nil
				}
				return "", exec.ErrNotFound
			}
			task := "Fictional private café\n项目 acceptance."
			args := append(f.args(), "--agent", "antigravity", "--task", "", "--task-file", "-", "--json")
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
			if preview.Primary.Agent.Adapter != "antigravity" || preview.Primary.Agent.Provider != "antigravity" || preview.Primary.Agent.Group != "antigravity" || preview.Primary.Account != "inherited" || preview.Probes != nil || len(preview.Fallbacks) != 0 || len(preview.Limits.FailoverOn) != 0 {
				t.Fatalf("incorrect report: %+v", preview)
			}
			if !strings.Contains(strings.Join(preview.Limitations, " "), initAntigravityNotice) {
				t.Fatal("missing Antigravity limits")
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
			if cfg.Loop.Antigravity == nil || cfg.Loop.Antigravity.CLI != self || cfg.Loop.Antigravity.Workdir != f.project || len(cfg.Loop.Antigravity.Env) != 0 || cfg.FallbackSet != nil || cfg.Loop.PromptGoal != goal {
				t.Fatalf("incorrect Antigravity setup: %+v", cfg.Loop)
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
			if !reflect.DeepEqual(discovered, []string{"agy", "agy"}) {
				t.Fatalf("wrong executable discovery: %q", discovered)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("setup invoked provider")
			}
		})
	}
}

func TestInitAntigravityWizardDiscoveryAndExistingDefaults(t *testing.T) {
	for _, tt := range []struct {
		name      string
		installed []string
		want      string
	}{
		{"antigravity only", []string{"agy"}, "antigravity"},
		{"cursor preferred", []string{"agy", "agent"}, "cursor"},
		{"codex preferred", []string{"agy", "codex", "claude", "bb"}, "codex"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSetupFixture(t)
			lookup := func(name string) (string, error) {
				for _, v := range tt.installed {
					if name == v {
						return f.lookPath(name)
					}
				}
				return "", exec.ErrNotFound
			}
			args := []string{"--name", "example-loop", "--project", f.project, "--directory", f.destination, "--task", "Fictional task", "--with-goal=false"}
			var out, errOut bytes.Buffer
			code := setupLoop(env{context.Background(), &out, &errOut}, args, strings.NewReader(strings.Repeat("\n", 20)), lookup)
			if code != 0 {
				t.Fatalf("wizard: %d %s", code, &errOut)
			}
			cfg, err := config.Load(filepath.Join(f.destination, config.FileName))
			if err != nil || cfg.Loop.Adapter != tt.want {
				t.Fatalf("default: %+v %v", cfg, err)
			}
			if tt.want == "antigravity" {
				if strings.Contains(out.String(), "Next fallback") || strings.Contains(out.String(), "Existing account directory") || strings.Contains(out.String(), "Model (blank inherits CLI configuration)") || strings.Contains(out.String(), "Scheduled probes per fallback") {
					t.Fatal("wizard asked unsupported Antigravity questions")
				}
				if !strings.Contains(out.String(), initAntigravityNotice) || cfg.Loop.Antigravity.CLI != filepath.Join(f.root, "not-executable", "agy") || cfg.Loop.Group != "antigravity" {
					t.Fatal("missing Antigravity identity or limits")
				}
			}
		})
	}
}

func TestInitAntigravityRejectsUnsupportedOptionsWithoutWrites(t *testing.T) {
	for _, tt := range []struct {
		name   string
		extra  []string
		reason string
	}{
		{"account", []string{"--account-dir", "fictional-account"}, "does not support --account-dir"},
		{"model", []string{"--model", "example-model"}, "does not support --model"},
		{"fallback", []string{"--fallback", "codex:another-pool"}, "does not support --fallback"},
		{"antigravity fallback", []string{"--agent", "codex", "--fallback", "antigravity:another-pool"}, "fallback must be claude or codex"},
		{"thread", []string{"--thread", "thr_fixture"}, "only used with bb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSetupFixture(t)
			args := append(f.args(), "--agent", "antigravity", "--json")
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

func TestFallbacksAddRejectsAntigravity(t *testing.T) {
	if err := fallbackAddAgentSupported("antigravity"); err == nil || !strings.Contains(err.Error(), "Antigravity cannot be a fallback yet") {
		t.Fatalf("fallbackAddAgentSupported(antigravity) = %v", err)
	}

	f := newSetupFixture(t)
	code, _, errOut := f.run(t, append(f.args(), "--agent", "antigravity"), "")
	if code != 0 {
		t.Fatalf("init failed: %d %s", code, errOut)
	}
	cfgPath := filepath.Join(f.destination, config.FileName)
	var out, errBuf bytes.Buffer
	code = fallbacksAdd(env{context.Background(), &out, &errBuf}, []string{"-c", cfgPath, "--agent", "codex"}, strings.NewReader(""), f.lookPath, nil)
	if code == 0 || !strings.Contains(errBuf.String(), "An Antigravity primary supports standalone loops only") {
		t.Fatalf("fallbacks add on antigravity primary: %d %q %q", code, out.String(), errBuf.String())
	}
}
