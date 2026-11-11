package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func transferFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	path := filepath.Join(root, "loop.yaml")
	raw := "version: 1\nloop:\n  name: fixture\n  state_dir: ./state\n  adapter: bb\n  provider: codex\n  group: fixture-account-label\n  bb:\n    thread: thr_source\n    model: fixture-model\n  prompt: PRIVATE-PROMPT\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func transferResponse(id string) map[string]any {
	return map[string]any{
		"thread":       map[string]any{"id": id, "projectId": "proj_fixture", "environmentId": "env_fixture", "providerId": "codex", "status": "idle", "archivedAt": nil, "deletedAt": nil, "title": "PRIVATE-TITLE"},
		"environment":  map[string]any{"id": "env_fixture", "projectId": "proj_fixture", "hostId": "host_fixture", "path": filepath.Join(os.TempDir(), "PRIVATE-WORKSPACE")},
		"pendingTodos": "PRIVATE-TODOS",
	}
}

func transferRunner(t *testing.T, mutate func(string, map[string]any)) doctorRun {
	t.Helper()
	return func(ctx context.Context, cli string, args []string, env map[string]string, workdir string) ([]byte, error) {
		if len(args) != 4 || args[0] != "thread" || args[1] != "show" || args[3] != "--json" || env != nil || workdir != "" {
			t.Fatalf("unexpected query: %v", args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded query")
		}
		if args[2] != "thr_source" && args[2] != "thr_destination" {
			t.Fatal("unexpected target")
		}
		v := transferResponse(args[2])
		if mutate != nil {
			mutate(args[2], v)
		}
		b, _ := json.Marshal(v)
		return b, nil
	}
}

func runTransfer(t *testing.T, path string, runner doctorRun, args ...string) (int, transferPreview, string) {
	t.Helper()
	var out, errs bytes.Buffer
	flags := append([]string{"preview", "-c", path, "--thread", "thr_destination", "--json"}, args...)
	code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &errs}, flags, runner)
	var r transferPreview
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("no report (%d): %s %s", code, out.String(), errs.String())
	}
	if strings.Contains(out.String()+errs.String(), "PRIVATE-") {
		t.Fatal("private content exposed")
	}
	return code, r, out.String()
}

func transferFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		files[p] = info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[p] += string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestTransferPreviewPreservesPrivateState(t *testing.T) {
	path, dir := transferFixture(t)
	ctl := control.File{Enabled: true, Paused: true, Stop: true, AwaitingUser: true, Completed: true, Note: "PRIVATE-NOTE", Ownership: &control.Ownership{ThreadID: "thr_source", Generation: 4}}
	if err := control.Save(dir, ctl); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(dir, state.File{Reprompts: 7, Nudges: 9, PrimaryResetsAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	spec := control.GoalSpec{Title: "PRIVATE-GOAL", ObjectiveRefs: []string{"PRIVATE-OBJECTIVE"}, AcceptanceRefs: []string{"PRIVATE-ACCEPTANCE"}}
	g, err := control.WriteGoal(dir, "fixture", "thr_source", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sends.json", "progress.json", "incidents.json", "deliveries.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("PRIVATE-LEDGER"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := transferFiles(t, filepath.Dir(path))
	calls := 0
	runner := transferRunner(t, func(_ string, _ map[string]any) { calls++ })
	code, r, _ := runTransfer(t, path, runner)
	if code != exitOK || r.Compatibility != "compatible_metadata" || !r.ApplySupported || r.SchemaVersion != 1 || r.Outcome != "preview" || calls != 2 {
		t.Fatalf("%d %+v calls=%d", code, r, calls)
	}
	if r.Controls == nil || !r.Controls.Stop || !r.Controls.Paused || !r.Controls.Completed || !r.Controls.AwaitingUser || !r.Controls.Enabled || r.Owner != "thr_source" || r.Goal == nil || r.Goal.ID != g.ID {
		t.Fatal("lost controls/owner/goal")
	}
	if r.From.Thread != "thr_source" || r.To.Thread != "thr_destination" || r.To.Provider != "codex" {
		t.Fatal("wrong targets")
	}
	for _, id := range []string{"model_account"} {
		found := false
		for _, c := range r.Checks {
			if c.ID == id && c.Status == "unavailable" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing limitation %s", id)
		}
	}
	var out bytes.Buffer
	if code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"preview", "-c", path, "--thread", "thr_destination"}, runner); code != exitOK {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "PRIVATE-") || !strings.Contains(out.String(), "stop=true") || !strings.Contains(out.String(), "apply requires revalidation") {
		t.Fatal("invalid text output")
	}
	if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
		t.Fatal("preview changed private files")
	}
}

func TestTransferPreviewMissingStateAndOffline(t *testing.T) {
	path, dir := transferFixture(t)
	code, r, _ := runTransfer(t, path, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("offline executed CLI")
		return nil, nil
	}, "--offline")
	if code != exitFailed || r.Compatibility != "unavailable" || r.Controls == nil || r.Controls.Enabled || !r.ApplySupported {
		t.Fatalf("%d %+v", code, r)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("preview created runtime directory or locks")
	}
	// Apply requires explicit source and destination through the public dispatcher.
	var out bytes.Buffer
	if code := run(context.Background(), []string{"transfer", "apply", "-c", path}, &out, &out); code != exitUsage {
		t.Fatal("apply accepted")
	}
}

