package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// A headless route on the loop's own tool keeps the loop's execution
// settings; on the other tool only what means the same thing carries over.
func TestHeadlessRoutesCarryTheLoopsSettings(t *testing.T) {
	list := `version: 1
routes:
  - {id: claude-other, provider: claude-code, model: example-large, group: claude-second, enter: headless}
  - {id: codex-plain, provider: codex, model: example-codex, group: codex-main, enter: headless}
  - {id: codex-effort, provider: codex, model: example-codex, effort: high, group: codex-other, enter: headless}
`
	claudeLoop := "  adapter: claude\n  provider: claude-code\n  group: claude-main\n  claude: {workdir: ./project, model: example-medium, permission_mode: acceptEdits, max_budget_usd: 5, extra_args: [--fictional-claude-flag], env: {EXAMPLE_SETTING: one}}\n  routes: standard\n"
	codexLoop := "  adapter: codex\n  provider: codex\n  group: codex-first\n  codex: {workdir: ./project, model: example-mini, sandbox: workspace-write, extra_args: [--fictional-codex-flag], env: {EXAMPLE_SETTING: two}}\n  routes: standard\n"

	t.Run("claude_loop_with_budget", func(t *testing.T) {
		writeRoutes(t, list)
		cfg, err := Load(loopFile(t, claudeLoop))
		if err != nil {
			t.Fatal(err)
		}
		if ids(cfg.FallbackSet) != "claude-other" {
			t.Fatalf("resolved %q (skipped %+v)", ids(cfg.FallbackSet), cfg.RouteList.Skipped)
		}
		c := cfg.FallbackSet.Candidates[0].Claude
		if c.MaxBudgetUSD != 5 || c.PermissionMode != "acceptEdits" || len(c.ExtraArgs) != 1 || c.ExtraArgs[0] != "--fictional-claude-flag" || c.Env["EXAMPLE_SETTING"] != "one" {
			t.Fatalf("same-tool settings not carried: %+v", c)
		}
		reasons := skips(cfg.RouteList.Skipped)
		// The loop's budget cannot be kept on Codex, so that route is left out
		// rather than run without it.
		if !strings.Contains(reasons["codex-plain"], "max_budget_usd") || !strings.Contains(reasons["codex-effort"], "effort") {
			t.Fatalf("skips: %v", reasons)
		}
	})

	t.Run("claude_loop_without_budget_on_codex", func(t *testing.T) {
		writeRoutes(t, list)
		cfg, err := Load(loopFile(t, strings.Replace(claudeLoop, ", max_budget_usd: 5", "", 1)))
		if err != nil {
			t.Fatal(err)
		}
		c, ok := cfg.FallbackSet.Find("codex-plain")
		if !ok {
			t.Fatalf("codex route left out: %+v", cfg.RouteList.Skipped)
		}
		if !strings.HasSuffix(c.Codex.Workdir, "project") || c.Codex.Env["EXAMPLE_SETTING"] != "one" || len(c.Codex.ExtraArgs) != 0 || c.Codex.Sandbox != "" {
			t.Fatalf("cross-tool settings: %+v", c.Codex)
		}
	})

	t.Run("codex_loop", func(t *testing.T) {
		writeRoutes(t, list)
		cfg, err := Load(loopFile(t, codexLoop))
		if err != nil {
			t.Fatal(err)
		}
		cl, ok := cfg.FallbackSet.Find("claude-other")
		if !ok || len(cl.Claude.ExtraArgs) != 0 || cl.Claude.Env["EXAMPLE_SETTING"] != "two" || cl.Claude.MaxBudgetUSD != 0 {
			t.Fatalf("cross-tool claude route: %+v %t", cl.Claude, ok)
		}
		cx, ok := cfg.FallbackSet.Find("codex-plain")
		if !ok || cx.Codex.Sandbox != "workspace-write" || len(cx.Codex.ExtraArgs) != 1 || cx.Codex.Env["EXAMPLE_SETTING"] != "two" {
			t.Fatalf("same-tool codex route: %+v %t", cx.Codex, ok)
		}
		if !strings.Contains(skips(cfg.RouteList.Skipped)["codex-effort"], "effort") {
			t.Fatalf("effort on a codex route silently dropped: %+v", cfg.RouteList.Skipped)
		}
	})

	t.Run("account_dir_overrides_the_loops_account", func(t *testing.T) {
		writeRoutes(t, strings.Replace(list, "enter: headless}\n  - {id: codex-plain", "enter: headless, account_dir: /fictional/second}\n  - {id: codex-plain", 1))
		cfg, err := Load(loopFile(t, strings.Replace(claudeLoop, "env: {EXAMPLE_SETTING: one}", "env: {EXAMPLE_SETTING: one, CLAUDE_CONFIG_DIR: /fictional/first}", 1)))
		if err != nil {
			t.Fatal(err)
		}
		c, _ := cfg.FallbackSet.Find("claude-other")
		if c.Claude.Env["CLAUDE_CONFIG_DIR"] != "/fictional/second" {
			t.Fatalf("%+v", c.Claude.Env)
		}
		// The template read back from a resolved route holds the route only,
		// not the loop's environment, and resolves to the same route again.
		rt, ok := RouteOf(cfg.Loop, c)
		if !ok || rt.AccountDir != "/fictional/second" || rt.Model != "example-large" || rt.Enter != EnterHeadless {
			t.Fatalf("%+v %t", rt, ok)
		}
		raw, _ := yaml.Marshal(rt)
		if strings.Contains(string(raw), "EXAMPLE_SETTING") || strings.Contains(string(raw), "fictional-claude-flag") || strings.Contains(string(raw), "/fictional/first") {
			t.Fatalf("template carries the loop's settings: %s", raw)
		}
		again, ok := ResolveSaved(cfg.Loop, rt, cfg.FallbackSet)
		a, _ := yaml.Marshal(again)
		b, _ := yaml.Marshal(c)
		if !ok || string(a) != string(b) {
			t.Fatalf("resolved again differently:\n%s\n%s", a, b)
		}
		// Switched off and new-conversation templates resolve, since the loop
		// is already on them; one that no longer fits the loop does not.
		rt.Disabled = true
		if _, ok := ResolveSaved(cfg.Loop, rt, cfg.FallbackSet); !ok {
			t.Fatal("a switched-off saved route was refused")
		}
		rt.Enter = EnterSameConversation
		if _, ok := ResolveSaved(cfg.Loop, rt, cfg.FallbackSet); ok {
			t.Fatal("a conversation route resolved for a headless loop")
		}
	})
}

