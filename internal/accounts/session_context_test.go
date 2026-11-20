package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/envvar"
)

// sessionEnvironment is a fictional agent session: every variable of
// envvar.SupervisorSession with a value that differs per session.
func sessionEnvironment(session string, upper bool) []string {
	env := []string{}
	for _, name := range envvar.SupervisorSession {
		if !upper {
			name = strings.ToLower(name)
		}
		env = append(env, name+"=example-"+session)
	}
	return env
}

func TestContextIgnoresTheCallerSession(t *testing.T) {
	f := File{Salt: strings.Repeat("ab", 16)}
	work := Entry{Label: "work", Provider: "claude-code", Mode: "explicit_directory", CLI: "/example/bin/claude", Directory: "/example/claude-work", Workdir: "/example/projects"}
	personal := Entry{Label: "personal", Provider: "codex", Mode: "inherited", CLI: "/example/bin/codex", Workdir: "/example/projects"}
	shell := []string{"HOME=/example/home", "ANTHROPIC_API_KEY=fictional-a", "PATH=/example/bin"}
	for _, windows := range []bool{false, true} {
		for _, e := range []Entry{work, personal} {
			plain := contextForOS(f, e, shell, windows)
			for _, tc := range []struct {
				name string
				env  []string
				same bool
			}{
				{"session one", append(slices.Clone(shell), sessionEnvironment("one", true)...), true},
				{"session two", append(sessionEnvironment("two", true), shell...), true},
				// Windows and Unix alike: the list is matched in any letter case,
				// as WithoutSupervisorSession drops it from execution.
				{"lower-case session", append(slices.Clone(shell), sessionEnvironment("three", false)...), true},
				{"real auth change", []string{"HOME=/example/home", "ANTHROPIC_API_KEY=fictional-b", "PATH=/example/bin"}, false},
				{"other home", []string{"HOME=/example/other", "ANTHROPIC_API_KEY=fictional-a", "PATH=/example/bin"}, false},
				// Close to the list but not on it: still bound.
				{"config directory", append(slices.Clone(shell), "CLAUDE_CONFIG_DIR=/example/claude-other", "CODEX_HOME=/example/codex-other"), false},
				{"session prefix", append(slices.Clone(shell), "CLAUDE_CODE_SESSION_ID_EXTRA=example"), false},
			} {
				if got := contextForOS(f, e, tc.env, windows) == plain; got != tc.same {
					t.Errorf("%s %s windows=%t: same=%t, want %t", e.Label, tc.name, windows, got, tc.same)
				}
			}
			for _, change := range []func(*Entry){
				func(x *Entry) { x.Workdir = "/example/projects-other" },
				func(x *Entry) { x.CLI = "/example/other/bin/claude" },
			} {
				x := e
				change(&x)
				if contextForOS(f, x, append(slices.Clone(shell), sessionEnvironment("one", true)...), windows) == plain {
					t.Errorf("%s windows=%t: a directory or CLI change kept the context", e.Label, windows)
				}
			}
		}
	}
}

func TestExecutionEnvironmentDropsTheCallerSession(t *testing.T) {
	work := Entry{Provider: "claude-code", Mode: "explicit_directory", Directory: "/example/claude-work"}
	personal := Entry{Provider: "codex", Mode: "inherited"}
	env := append([]string{"HOME=/example/home", "CODEX_TOOL_CALL_ID=example", "BB_CLI=/example/bin/bb", "CLAUDE_CONFIG_DIR=/example/claude-other"}, sessionEnvironment("one", true)...)
	env = append(env, sessionEnvironment("one", false)...)
	for _, windows := range []bool{false, true} {
		for _, e := range []Entry{work, personal} {
			out := executionEnvironmentForOS(e, env, windows)
			for _, kv := range out {
				name, _, _ := strings.Cut(kv, "=")
				if envvar.IsSupervisorSession(name) {
					t.Errorf("windows=%t %s: %s reached the status command", windows, e.Mode, name)
				}
			}
			for _, kept := range []string{"HOME=/example/home", "CODEX_TOOL_CALL_ID=example", "BB_CLI=/example/bin/bb"} {
				if !slices.Contains(out, kept) {
					t.Errorf("windows=%t %s: %s dropped", windows, e.Mode, kept)
				}
			}
			want := "CLAUDE_CONFIG_DIR=/example/claude-other"
			if e.Mode == "explicit_directory" {
				want = "CLAUDE_CONFIG_DIR=/example/claude-work"
			}
			if !slices.Contains(out, want) {
				t.Errorf("windows=%t %s: account directory %q missing", windows, e.Mode, want)
			}
		}
	}
}

