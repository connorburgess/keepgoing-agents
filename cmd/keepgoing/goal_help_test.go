package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoalHelpWithoutConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Even a broken default configuration must not prevent help or be repaired.
	const invalidConfig = "PRIVATE-CONFIG: ["
	if err := os.WriteFile("keepgoing.yaml", []byte(invalidConfig), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"create", "revise", "show"} {
		for _, help := range []string{"-h", "--help"} {
			for _, missing := range []bool{false, true} {
				args := []string{"goal", cmd}
				if missing {
					args = append(args, "-c", filepath.Join(dir, "missing.yaml"))
				}
				args = append(args, help)
				var out, errs bytes.Buffer
				code := run(context.Background(), args, &out, &errs)
				if code != exitOK || !strings.Contains(errs.String(), "Usage of goal "+cmd) || !strings.Contains(errs.String(), "-revision") || out.Len() != 0 {
					t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out.String(), errs.String())
				}
				if strings.Contains(errs.String(), "PRIVATE-CONFIG") {
					t.Fatal("help exposed configuration")
				}
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keepgoing.yaml" {
		t.Fatalf("help created files: %v, %v", entries, err)
	}
	b, err := os.ReadFile("keepgoing.yaml")
	if err != nil || string(b) != invalidConfig {
		t.Fatalf("help changed configuration: %q, %v", b, err)
	}
}

func TestGoalHelpDoesNotHideUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"goal"}, {"goal", "unknown", "--help"},
		{"goal", "show", "--unknown"}, {"goal", "show", "-c"},
		{"goal", "revise", "--revision", "invalid", "--help"},
		{"goal", "create"}, {"goal", "revise"},
	} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), args, &out, &errs); code != exitUsage {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out.String(), errs.String())
		}
	}
}
