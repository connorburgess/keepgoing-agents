package config

import (
	"strings"
	"testing"
	"time"
)

const validFallbacks = `
version: 1
redundancy:
  min_healthy: 2
  span: provider
candidates:
  - id: same-thread-fable
    adapter: bb
    provider: claude-code
    group: claude-example-account
    bb: {thread: thr_example123, model: claude-fable-5-1, reasoning: medium}
    probe: {model: claude-haiku-5-5}
  - id: codex-thread
    adapter: bb
    provider: codex
    bb: {thread: thr_example456}
  - id: headless-claude
    adapter: claude
    claude: {workdir: /fictional/example-app, model: sonnet}
  - id: custom
    adapter: process
    provider: example-cli
    process: {status_command: [st], resume_command: [re]}
    probe: {kind: command, command: [./probe.sh]}
`

func TestParseFallbacksDefaults(t *testing.T) {
	fb, err := ParseFallbacks([]byte(validFallbacks))
	if err != nil {
		t.Fatal(err)
	}
	if fb.Probe.Every.Duration != 4*time.Hour || fb.Probe.TTL.Duration != 6*time.Hour || fb.Probe.DailyCap != 6 {
		t.Errorf("probe defaults: %+v", fb.Probe)
	}
	c, _ := fb.Find("codex-thread")
	if c.Probe.Kind != "codex" || c.Group != "codex" {
		t.Errorf("codex defaults: %+v", c)
	}
	h, _ := fb.Find("headless-claude")
	if h.Provider != "claude-code" || h.Probe.Kind != "claude" || h.ProbeWorkdir() != "/fictional/example-app" {
		t.Errorf("headless defaults: %+v", h)
	}
	f, _ := fb.Find("same-thread-fable")
	if f.ProbeModel(false) != "claude-haiku-5-5" || f.ProbeModel(true) != "claude-fable-5-1" {
		t.Errorf("probe models: %q %q", f.ProbeModel(false), f.ProbeModel(true))
	}
}

func TestParseFallbacksRejects(t *testing.T) {
	cases := map[string]string{
		"duplicate id":      validFallbacks + "  - id: custom\n    adapter: bb\n    provider: x\n    bb: {thread: t}\n    probe: {kind: command, command: [p]}\n",
		"reserved id":       strings.Replace(validFallbacks, "id: custom", "id: primary", 1),
		"no provider":       strings.Replace(validFallbacks, "    provider: example-cli\n", "", 1),
		"no probe kind":     strings.Replace(validFallbacks, "    probe: {kind: command, command: [./probe.sh]}\n", "", 1),
		"command w/o argv":  strings.Replace(validFallbacks, "{kind: command, command: [./probe.sh]}", "{kind: command}", 1),
		"bad span":          strings.Replace(validFallbacks, "span: provider", "span: vibes", 1),
		"ttl below every":   validFallbacks + "probe: {every: 8h, ttl: 1h}\n",
		"no candidates":     "version: 1\ncandidates: []\n",
		"wrong adapter blk": strings.Replace(validFallbacks, "adapter: claude\n", "adapter: codex\n", 1),
	}
	for name, src := range cases {
		if _, err := ParseFallbacks([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