func TestTransferPreviewRejectsIncompatibleMetadata(t *testing.T) {
	for _, kind := range []string{"provider", "project", "environment", "host", "path", "wrong_id", "archived", "deleted", "missing_environment", "environment_identity", "status", "private_provider", "command_envelope", "missing_lifecycle", "relative_path", "invalid_host"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := transferFixture(t)
			runner := transferRunner(t, func(id string, v map[string]any) {
				if id != "thr_destination" {
					return
				}
				th := v["thread"].(map[string]any)
				en := v["environment"].(map[string]any)
				switch kind {
				case "provider":
					th["providerId"] = "claude-code"
				case "project":
					th["projectId"] = "proj_other"
					en["projectId"] = "proj_other"
				case "environment":
					th["environmentId"] = "env_other"
					en["id"] = "env_other"
				case "host":
					en["hostId"] = "host_other"
				case "path":
					en["path"] = filepath.Join(os.TempDir(), "other")
				case "wrong_id":
					th["id"] = "thr_other"
				case "archived":
					th["archivedAt"] = 123
				case "deleted":
					th["deletedAt"] = 123
				case "missing_environment":
					delete(v, "environment")
				case "environment_identity":
					en["id"] = "env_other"
				case "status":
					th["status"] = "PRIVATE-STATUS"
				case "private_provider":
					th["providerId"] = "PRIVATE-PROVIDER\nsecret"
				case "missing_lifecycle":
					delete(th, "archivedAt")
				case "relative_path":
					en["path"] = "relative"
				case "invalid_host":
					en["hostId"] = "invalid"
				case "command_envelope":
					v["ok"] = false
					v["error"] = map[string]string{"message": "PRIVATE-ERROR"}
				}
			})
			code, r, _ := runTransfer(t, path, runner)
			if code != exitFailed || !r.ApplySupported || r.Compatibility == "compatible_metadata" {
				t.Fatalf("%d %+v", code, r)
			}
		})
	}
}

func TestTransferPreviewUnavailableState(t *testing.T) {
	for _, kind := range []string{"control", "state", "goal", "owner", "goal_owner", "fallback", "same_thread", "labels"} {
		t.Run(kind, func(t *testing.T) {
			path, dir := transferFixture(t)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "control", "state", "goal":
				if err := os.WriteFile(filepath.Join(dir, kind+".json"), []byte("PRIVATE-CORRUPTION"), 0600); err != nil {
					t.Fatal(err)
				}
			case "owner":
				control.Save(dir, control.File{Ownership: &control.Ownership{ThreadID: "PRIVATE OWNER", Generation: 1}})
			case "goal_owner":
				_, err := control.WriteGoal(dir, "other", "", control.GoalSpec{Title: "PRIVATE-GOAL", ObjectiveRefs: []string{"x"}, AcceptanceRefs: []string{"y"}}, "", 0)
				if err != nil {
					t.Fatal(err)
				}
			case "fallback":
				state.Save(dir, state.File{ActiveTarget: "PRIVATE-TARGET"})
			case "same_thread":
				b, _ := os.ReadFile(path)
				os.WriteFile(path, bytes.ReplaceAll(b, []byte("thr_source"), []byte("thr_destination")), 0600)
			case "labels":
				b, _ := os.ReadFile(path)
				os.WriteFile(path, bytes.ReplaceAll(b, []byte("fixture-model"), []byte("PRIVATE MODEL")), 0600)
			}
			before := transferFiles(t, filepath.Dir(path))
			code, r, _ := runTransfer(t, path, transferRunner(t, nil))
			if code != exitFailed || r.Compatibility == "compatible_metadata" {
				t.Fatalf("%d %+v", code, r)
			}
			if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
				t.Fatal("failed preview wrote state")
			}
		})
	}
}