// earlierContext computes a binding as builds before the current definition
// did: a plain HMAC over the same inputs, the caller's session variables
// included (the CLAUDE_ prefix covered most of them).
func earlierContext(f File, e Entry, env []string) string {
	m := map[string]string{}
	for _, pair := range env {
		k, v, _ := strings.Cut(pair, "=")
		if k != "CODEX_TOOL_CALL_ID" && (k == "HOME" || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "CLAUDE_")) {
			m[k] = v
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	key, _ := hex.DecodeString(f.Salt)
	h := hmac.New(sha256.New, key)
	enc := json.NewEncoder(h)
	_ = enc.Encode([]string{e.Provider, e.Mode, e.CLI, e.Directory, e.Workdir})
	for _, k := range keys {
		_ = enc.Encode([]string{k, m[k]})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestBoundAcceptsAnEarlierDefinitionOnce(t *testing.T) {
	f := File{Salt: strings.Repeat("cd", 16)}
	base := Entry{Label: "work", Provider: "claude-code", Mode: "inherited", CLI: "/example/bin/claude", Workdir: "/example/projects"}
	added := append([]string{"HOME=/example/home", "ANTHROPIC_API_KEY=fictional-a"}, sessionEnvironment("add", true)...)
	checked := append([]string{"HOME=/example/home", "ANTHROPIC_API_KEY=fictional-a"}, sessionEnvironment("check", true)...)
	changed := append([]string{"HOME=/example/home", "ANTHROPIC_API_KEY=fictional-b"}, sessionEnvironment("check", true)...)
	for _, tc := range []struct {
		name     string
		bound    func(Entry) string
		env      []string
		accepted bool
	}{
		{"current, other session", func(e Entry) string { return Context(f, e, added) }, checked, true},
		{"current, real change", func(e Entry) string { return Context(f, e, added) }, changed, false},
		{"earlier, other session", func(e Entry) string { return earlierContext(f, e, added) }, checked, true},
		// An earlier digest covered values that are no longer inputs, so it
		// cannot be compared; it is accepted once and then replaced.
		{"earlier, other key", func(e Entry) string { return earlierContext(f, e, added) }, changed, true},
	} {
		e := base
		e.Context = tc.bound(e)
		got, ok := Bound(f, e, tc.env)
		if ok != tc.accepted {
			t.Errorf("%s: accepted=%t, want %t", tc.name, ok, tc.accepted)
		}
		if got != Context(f, e, tc.env) || !protectedDefinition(f, got) {
			t.Errorf("%s: recorded context is not the current definition", tc.name)
		}
		// After rebinding, the same entry compares exactly.
		e.Context = got
		if _, ok := Bound(f, e, append(slices.Clone(tc.env), "ANTHROPIC_API_KEY=fictional-c")); ok {
			t.Errorf("%s: a rebound entry accepted a real change", tc.name)
		}
	}
}

func TestEarlierCatalogLoadsAndKeepsItsObservation(t *testing.T) {
	dir, _ := fixture(t)
	now := time.Now().UTC()
	// Rewrite the fixture as an earlier build stored it, with a good check.
	if err := Update(dir, func(f *File) error {
		x := &f.Entries[0]
		x.Context = earlierContext(*f, *x, append([]string{"HOME=/example"}, sessionEnvironment("add", true)...))
		o := Observation{Revision: x.Revision, Context: x.Context, At: now.Add(-time.Hour), Auth: "reported_present", Plan: "pro", Version: "2.1.318"}
		failed := o
		failed.At = now
		failed.Auth = "unavailable"
		failed.Plan = ""
		failed.Reason = "context_changed"
		x.Last, x.Attempt = &o, &failed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal("an earlier catalog no longer loads")
	}
	if protectedDefinition(f, f.Entries[0].Context) {
		t.Fatal("earlier digest read as current")
	}
	c := Project(f, now).Cards[0]
	if c.Auth.Text != "Selected CLI reports login" || c.Plan.Text != "Pro" || !c.Auth.ObservedAt.Equal(now.Add(-time.Hour)) || c.FailedCheckAt == nil || c.Reason != "context_changed" {
		t.Fatalf("the card did not keep the previous observation: %+v", c)
	}
}
