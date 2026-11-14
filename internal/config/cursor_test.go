package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cursorConfig = `version: 1
loop:
  name: cursor-example
  state_dir: ./state
  adapter: cursor
  cursor:
    workdir: ./project
    model: example-model
  prompt: Continue the fictional task.
`

func TestCursorPrimaryConfiguration(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "keepgoing.yaml")
	if err := os.WriteFile(p, []byte(cursorConfig), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Loop.Agent
	if a.Cursor == nil || a.Cursor.CLI != "agent" || a.Provider != "cursor" || a.Group != "cursor" || a.Model() != "example-model" || a.Cursor.Workdir != filepath.Join(root, "project") {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(a.Describe(), "example-model") {
		t.Fatal("model absent from target description")
	}
	for _, raw := range []string{
		strings.Replace(cursorConfig, "    workdir: ./project\n", "", 1),
		cursorConfig + "  codex: {workdir: ./project}\n",
		cursorConfig + "  fallbacks: ./fallbacks.yaml\n",
		cursorConfig + "  switch_back: {enabled: true}\n",
		strings.Replace(cursorConfig, "    model: example-model", "    extra_args: [--force]", 1),
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("invalid Cursor configuration accepted")
		}
	}
}

func TestCursorFallbacksRefusedEvenWithCommandProbe(t *testing.T) {
	raw := `version: 1
candidates:
  - id: cursor-example
    adapter: cursor
    cursor: {workdir: ./project}
    probe: {kind: command, command: [fictional-probe]}
`
	for _, src := range []string{raw, raw + "    disabled: true\n"} {
		if _, err := ParseFallbacks([]byte(src)); err == nil || !strings.Contains(err.Error(), "Cursor fallback candidates are not supported") {
			t.Fatalf("unexpected result: %v", err)
		}
	}
}

func TestCursorSchemaIsPrimaryOnly(t *testing.T) {
	kg := load(t, "../../schema/keepgoing.schema.json")
	props := node(kg.props()["loop"].(map[string]any)).props()
	found := false
	for _, v := range props["adapter"].(map[string]any)["enum"].([]any) {
		if v == "cursor" {
			found = true
		}
	}
	if !found {
		t.Fatal("primary schema does not offer Cursor")
	}
	fb := load(t, "../../schema/fallbacks.schema.json")
	props = node(fb.props()["candidates"].(map[string]any)["items"].(map[string]any)).props()
	if allowed, ok := props["cursor"].(bool); !ok || allowed {
		t.Fatal("fallback schema must reject Cursor settings")
	}
	for _, v := range props["adapter"].(map[string]any)["enum"].([]any) {
		if v == "cursor" {
			t.Fatal("fallback schema offers unsupported adapter")
		}
	}
}

func TestCursorFreshStartVersionsAreValidated(t *testing.T) {
	many := strings.Repeat("\"2030.01.02-abcdef0\", ", 17)
	for _, tc := range []struct {
		name, list string
		ok         bool
	}{
		{"absent", "", true},
		{"one", "    fresh_start_versions: [\"2030.01.02-abcdef0\"]\n", true},
		{"two", "    fresh_start_versions: [\"2030.01.02-abcdef0\", \"2031.12.31-0123456789abcdef\"]\n", true},
		{"prose", "    fresh_start_versions: [\"latest\"]\n", false},
		{"prefix_only", "    fresh_start_versions: [\"2030.01.02\"]\n", false},
		{"trailing_text", "    fresh_start_versions: [\"2030.01.02-abcdef0 beta\"]\n", false},
		{"empty_entry", "    fresh_start_versions: [\"\"]\n", false},
		{"too_many", "    fresh_start_versions: [" + strings.TrimSuffix(many, ", ") + "]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "keepgoing.yaml")
			raw := strings.Replace(cursorConfig, "    model: example-model\n", "    model: example-model\n"+tc.list, 1)
			if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			if (err == nil) != tc.ok {
				t.Fatalf("load: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "fresh_start_versions") {
				t.Fatalf("error does not name the field: %v", err)
			}
			if tc.name == "two" && len(cfg.Loop.Agent.Cursor.FreshStartVersions) != 2 {
				t.Fatalf("%+v", cfg.Loop.Agent.Cursor)
			}
		})
	}
}