func TestTransferPreviewQueryFailureAndStaleConfig(t *testing.T) {
	for _, kind := range []string{"error", "invalid_json", "oversized", "timeout", "config_changed"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := transferFixture(t)
			runner := func(ctx context.Context, cli string, args []string, env map[string]string, workdir string) ([]byte, error) {
				switch kind {
				case "error":
					return []byte("PRIVATE-OUTPUT"), errors.New("PRIVATE-ERROR")
				case "invalid_json":
					return []byte("PRIVATE-OUTPUT"), nil
				case "oversized":
					return bytes.Repeat([]byte("x"), 64*1024+1), nil
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				case "config_changed":
					b, _ := os.ReadFile(path)
					os.WriteFile(path, append(b, '\n'), 0600)
				}
				return transferRunner(t, nil)(ctx, cli, args, env, workdir)
			}
			code, r, _ := runTransfer(t, path, runner, "--timeout", "1ms")
			if code != exitFailed || r.Compatibility != "unavailable" {
				t.Fatalf("%d %+v", code, r)
			}
			if kind == "config_changed" {
				found := false
				for _, check := range r.Checks {
					if check.ID == "config" && check.Status == "unavailable" {
						found = true
					}
				}
				if !found {
					t.Fatal("source change was not detected")
				}
			}
		})
	}
}

func TestTransferPreviewRecordBounds(t *testing.T) {
	for _, data := range []string{"null", " null\n", "[]", "{broken", strings.Repeat("x", (1<<20)+1)} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, control.FileName), []byte(data), 0600)
		if readTransferRecord(dir, control.FileName, new(control.File)) == nil {
			t.Fatal("accepted unavailable control")
		}
	}
}

func TestTransferPreviewArgumentsAndConfigPrivacy(t *testing.T) {
	path, _ := transferFixture(t)
	runner := func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("invalid invocation queried BB")
		return nil, nil
	}
	for _, args := range [][]string{
		{}, {"apply"}, {"preview", "-c", path},
		{"preview", "-c", path, "--thread", "PRIVATE INVALID"},
		{"preview", "-c", path, "--thread", "thr_destination", "--timeout", "31s"},
		{"preview", "-c", path, "--thread", "thr_destination", "--timeout", "PRIVATE-TIMEOUT"},
		{"preview", "-c", path, "--thread", "thr_destination", "--PRIVATE-FLAG"},
	} {
		var out bytes.Buffer
		code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, runner)
		if code != exitUsage || strings.Contains(out.String(), "PRIVATE-") {
			t.Fatalf("%d %s", code, out.String())
		}
	}
	if err := os.WriteFile(path, []byte("version: PRIVATE-CONFIG\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"preview", "-c", path, "--thread", "thr_destination"}, runner); code != exitUsage || strings.Contains(out.String(), "PRIVATE-") {
		t.Fatal(out.String())
	}
}

func TestTransferPreviewConfiguredFallbacks(t *testing.T) {
	path, _ := transferFixture(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, []byte("  fallbacks: ./fallbacks.yaml\n")...)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	fb := "version: 1\ncandidates:\n  - id: backup\n    adapter: bb\n    provider: codex\n    bb:\n      thread: thr_backup\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "fallbacks.yaml"), []byte(fb), 0600); err != nil {
		t.Fatal(err)
	}
	before := transferFiles(t, filepath.Dir(path))
	code, r, _ := runTransfer(t, path, transferRunner(t, nil))
	if code != exitFailed || r.Compatibility != "blocked" {
		t.Fatalf("%d %+v", code, r)
	}
	if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
		t.Fatal("preview changed fallback configuration")
	}
}

func TestTransferPreviewNonregularRecords(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, control.FileName)
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	if readTransferRecord(dir, control.FileName, new(control.File)) == nil {
		t.Fatal("directory accepted")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "original.json")
	if err := os.WriteFile(target, []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Skip(err)
	}
	if readTransferRecord(dir, control.FileName, new(control.File)) == nil {
		t.Fatal("symlink accepted")
	}
}
