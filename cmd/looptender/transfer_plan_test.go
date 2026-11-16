package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// planFixture is a single-primary loop whose progress repository has a linked
// worktree, and a stand-in BB where the destination lives in another
// fictional project and environment at dest, on the same host.
func planFixture(t *testing.T, repositories bool, extra string) (path string, linked, outside string) {
	t.Helper()
	root, repo, linked := workspaceRepo(t)
	outside = filepath.Join(root, "unrelated-checkout")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	path, _ = transferFixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if repositories {
		raw = append(raw, []byte("  progress:\n    repositories: ["+strconvQuote(repo)+"]\n")...)
	}
	raw = append(raw, []byte(extra)...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, linked, outside
}

func planRunner(t *testing.T, source, dest string, mutate func(map[string]any, map[string]any)) doctorRun {
	return transferRunner(t, func(id string, v map[string]any) {
		th, en := v["thread"].(map[string]any), v["environment"].(map[string]any)
		en["path"] = source
		if id == "thr_destination" {
			th["projectId"], th["environmentId"] = "proj_other", "env_other"
			en["id"], en["projectId"], en["path"] = "env_other", "proj_other", dest
			if mutate != nil {
				mutate(th, en)
			}
		}
	})
}

func planCheck(r transferPreview, id string) string {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.Status
		}
	}
	return ""
}

func TestTransferPlanAcrossEnvironment(t *testing.T) {
	path, linked, outside := planFixture(t, true, "")
	for _, tc := range []struct {
		name      string
		dest      string
		flags     []string
		mutate    func(map[string]any, map[string]any)
		code      int
		compat    string
		env, work string
	}{
		{"refused without the flag", linked, nil, nil, exitFailed, "blocked", "error", ""},
		{"inside a worktree", linked, []string{"--across-environment"}, nil, exitOK, "compatible_metadata", "warning", "ok"},
		{"outside every repository", outside, []string{"--across-environment"}, nil, exitFailed, "blocked", "warning", "error"},
		{"outside, accepted", outside, []string{"--across-environment", "--accept-workspace-outside"}, nil, exitOK, "compatible_metadata", "warning", "warning"},
		{"another host", linked, []string{"--across-environment"}, func(_ map[string]any, en map[string]any) { en["hostId"] = "host_other" }, exitFailed, "blocked", "error", "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, r, _ := runTransfer(t, path, planRunner(t, outside, tc.dest, tc.mutate), tc.flags...)
			if code != tc.code || r.Compatibility != tc.compat || planCheck(r, "environment") != tc.env || planCheck(r, "workspace") != tc.work {
				t.Fatalf("%d %s env=%s workspace=%s", code, r.Compatibility, planCheck(r, "environment"), planCheck(r, "workspace"))
			}
			if len(tc.flags) > 0 && (r.Plan == nil || !r.Plan.AcrossEnvironment || r.ApplySupported || r.Plan.LabelVerification != "unavailable") {
				t.Fatalf("plan %+v apply=%t", r.Plan, r.ApplySupported)
			}
		})
	}
	// Without progress repositories the destination cannot be confirmed.
	bare, linked2, outside2 := planFixture(t, false, "")
	code, r, _ := runTransfer(t, bare, planRunner(t, outside2, linked2, nil), "--across-environment")
	if code != exitFailed || r.Compatibility != "unavailable" || planCheck(r, "workspace") != "unavailable" {
		t.Fatalf("%d %s", code, r.Compatibility)
	}
	if code, r, _ = runTransfer(t, bare, planRunner(t, outside2, linked2, nil), "--across-environment", "--accept-workspace-outside"); code != exitOK || r.Compatibility != "compatible_metadata" {
		t.Fatalf("accepted: %d %s", code, r.Compatibility)
	}
}

