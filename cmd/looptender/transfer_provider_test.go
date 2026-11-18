package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

func crossProviderArgs() []string {
	return []string{"--allow-provider-change", "--provider", "codex", "--quota-group", "example-codex", "--model=", "--reasoning="}
}

func crossProviderFixture(t *testing.T) (string, string) {
	t.Helper()
	path, dir := transferApplyFixture(t)
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte("provider: codex"), []byte("provider: claude-code"), 1)
	raw = append(raw, []byte("  account: 0123456789abcdef0123456789abcdef\n  probe:\n    kind: claude\n    cli: example-claude\n    model: example-claude\n    env:\n      CLAUDE_CONFIG_DIR: /example/account\n  timing:\n    enabled: false\n    windows: [seven_day]\n")...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load(dir)
	st.Timing = &state.TimingRecord{Pending: &state.TimingMove{From: "primary", To: "example-old"}}
	st.ReapplyRoute = true
	if err := state.Save(dir, st); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func crossProviderRunner(t *testing.T, mutate func(string, map[string]any)) doctorRun {
	return transferRunner(t, func(id string, v map[string]any) {
		if id == "thr_source" {
			v["thread"].(map[string]any)["providerId"] = "claude-code"
		}
		if mutate != nil {
			mutate(id, v)
		}
	})
}

func callProviderTransfer(t *testing.T, path string, run doctorRun, step func(string) error, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := transferApply(env{ctx: context.Background(), stdout: &out, stderr: &out}, append(args, "-c", path), run, step)
	if strings.Contains(out.String(), "PRIVATE-") {
		t.Fatal("private output")
	}
	return code, out.String()
}

func TestTransferProviderRecoveryAndPreservation(t *testing.T) {
	for _, boundary := range []string{"none", "held", "state", "config"} {
		t.Run(boundary, func(t *testing.T) {
			path, dir := crossProviderFixture(t)
			before, _ := config.Load(path)
			ctlBefore, _ := control.Load(dir)
			stBefore, _ := state.Load(dir)
			kept := map[string][]byte{}
			for _, name := range []string{control.GoalName, "sends.json", "progress.json", "deliveries.json"} {
				kept[name], _ = os.ReadFile(filepath.Join(dir, name))
			}
			args := append([]string{"apply", "--from", "thr_source", "--thread", "thr_destination"}, crossProviderArgs()...)
			code, out := callProviderTransfer(t, path, crossProviderRunner(t, nil), func(step string) error {
				if step == boundary {
					return errors.New("fixture interruption")
				}
				return nil
			}, args...)
			if boundary != "none" {
				if code != exitFailed {
					t.Fatal(code, out)
				}
				ctl, _ := control.Load(dir)
				if ctl.PendingTransfer == nil || !ctl.PendingTransfer.Valid() || ctl.PendingTransfer.Version != 2 || !ctl.AwaitingUser {
					t.Fatal("missing provider recovery fence")
				}
				cfg, _ := config.Load(path)
				if checkTransferSources(cfg, false) == nil || checkTransferSources(cfg, true) != nil {
					t.Fatal("pending fence failed")
				}
				// A changed provider at either end must not be accepted on recovery,
				// including after the config has already been replaced.
				code, _ = callProviderTransfer(t, path, crossProviderRunner(t, func(id string, v map[string]any) {
					if id == "thr_source" {
						v["thread"].(map[string]any)["providerId"] = "codex"
					}
				}), nil, "recover", "--id", ctl.PendingTransfer.ID)
				if code != exitFailed {
					t.Fatal("changed source provider recovered")
				}
				code, out = callProviderTransfer(t, path, crossProviderRunner(t, nil), nil, "recover", "--id", ctl.PendingTransfer.ID)
			}
			if code != exitOK {
				t.Fatal(code, out)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			l := cfg.Loop
			if l.Provider != "codex" || l.Group != "example-codex" || l.BB.Model != "" || l.BB.Reasoning != "" || l.Account != "" || l.Timing != nil || l.Probe.Kind != "codex" || l.Probe.CLI != "" || l.Probe.Model != "" || len(l.Probe.Env) != 0 {
				t.Fatalf("stale provider settings: %+v", l)
			}
			ctl, _ := control.Load(dir)
			if ctl.PendingTransfer != nil || ctl.BBTarget != "thr_destination" || ctl.Ownership.Generation != ctlBefore.Ownership.Generation+1 || ctl.Ownership.ThreadID != "thr_destination" || ctl.Enabled != ctlBefore.Enabled || ctl.Paused != ctlBefore.Paused || ctl.Stop != ctlBefore.Stop || ctl.Completed != ctlBefore.Completed || !ctl.AwaitingUser || ctl.Note != ctlBefore.Note {
				t.Fatal("holds/ownership changed")
			}
			st, _ := state.Load(dir)
			if st.Timing != nil || st.ReapplyRoute || st.Fingerprint != stBefore.Fingerprint || st.LastProgressAt != stBefore.LastProgressAt || st.LastWakeAt != stBefore.LastWakeAt || st.Nudges != stBefore.Nudges || st.Reprompts != stBefore.Reprompts || st.Handoffs != stBefore.Handoffs {
				t.Fatal("state/accounting not preserved")
			}
			if checkTransferSources(before, true) == nil || checkOwnership(dir, leaseOf(ctlBefore)) == nil {
				t.Fatal("old owner/config not fenced")
			}
			for name, b := range kept {
				after, _ := os.ReadFile(filepath.Join(dir, name))
				if !bytes.Equal(b, after) {
					t.Fatal(name, "changed")
				}
			}
			// No automatic work: even an identity-free caller cannot clear the hold
			// through a tick. The runner above accepts only thread show queries.
			if !ctl.AwaitingUser {
				t.Fatal("transfer resumed")
			}
		})
	}
}

func TestTransferProviderRefusesBeforeMutation(t *testing.T) {
	for _, kind := range []string{"default", "missing_allow", "missing_provider", "missing_model", "missing_reasoning", "missing_group", "wrong_source", "wrong_destination", "project", "environment", "host", "path", "same_provider", "stale_config"} {
		t.Run(kind, func(t *testing.T) {
			path, dir := crossProviderFixture(t)
			flags := crossProviderArgs()
			switch kind {
			case "default":
				flags = nil
			case "missing_allow":
				flags = flags[1:]
			case "missing_provider":
				flags = []string{"--allow-provider-change"}
			case "missing_model":
				flags = flags[:len(flags)-2]
				flags = append(flags, "--reasoning=")
			case "missing_reasoning":
				flags = flags[:len(flags)-1]
			case "missing_group":
				flags = []string{"--allow-provider-change", "--provider", "codex", "--model=", "--reasoning="}
			case "same_provider":
				flags[2] = "claude-code"
			}
			before := transferFiles(t, filepath.Dir(path))
			run := crossProviderRunner(t, func(id string, v map[string]any) {
				th := v["thread"].(map[string]any)
				en := v["environment"].(map[string]any)
				if id == "thr_source" && kind == "wrong_source" {
					th["providerId"] = "example-other"
				}
				if id == "thr_destination" {
					switch kind {
					case "wrong_destination":
						th["providerId"] = "claude-code"
					case "project":
						th["projectId"], en["projectId"] = "proj_other", "proj_other"
					case "environment":
						th["environmentId"], en["id"] = "env_other", "env_other"
					case "host":
						en["hostId"] = "host_other"
					case "path":
						en["path"] = filepath.Join(os.TempDir(), "example-other")
					case "stale_config":
						b, _ := os.ReadFile(path)
						os.WriteFile(path, append(b, []byte("# changed\n")...), 0600)
					}
				}
			})
			args := append([]string{"apply", "--from", "thr_source", "--thread", "thr_destination"}, flags...)
			code, out := callProviderTransfer(t, path, run, nil, args...)
			if code == exitOK {
				t.Fatal("accepted", out)
			}
			ctl, _ := control.Load(dir)
			if ctl.PendingTransfer != nil {
				t.Fatal("invalid intent persisted")
			}
			if kind != "stale_config" {
				// tick.lock is the only permitted new local artifact for metadata refusals.
				delete(before, filepath.Join(dir, "tick.lock"))
				after := transferFiles(t, filepath.Dir(path))
				delete(after, filepath.Join(dir, "tick.lock"))
				if !reflect.DeepEqual(before, after) {
					t.Fatal("refusal mutated private records")
				}
			}
		})
	}
}

func TestTransferProviderPreview(t *testing.T) {
	path, _ := crossProviderFixture(t)
	if code, _, _ := runTransfer(t, path, crossProviderRunner(t, nil)); code != exitFailed {
		t.Fatal("default preview accepted provider change")
	}
	before := transferFiles(t, filepath.Dir(path))
	code, r, _ := runTransfer(t, path, crossProviderRunner(t, nil), crossProviderArgs()...)
	if code != exitOK || r.DestinationRoute == nil || r.DestinationRoute.Provider != "codex" || r.DestinationRoute.Model != "" {
		t.Fatal("explicit preview failed", r)
	}
	if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
		t.Fatal("preview wrote files")
	}
}

func TestTransferProviderDestinationLabelsAndHolds(t *testing.T) {
	for _, hold := range []control.File{
		{Enabled: true}, {}, {Enabled: true, Paused: true}, {Enabled: true, Stop: true}, {Enabled: true, Completed: true}, {Enabled: true, AwaitingUser: true},
	} {
		t.Run(fmt.Sprint(hold.Gate()), func(t *testing.T) {
			path, dir := crossProviderFixture(t)
			hold.Ownership = &control.Ownership{ThreadID: "thr_source", Generation: 7}
			if err := control.Save(dir, hold); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"apply", "--from", "thr_source", "--thread", "thr_destination"}, crossProviderArgs()...)
			args[len(args)-2], args[len(args)-1] = "--model=example-codex-model", "--reasoning=medium"
			code, out := callProviderTransfer(t, path, crossProviderRunner(t, nil), nil, args...)
			if code != exitOK {
				t.Fatal(code, out)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Loop.BB.Model != "example-codex-model" || cfg.Loop.BB.Reasoning != "medium" {
				t.Fatal("destination labels not replaced")
			}
			after, _ := control.Load(dir)
			if after.Enabled != hold.Enabled || after.Paused != hold.Paused || after.Stop != hold.Stop || after.Completed != hold.Completed || !after.AwaitingUser {
				t.Fatal("hold lost or automatically resumed")
			}
		})
	}
}