func TestStandardRoutesFileChecks(t *testing.T) {
	writeFile := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(standardList), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("private_file", func(t *testing.T) {
		p := writeRoutes(t, standardList)
		if _, err := LoadRoutes(p); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("readable_by_others", func(t *testing.T) {
		p := writeRoutes(t, "")
		writeFile(t, p, 0o644)
		if _, err := LoadRoutes(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("%v", err)
		}
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || !cfg.RouteList.Unreadable {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
	t.Run("symlink_owned_by_this_user", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "dotfiles-routes.yaml")
		writeFile(t, real, 0o600)
		link := filepath.Join(dir, RoutesFileName)
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv(RoutesFileEnv, link)
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || cfg.RouteList.Unreadable || ids(cfg.FallbackSet) != "large-same" {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
	t.Run("symlink_to_a_shared_file", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "shared.yaml")
		writeFile(t, real, 0o644)
		link := filepath.Join(dir, RoutesFileName)
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRoutes(link); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), RoutesFileName)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRoutes(p); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), RoutesFileName)
		if err := os.WriteFile(p, []byte(standardList+"#"+strings.Repeat("x", 1<<20)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRoutes(p); err == nil || !strings.Contains(err.Error(), "1 MiB") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("override_inside_a_checkout", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(repo, "config"), 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(repo, "config", RoutesFileName)
		writeFile(t, p, 0o600)
		t.Setenv(RoutesFileEnv, p)
		if _, err := StandardRoutesPath(); err == nil || !strings.Contains(err.Error(), "Git checkout") {
			t.Fatalf("%v", err)
		}
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil || !cfg.RouteList.Unreadable {
			t.Fatalf("%v %+v", err, cfg.RouteList)
		}
	})
	t.Run("long_account_dir", func(t *testing.T) {
		long := "version: 1\nroutes:\n  - {id: one, provider: codex, model: m, group: g, enter: headless, account_dir: /" + strings.Repeat("a", maxRoutePath) + "}\n"
		if _, err := ParseRoutes([]byte(long)); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("%v", err)
		}
	})
}

// A loop that follows the list cannot be prepared for a transfer even while
// the list is unreadable and it has no routes: when the list comes back it
// would have a route list a transfer does not cover.
func TestTransferRefusesAFollowingLoopWhateverTheListsState(t *testing.T) {
	for _, content := range []string{standardList, ""} {
		writeRoutes(t, content)
		cfg, err := Load(loopFile(t, bbLoop))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.PrepareBBTransfer("thr_example_next"); err == nil {
			t.Fatalf("transfer prepared (list present=%t)", content != "")
		}
	}
}