func TestTransferPlanProviderChange(t *testing.T) {
	path, linked, outside := planFixture(t, true, "")
	claude := func(th map[string]any, _ map[string]any) { th["providerId"] = "claude-code" }
	full := []string{"--across-environment", "--provider", "claude-code", "--quota-group", "example-group", "--model", "example-model", "--no-reasoning"}

	code, r, _ := runTransfer(t, path, planRunner(t, outside, linked, claude), "--across-environment")
	if code != exitFailed || planCheck(r, "provider") != "error" {
		t.Fatal("provider change inferred without --provider")
	}
	code, r, _ = runTransfer(t, path, planRunner(t, outside, linked, claude), "--across-environment", "--provider", "claude-code")
	if code != exitFailed || planCheck(r, "labels_required") != "error" || planCheck(r, "provider") != "ok" {
		t.Fatal("labels carried over to a new provider")
	}
	before := transferFiles(t, filepath.Dir(path))
	code, r, _ = runTransfer(t, path, planRunner(t, outside, linked, claude), full...)
	if code != exitOK || r.Compatibility != "compatible_metadata" || r.ApplySupported || planCheck(r, "provider_settings") != "ok" {
		t.Fatalf("%d %s %+v", code, r.Compatibility, r.Checks)
	}
	want := &transferPlan{AcrossEnvironment: true, LabelVerification: "unavailable",
		Provider:   &transferChange{strPtr("codex"), "claude-code"},
		QuotaGroup: &transferChange{strPtr("fixture-account-label"), "example-group"},
		Model:      &transferChange{strPtr("fixture-model"), "example-model"},
	}
	if !reflect.DeepEqual(r.Plan, want) {
		t.Fatalf("plan %+v", r.Plan)
	}
	if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
		t.Fatal("preview changed files")
	}
	// The destination must match the label given, not merely differ.
	code, r, _ = runTransfer(t, path, planRunner(t, outside, linked, nil), full...)
	if code != exitFailed || planCheck(r, "provider") != "error" {
		t.Fatal("destination provider not checked against --provider")
	}
	// A probe or timing section names the old provider.
	for _, extra := range []string{"  probe:\n    kind: codex\n"} {
		p, l, o := planFixture(t, true, extra)
		code, r, _ = runTransfer(t, p, planRunner(t, o, l, claude), full...)
		if code != exitFailed || planCheck(r, "provider_settings") != "error" {
			t.Fatalf("provider-bound settings not refused: %q", extra)
		}
	}
	// Relabeling without a provider change needs no other flags.
	if code, r, _ = runTransfer(t, path, planRunner(t, outside, linked, nil), "--across-environment", "--no-model"); code != exitOK || r.Plan.Model == nil || r.Plan.Model.From == nil || *r.Plan.Model.From != "fixture-model" || r.Plan.Model.To != "" || r.Plan.Provider != nil {
		t.Fatalf("%d %+v", code, r.Plan)
	}
}

func TestTransferPlanArguments(t *testing.T) {
	path, _ := transferFixture(t)
	for _, extra := range [][]string{
		{"--model", "example", "--no-model"},
		{"--reasoning", "high", "--no-reasoning"},
		{"--accept-workspace-outside"},
		{"--provider", "-x"},
		{"--quota-group", "PRIVATE value"},
	} {
		var out bytes.Buffer
		args := append([]string{"preview", "-c", path, "--thread", "thr_destination"}, extra...)
		if code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, nil); code != exitUsage {
			t.Fatalf("%v: %d", extra, code)
		}
		if bytes.Contains(out.Bytes(), []byte("PRIVATE")) {
			t.Fatal("argument echoed")
		}
	}
	// A plain same-environment preview has no plan and keeps apply support.
	code, r, out := runTransfer(t, path, transferRunner(t, nil))
	if code != exitOK || r.Plan != nil || !r.ApplySupported || bytes.Contains([]byte(out), []byte(`"plan"`)) {
		t.Fatalf("%d %+v", code, r.Plan)
	}
}

// A configured label that fails the shape check is never printed in a plan:
// its from value is left out of the JSON and withheld in the text.
func TestTransferPlanWithholdsMalformedLabels(t *testing.T) {
	path, linked, outside := planFixture(t, true, "")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("model: fixture-model\n"), []byte("model: \"PRIVATE-MODEL with space\"\n    reasoning: \"PRIVATE-REASONING!\"\n"), 1)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--across-environment", "--model", "example-model", "--reasoning", "high"}
	_, r, _ := runTransfer(t, path, planRunner(t, outside, linked, nil), flags...)
	if planCheck(r, "labels") != "error" || r.Plan == nil || r.Plan.Model == nil || r.Plan.Reasoning == nil {
		t.Fatalf("labels=%s plan %+v", planCheck(r, "labels"), r.Plan)
	}
	if r.Plan.Model.From != nil || r.Plan.Model.To != "example-model" || r.Plan.Reasoning.From != nil || r.Plan.Reasoning.To != "high" {
		t.Fatalf("model %+v reasoning %+v", r.Plan.Model, r.Plan.Reasoning)
	}
	var out bytes.Buffer
	args := append([]string{"preview", "-c", path, "--thread", "thr_destination"}, flags...)
	transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, planRunner(t, outside, linked, nil))
	if bytes.Contains(out.Bytes(), []byte("PRIVATE-")) || !bytes.Contains(out.Bytes(), []byte(`model: (withheld: unsupported shape) -> "example-model"`)) {
		t.Fatalf("text output:\n%s", out.String())
	}
}

func strPtr(s string) *string { return &s }
