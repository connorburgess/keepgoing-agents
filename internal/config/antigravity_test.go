package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAntigravityConfigDefaultsAndValidation(t *testing.T) {
	root := t.TempDir()
	yaml := `version: 1
loop:
  name: example-antigravity
  state_dir: state
  adapter: antigravity
  antigravity:
    workdir: project
    sandbox: true
    agent: example-agent
    add_dir:
      - extra
  prompt: Complete the fictional example task.
`
	cfgPath := filepath.Join(root, "loop.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Loop.Agent
	if a.Antigravity == nil || a.Antigravity.CLI != "agy" || a.Provider != "antigravity" || a.Group != "antigravity" || a.Antigravity.Workdir != filepath.Join(root, "project") || !a.Antigravity.Sandbox || a.Antigravity.Agent != "example-agent" || len(a.Antigravity.AddDirs) != 1 || a.Antigravity.AddDirs[0] != filepath.Join(root, "extra") {
		t.Fatalf("unexpected antigravity config: %+v %+v", a, a.Antigravity)
	}
}

func TestAntigravityRejectsFallbacksSwitchBackAndTiming(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  string
	}{
		{"fallbacks", "  fallbacks: fallbacks.yaml\n", "standalone primary loops only"},
		{"switch_back", "  switch_back:\n    enabled: true\n", "standalone primary loops only"},
		{"timing", "  timing:\n    enabled: true\n", "loop.timing is not supported for Antigravity loops"},
		{"missing workdir", "", "antigravity.workdir is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			workdir := "    workdir: project\n"
			if tc.name == "missing workdir" {
				workdir = ""
			}
			body := "version: 1\nloop:\n  name: example\n  state_dir: state\n  adapter: antigravity\n  antigravity:\n" + workdir + "    cli: agy\n" + tc.extra + "  prompt: Fictional task.\n"
			p := filepath.Join(root, "loop.yaml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestAntigravitySchemaIncludesPrimaryAndForbidsFallback(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "looptender.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loopSchema map[string]any
	if err := json.Unmarshal(raw, &loopSchema); err != nil {
		t.Fatal(err)
	}
	defs := loopSchema["$defs"].(map[string]any)
	if _, ok := defs["antigravity"]; !ok {
		t.Fatal("looptender.schema.json missing $defs.antigravity")
	}

	fbRaw, err := os.ReadFile(filepath.Join("..", "..", "schema", "fallbacks.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fbSchema map[string]any
	if err := json.Unmarshal(fbRaw, &fbSchema); err != nil {
		t.Fatal(err)
	}
	items := fbSchema["properties"].(map[string]any)["candidates"].(map[string]any)["items"].(map[string]any)
	props := items["properties"].(map[string]any)
	if allowed, ok := props["antigravity"].(bool); !ok || allowed {
		t.Fatalf("fallbacks.schema.json antigravity=%v, want false", props["antigravity"])
	}
}
