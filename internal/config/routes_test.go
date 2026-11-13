package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const standardList = `version: 1
redundancy: {min_healthy: 1, span: group}
routes:
  - id: large-same
    provider: claude-code
    model: example-large
    effort: high
    group: claude-large
    enter: same_conversation
    probe: {model: example-small}
  - id: codex-new
    provider: codex
    model: example-codex
    group: codex-main
    enter: new_conversation
    permission_mode: auto
  - id: claude-headless
    provider: claude-code
    model: example-large
    group: claude-second
    enter: headless
    account_dir: /fictional/accounts/second
  - id: codex-headless
    provider: codex
    model: example-codex
    group: codex-main
    enter: headless
  - id: off
    provider: codex
    model: example-codex
    group: codex-other
    enter: same_conversation
    disabled: true
`

func writeRoutes(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "user", RoutesFileName)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(RoutesFileEnv, p)
	return p
}

func loopFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keepgoing.yaml")
	if err := os.WriteFile(p, []byte("version: 1\nloop:\n  name: example-loop\n  state_dir: ./state\n  prompt: Continue the fictional task.\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const bbLoop = "  adapter: bb\n  provider: claude-code\n  group: claude-main\n  bb: {thread: thr_example, model: example-medium}\n  routes: standard\n"

func ids(fb *Fallbacks) string {
	if fb == nil {
		return ""
	}
	var out []string
	for _, c := range fb.Candidates {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

func skips(s []RouteSkip) map[string]string {
	out := map[string]string{}
	for _, k := range s {
		out[k.ID] = k.Reason
	}
	return out
}

func TestStandardRoutesResolvePerLoop(t *testing.T) {
	for _, tc := range []struct {
		name, loop, want string
		skipped          map[string]string // id -> a word the reason must contain
		check            func(*testing.T, *Fallbacks)
	}{
		{"bb_thread", bbLoop, "large-same",
			map[string]string{"codex-new": "does not allow", "claude-headless": "headless", "codex-headless": "headless", "off": "switched off"},
			func(t *testing.T, fb *Fallbacks) {
				c := fb.Candidates[0]
				if c.Adapter != "bb" || c.BB.Thread != "thr_example" || c.BB.NewThread || c.BB.Model != "example-large" || c.BB.Reasoning != "high" || c.Group != "claude-large" || c.Probe.Kind != "claude" || c.Probe.Model != "example-small" {
					t.Fatalf("%+v %+v", c, c.BB)
				}
			}},
		{"bb_thread_allows_new", bbLoop + "  routes_allow: [new_conversation]\n", "large-same,codex-new", nil,
			func(t *testing.T, fb *Fallbacks) {
				c := fb.Candidates[1]
				if c.BB.Thread != "" || !c.BB.NewThread || c.BB.PermissionMode != "auto" || c.Provider != "codex" || c.Probe.Kind != "codex" {
					t.Fatalf("%+v %+v", c, c.BB)
				}
			}},
		{"bb_same_model", strings.Replace(bbLoop, "example-medium", "Example-Large", 1), "",
			map[string]string{"large-same": "already uses"}, nil},
		{"bb_same_group", strings.Replace(bbLoop, "claude-main", "claude-large", 1), "",
			map[string]string{"large-same": "same usage limit"}, nil},
		{"bb_other_provider", "  adapter: bb\n  provider: codex\n  group: codex-first\n  bb: {thread: thr_example}\n  routes: standard\n", "",
			map[string]string{"large-same": "one provider"}, nil},
		{"claude_headless", "  adapter: claude\n  provider: claude-code\n  group: claude-main\n  claude: {workdir: ./project, model: example-medium, permission_mode: acceptEdits}\n  routes: standard\n", "claude-headless,codex-headless",
			map[string]string{"large-same": "conversation route", "codex-new": "conversation route"},
			func(t *testing.T, fb *Fallbacks) {
				a, b := fb.Candidates[0], fb.Candidates[1]
				if a.Adapter != "claude" || !strings.HasSuffix(a.Claude.Workdir, "project") || a.Claude.Model != "example-large" || a.Claude.PermissionMode != "acceptEdits" || a.Claude.Env["CLAUDE_CONFIG_DIR"] != "/fictional/accounts/second" {
					t.Fatalf("%+v %+v", a, a.Claude)
				}
				if b.Adapter != "codex" || b.Codex.Workdir != a.Claude.Workdir || b.Codex.Model != "example-codex" || len(b.Codex.Env) != 0 {
					t.Fatalf("%+v %+v", b, b.Codex)
				}
			}},
		{"codex_headless_same_agent", "  adapter: codex\n  provider: codex\n  group: codex-first\n  codex: {workdir: ./project, model: example-codex}\n  routes: standard\n", "claude-headless",
			map[string]string{"codex-headless": "already uses"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeRoutes(t, standardList)
			cfg, err := Load(loopFile(t, tc.loop))
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(cfg.FallbackSet); got != tc.want {
				t.Fatalf("resolved %q, want %q (skipped %+v)", got, tc.want, cfg.RouteList.Skipped)
			}
			if cfg.RouteList.Source != RoutesStandard || cfg.RouteList.Unreadable {
				t.Fatalf("%+v", cfg.RouteList)
			}
			got := skips(cfg.RouteList.Skipped)
			for id, word := range tc.skipped {
				if !strings.Contains(got[id], word) {
					t.Fatalf("skip reason for %s = %q, want it to mention %q", id, got[id], word)
				}
			}
			if cfg.FallbackSet != nil {
				if cfg.FallbackSet.Redundancy.Span != "group" || cfg.FallbackSet.Redundancy.MinHealthy != 1 || cfg.FallbackSet.Probe.Every.Duration != DefaultProbeEvery {
					t.Fatalf("policy %+v %+v", cfg.FallbackSet.Redundancy, cfg.FallbackSet.Probe)
				}
				if err := cfg.FallbackSet.Validate(); err != nil {
					t.Fatalf("resolved list is not a valid fallbacks list: %v", err)
				}
			}
			if tc.check != nil {
				tc.check(t, cfg.FallbackSet)
			}
		})
	}
}

// A following loop must keep loading whatever state the standard list is in:
// a failed load makes a check exit non-zero, and repeated failures make an
// external scheduler disable itself.
func TestStandardRoutesUnusableListNeverFailsTheLoad(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"missing", ""},
		{"not_yaml", "version: [1\n"},
		{"unknown_version", strings.Replace(standardList, "version: 1", "version: 2", 1)},
		{"unknown_field", standardList + "  - {id: extra, provider: codex, model: m, group: g, enter: headless, thread: thr_other}\n"},
		{"bad_route", standardList + "  - {id: extra, provider: other-tool, model: m, group: g, enter: headless}\n"},
		{"repeated_id", standardList + "  - {id: off, provider: codex, model: m, group: g, enter: headless}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRoutes(t, tc.content)
			cfg, err := Load(loopFile(t, bbLoop))
			if err != nil {
				t.Fatalf("load failed: %v", err)
			}
			if cfg.FallbackSet != nil || !cfg.RouteList.Unreadable || cfg.RouteList.Source != RoutesStandard {
				t.Fatalf("%+v %+v", cfg.FallbackSet, cfg.RouteList)
			}
			if err := cfg.CheckSources(); err != nil {
				t.Fatalf("unchanged sources reported stale: %v", err)
			}
			// When the list becomes usable, the loaded configuration is stale, so
			// the next check reloads it and picks the routes up.
			if err := os.WriteFile(path, []byte(standardList), 0o600); err != nil {
				t.Fatal(err)
			}
			if cfg.CheckSources() == nil {
				t.Fatal("a repaired list did not make the loaded configuration stale")
			}
			fresh, err := Load(cfg.Path())
			if err != nil || ids(fresh.FallbackSet) != "large-same" || fresh.RouteList.Unreadable {
				t.Fatalf("%v %+v", err, fresh.RouteList)
			}
			// And a later edit or removal is noticed the same way.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if fresh.CheckSources() == nil {
				t.Fatal("a removed list did not make the loaded configuration stale")
			}
		})
	}

	t.Run("relative_override", func(t *testing.T) {
		t.Setenv(RoutesFileEnv, "routes.yaml")
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || !cfg.RouteList.Unreadable || cfg.FallbackSet != nil {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})

	t.Run("no_route_applies", func(t *testing.T) {
		writeRoutes(t, standardList)
		cfg, err := Load(loopFile(t, strings.Replace(bbLoop, "claude-main", "claude-large", 1)))
		if err != nil || cfg.FallbackSet != nil || cfg.RouteList.Unreadable || len(cfg.RouteList.Skipped) != 5 {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})

	t.Run("short_tick_timeout_warns", func(t *testing.T) {
		writeRoutes(t, strings.Replace(standardList, "redundancy:", "probe: {timeout: 10m, every: 1h, ttl: 2h}\nredundancy:", 1))
		cfg, err := Load(loopFile(t, bbLoop+"  tick_timeout: 3m\n"))
		if err != nil || ids(cfg.FallbackSet) != "large-same" || len(cfg.RouteList.Warnings) != 1 {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
}

func TestStandardRoutesLoopKeysAreValidated(t *testing.T) {
	writeRoutes(t, standardList)
	for _, tc := range []struct {
		name, loop, want string
	}{
		{"both", bbLoop + "  fallbacks: fallbacks.yaml\n", "not both"},
		{"unknown_value", strings.Replace(bbLoop, "routes: standard", "routes: custom", 1), "must be"},
		{"allow_unknown", bbLoop + "  routes_allow: [headless]\n", "only value"},
		{"allow_without_standard", strings.Replace(bbLoop, "  routes: standard\n", "  routes_allow: [new_conversation]\n", 1), "applies only"},
		{"process_loop", "  adapter: process\n  process: {status_command: [status], resume_command: [resume]}\n  routes: standard\n", "its own list"},
		{"cursor_loop", "  adapter: cursor\n  cursor: {workdir: ./project}\n  routes: standard\n", "its own list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(loopFile(t, tc.loop))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to mention %q", err, tc.want)
			}
		})
	}
	// Switch-back needs a route list, and following the standard list is one.
	cfg, err := Load(loopFile(t, bbLoop+"  switch_back: {enabled: true}\n"))
	if err != nil || !cfg.Loop.HasRouteList() {
		t.Fatalf("%v", err)
	}
	// A loop that does not follow the list never reads it.
	t.Setenv(RoutesFileEnv, filepath.Join(t.TempDir(), "absent", RoutesFileName))
	cfg, err = Load(loopFile(t, strings.Replace(bbLoop, "  routes: standard\n", "", 1)))
	if err != nil || cfg.RouteList.Source != "" || cfg.RouteList.Unreadable || cfg.CheckSources() != nil {
		t.Fatalf("%v %+v", err, cfg.RouteList)
	}
}

func TestStandardRoutesFileIsValidated(t *testing.T) {
	base := "version: 1\nroutes:\n"
	route := "  - {id: one, provider: codex, model: example, group: codex-a, enter: headless"
	for _, tc := range []struct {
		name, content string
		ok            bool
	}{
		{"empty_list", "version: 1\nroutes: []\n", true},
		{"minimal", base + route + "}\n", true},
		{"account_dir", base + route + ", account_dir: /fictional/account}\n", true},
		{"relative_account_dir", base + route + ", account_dir: account}\n", false},
		{"account_dir_on_conversation", base + strings.Replace(route, "headless", "same_conversation", 1) + ", account_dir: /fictional/account}\n", false},
		{"permission_on_headless", base + route + ", permission_mode: auto}\n", false},
		{"unknown_permission", base + strings.Replace(route, "headless", "new_conversation", 1) + ", permission_mode: everything}\n", false},
		{"no_model", base + strings.Replace(route, "model: example, ", "", 1) + "}\n", false},
		{"no_group", base + strings.Replace(route, "group: codex-a, ", "", 1) + "}\n", false},
		{"unknown_enter", base + strings.Replace(route, "headless", "attach", 1) + "}\n", false},
		{"reserved_id", base + strings.Replace(route, "id: one", "id: primary", 1) + "}\n", false},
		{"names_a_thread", base + route + ", thread: thr_example}\n", false},
		{"relative_probe_account", base + route + ", probe: {account_dir: login}}\n", false},
		{"ttl_below_every", "version: 1\nprobe: {every: 6h, ttl: 1h}\nroutes: []\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRoutes([]byte(tc.content))
			if (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
	many := base
	for i := 0; i <= MaxStandardRoutes; i++ {
		many += strings.Replace(route, "id: one", "id: r"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)), 1) + "}\n"
	}
	if _, err := ParseRoutes([]byte(many)); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized list: %v", err)
	}
}
