//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// routeAccountCatalogFixture makes a private catalog with two fictional
// entries, "work" (claude-code, its own directory) and "personal" (codex,
// inherited), and returns the catalog directory and their IDs.
func routeAccountCatalogFixture(t *testing.T) (dir, work, personal string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(root, "catalog")
	cli := filepath.Join(root, "fixture-cli")
	if err := os.WriteFile(cli, []byte("fictional stand-in"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = accounts.Update(dir, func(f *accounts.File) error {
		w := accounts.NewEntry(*f, accounts.Entry{Label: "work", Provider: "claude-code", Mode: "explicit_directory", Directory: "/example/accounts/work", CLI: cli, Workdir: root}, "", time.Now().UTC(), nil)
		p := accounts.NewEntry(*f, accounts.Entry{Label: "personal", Provider: "codex", Mode: "inherited", CLI: cli, Workdir: root}, "", time.Now().UTC(), nil)
		f.Entries = append(f.Entries, w, p)
		work, personal = w.ID, p.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, work, personal
}

// routeAccountLoop writes a headless Claude Code loop with a headless Codex
// fallback and a BB thread fallback, each stating the given account (empty
// for none), and loads it.
func routeAccountLoop(t *testing.T, primaryDir, primary, codex, bb string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	line := func(indent, id string) string {
		if id == "" {
			return ""
		}
		return indent + "account: " + id + "\n"
	}
	env := ""
	if primaryDir != "" {
		env = ", env: {CLAUDE_CONFIG_DIR: " + primaryDir + "}"
	}
	write(t, filepath.Join(dir, "fallbacks.yaml"), `version: 1
redundancy: {min_healthy: 1, span: provider}
candidates:
  - id: codex-personal
    adapter: codex
    provider: codex
    group: personal
`+line("    ", codex)+`    codex: {workdir: /example/project}
    probe: {kind: codex}
  - id: bb-work
    adapter: bb
    provider: claude-code
    group: work-bb
`+line("    ", bb)+`    bb: {thread: thr_example_work}
    probe: {kind: claude}
`, 0o600)
	path := filepath.Join(dir, "keepgoing.yaml")
	write(t, path, `version: 1
loop:
  name: example-accounts
  state_dir: state
  adapter: claude
  provider: claude-code
  group: work
`+line("  ", primary)+`  claude: {workdir: /example/project`+env+`}
  prompt: Continue the fictional task.
  fallbacks: fallbacks.yaml
`, 0o600)
	return loadFixture(t, path)
}

func TestRouteAccountsAreCheckedForConsistencyOnly(t *testing.T) {
	catalog, work, personal := routeAccountCatalogFixture(t)
	const missing = "22222222222222222222222222222222"
	unreadable := filepath.Join(t.TempDir(), "catalog")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(unreadable, accounts.FileName), "not a catalog", 0o600)

	type want struct {
		ok       bool
		stdout   []string
		stderr   []string
		doctor   map[string]string // check id -> status
		noDoctor []string
	}
	for _, tc := range []struct {
		name                         string
		catalog                      string
		dir, primary, codex, bbRoute string
		want                         want
	}{
		{"no account anywhere, catalog unreadable", unreadable, "/example/accounts/work", "", "", "", want{ok: true, noDoctor: []string{"accounts.catalog", "agent.primary.account"}}},
		{"matching directory and provider", catalog, "/example/accounts/work", work, personal, "", want{ok: true,
			stdout: []string{"account (primary): work, assigned by you; the entry's provider and directory match this route", "account (codex-personal): personal,"},
			doctor: map[string]string{"agent.primary.account": "ok", "agent.codex-personal.account": "ok"}}},
		{"headless route on another directory", catalog, "/example/accounts/personal", work, "", "", want{ok: false,
			stderr: []string{"error: route primary: account " + work + ": the accounts catalog entry names another account directory than this route"},
			doctor: map[string]string{"agent.primary.account": "error"}}},
		{"headless route on another provider", catalog, "", "", work, "", want{ok: false,
			stderr: []string{"error: route codex-personal: account " + work + ": the accounts catalog entry is for another provider"},
			doctor: map[string]string{"agent.codex-personal.account": "error"}}},
		{"headless route not in the catalog", catalog, "", missing, "", "", want{ok: false,
			stderr: []string{"error: route primary: account " + missing + ": the accounts catalog has no entry with this ID"},
			doctor: map[string]string{"agent.primary.account": "error"}}},
		{"bb thread route taken as given", catalog, "", "", "", personal, want{ok: true,
			stdout: []string{"account (bb-work): personal, assigned by you; not verified"},
			doctor: map[string]string{"agent.bb-work.account": "ok"}}},
		{"bb thread route not in the catalog", catalog, "", "", "", missing, want{ok: true,
			stderr: []string{"warning: route bb-work: account " + missing + ": the accounts catalog has no entry with this ID; assigned by you; not verified"},
			doctor: map[string]string{"agent.bb-work.account": "warning"}}},
		{"catalog unreadable never stops a loop", unreadable, "/example/accounts/personal", work, missing, missing, want{ok: true,
			stderr:   []string{"warning: the accounts catalog could not be read, so the stated account was not compared; nothing stops supervision"},
			doctor:   map[string]string{"accounts.catalog": "warning"},
			noDoctor: []string{"agent.primary.account", "agent.codex-personal.account", "agent.bb-work.account"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := routeAccountLoop(t, tc.dir, tc.primary, tc.codex, tc.bbRoute)
			var out, errb bytes.Buffer
			e := env{ctx: context.Background(), stdout: &out, stderr: &errb}
			if ok := validateRouteAccounts(e, cfg, tc.catalog); ok != tc.want.ok {
				t.Fatalf("validate ok %v\nstdout: %s\nstderr: %s", ok, &out, &errb)
			}
			for _, s := range tc.want.stdout {
				if !strings.Contains(out.String(), s) {
					t.Errorf("stdout lacks %q:\n%s", s, &out)
				}
			}
			for _, s := range tc.want.stderr {
				if !strings.Contains(errb.String(), s) {
					t.Errorf("stderr lacks %q:\n%s", s, &errb)
				}
			}
			if len(tc.want.stderr) == 0 && errb.Len() > 0 {
				t.Errorf("unexpected stderr: %s", &errb)
			}
			if strings.Contains(out.String()+errb.String(), "/example/accounts") {
				t.Errorf("a directory was printed:\n%s%s", &out, &errb)
			}
			var r doctorReport
			doctorRouteAccounts(&r, cfg, tc.catalog)
			got := map[string]doctorCheck{}
			for _, c := range r.Checks {
				got[c.ID] = c
				if strings.Contains(c.Message+c.Fix, "/example/accounts") {
					t.Errorf("doctor printed a directory: %+v", c)
				}
			}
			for id, status := range tc.want.doctor {
				if got[id].Status != status {
					t.Errorf("doctor %s = %+v, want %s (all: %+v)", id, got[id], status, r.Checks)
				}
			}
			for _, id := range tc.want.noDoctor {
				if _, ok := got[id]; ok {
					t.Errorf("doctor has %s: %+v", id, got[id])
				}
			}
		})
	}
}

// Status carries what each route states about its account, or the derived
// account context of one that states none. It reads the catalog only for a
// route that names an entry, from the default directory.
func TestStatusShowsTheStatedAccountOrTheDerivedContext(t *testing.T) {
	f := newFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	read := func() statusOut {
		t.Helper()
		code, out, errs := f.run("status", "--json")
		if code != 0 {
			t.Fatalf("status %d %s", code, errs)
		}
		var s statusOut
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := read()
	a := s.PrimaryAgent.Account
	if a == nil || a.Assigned != "none" || len(a.Context) != 32 || a.Account != "" || a.Check != "" {
		t.Fatalf("primary account %+v", a)
	}
	if s.TargetAgent.Account == nil || *s.TargetAgent.Account != *a {
		t.Fatalf("target account %+v", s.TargetAgent.Account)
	}
	const stated = "0123456789abcdef0123456789abcdef"
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  group: claude-account-a\n", "  group: claude-account-a\n  account: "+stated+"\n", 1), 0o600)
	s = read()
	// No catalog exists in this home, so there is no such entry. The primary
	// is a BB thread, which is never refused.
	if a := s.PrimaryAgent.Account; a == nil || a.Account != stated || a.Assigned != "by_you" || a.Check != accounts.RouteNotInCatalog || a.Label != "" || a.Context != "" {
		t.Fatalf("stated account %+v", a)
	}
}

// The panel data carries each route's stated account, compared with the
// catalog read once for the report, or its derived context.
func TestDockChainCarriesTheStatedAccount(t *testing.T) {
	work := "0123456789abcdef0123456789abcdef"
	catalog := &accounts.File{SchemaVersion: 1, Entries: []accounts.Entry{{ID: work, Label: "work", Provider: "claude-code", Mode: "explicit_directory", Directory: "/example/accounts/work"}}}
	primary := config.Candidate{ID: "primary", Agent: config.Agent{Adapter: "claude", Provider: "claude-code", Group: "work", Account: work, Claude: &config.Claude{Workdir: "/example/project", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/example/accounts/work"}}}}
	plain := config.Candidate{ID: "codex-plain", Agent: config.Agent{Adapter: "codex", Provider: "codex", Group: "codex", Codex: &config.Codex{Workdir: "/example/project"}}}
	for _, tc := range []struct {
		name    string
		catalog *accounts.File
		check   string
		label   string
	}{
		{"catalog read", catalog, accounts.RouteConsistent, "work"},
		{"catalog not read", nil, accounts.RouteCatalogUnavailable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dockHandoffChain(&dockChainInput{Primary: primary, Candidates: []config.Candidate{plain}, Summary: &fallbacksOut{}, Catalog: tc.catalog}, dockRouteUse{}, dockTesting{}, dockFallbacks{}, dockNow, false)
			if len(c.Entries) != 2 {
				t.Fatalf("entries %+v", c.Entries)
			}
			first, second := c.Entries[0].CatalogAccount, c.Entries[1].CatalogAccount
			if first == nil || first.Account != work || first.Label != tc.label || first.Check != tc.check || first.Assigned != "by_you" {
				t.Fatalf("first route %+v", first)
			}
			if second == nil || second.Assigned != "none" || second.Account != "" || len(second.Context) != 32 {
				t.Fatalf("second route %+v", second)
			}
			b, err := json.Marshal(c.Entries[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"catalogAccount":{"id":"`+work+`"`) || strings.Contains(string(b), "/example/accounts") || strings.Contains(string(b), "Refused") {
				t.Fatalf("entry JSON %s", b)
			}
		})
	}
}

// routes add can state an account; moving, switching off and on keep it.
func TestRoutesAddStatesAnAccountAndEditsKeepIt(t *testing.T) {
	r := newRoutesFixture(t)
	const work = "0123456789abcdef0123456789abcdef"
	write(t, r.list, `version: 1
routes:
  # Work account, stated by hand.
  - id: work-large
    provider: claude-code
    model: example-large
    group: claude-work
    account: `+work+` # assigned by you
    enter: same_conversation
`, 0o600)
	code, v, errs := r.change("add", "--id", "personal-codex", "--provider", "codex", "--model", "example-codex", "--group", "codex-personal", "--enter", "new_conversation", "--account", "fedcba9876543210fedcba9876543210", "--yes")
	if code != 0 || v.Outcome != "changed" {
		t.Fatalf("add %d %+v %s", code, v, errs)
	}
	if !strings.Contains(r.read(), "account: fedcba9876543210fedcba9876543210") {
		t.Fatalf("after add:\n%s", r.read())
	}
	if code, v, errs = r.change("add", "--id", "bad-account", "--provider", "codex", "--model", "example-codex", "--group", "codex-other", "--enter", "new_conversation", "--account", "personal", "--dry-run"); code == 0 || v.Refusal == nil {
		t.Fatalf("a label as account: %d %+v %s", code, v, errs)
	}
	for _, step := range [][]string{{"move", "work-large", "--to", "2", "--yes"}, {"off", "work-large", "--yes"}, {"on", "work-large", "--yes"}} {
		if code, v, errs = r.change(step...); code != 0 || v.Outcome != "changed" {
			t.Fatalf("%v: %d %+v %s", step, code, v, errs)
		}
		if got := r.read(); !strings.Contains(got, "    account: "+work+" # assigned by you\n") || !strings.Contains(got, "# Work account, stated by hand.") {
			t.Fatalf("after %v:\n%s", step, got)
		}
	}
	routes, err := config.LoadRoutes(r.list)
	if err != nil {
		t.Fatal(err)
	}
	if rt, ok := routes.Find("work-large"); !ok || rt.Account != work {
		t.Fatalf("work-large %+v", rt)
	}
}

// Guided fallbacks add rewrites the files through YAML nodes; a stated
// account on an existing candidate and on the loop survives, with its comment.
func TestFallbacksAddKeepsStatedAccounts(t *testing.T) {
	const work = "0123456789abcdef0123456789abcdef"
	list := "version: 1\ncandidates:\n  - id: work-codex\n    adapter: codex\n    provider: codex\n    group: work\n    account: " + work + " # assigned by you\n    codex: {workdir: /example/project}\n"
	added := config.Candidate{ID: "personal-codex", Agent: config.Agent{Adapter: "codex", Provider: "codex", Group: "personal", Codex: &config.Codex{Workdir: "/example/project"}}}
	out, err := insertCandidateYAML([]byte(list), added, 1)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := config.ParseFallbacks(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Candidates) != 2 || fb.Candidates[0].Account != work || fb.Candidates[1].Account != "" || !strings.Contains(string(out), "# assigned by you") {
		t.Fatalf("after insert:\n%s", out)
	}
	loop := "version: 1\nloop:\n  name: example-accounts\n  adapter: codex\n  account: " + work + " # assigned by you\n"
	edited, err := editLoopYAML([]byte(loop), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "account: "+work+" # assigned by you") {
		t.Fatalf("after loop edit:\n%s", edited)
	}
}

// The Accounts page says a route names an entry only from the chains the
// report read, and says none only when every loop and route was read.
func TestDockLinksAccountCardsToRoutes(t *testing.T) {
	named, other := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	loop := func(ids ...string) dockLoop {
		l := dockLoop{Chain: dockChain{Availability: "known"}}
		for _, id := range ids {
			a := &accounts.RouteAccount{Assigned: "none", Context: "0123456789abcdef0123456789abcdef"}
			if id != "" {
				a = &accounts.RouteAccount{Account: id, Assigned: "by_you"}
			}
			l.Chain.Entries = append(l.Chain.Entries, dockChainEntry{CatalogAccount: a})
		}
		return l
	}
	read := dockRegistry{Availability: "available", Count: 2}
	for _, tc := range []struct {
		name   string
		report dockReport
		want   [2]string
	}{
		{"every route read", dockReport{Complete: true, Registry: read, Loops: []dockLoop{loop(named, ""), loop("")}}, [2]string{accounts.RoutesNamed, accounts.RoutesNone}},
		{"no loops registered", dockReport{Complete: true, Registry: dockRegistry{Availability: "available"}}, [2]string{accounts.RoutesNone, accounts.RoutesNone}},
		{"incomplete read", dockReport{Registry: read, Loops: []dockLoop{loop(named)}}, [2]string{accounts.RoutesNamed, accounts.RoutesUnknown}},
		{"registry unavailable", dockReport{Complete: true, Registry: dockRegistry{Availability: "unavailable"}}, [2]string{accounts.RoutesUnknown, accounts.RoutesUnknown}},
		{"loops omitted", dockReport{Complete: true, Registry: dockRegistry{Availability: "available", Omitted: 1}, Loops: []dockLoop{loop("")}}, [2]string{accounts.RoutesUnknown, accounts.RoutesUnknown}},
		{"a chain not read", dockReport{Complete: true, Registry: read, Loops: []dockLoop{{Chain: dockChain{Availability: "unknown"}}}}, [2]string{accounts.RoutesUnknown, accounts.RoutesUnknown}},
		{"fallbacks omitted", func() dockReport {
			l := loop("")
			l.Chain.Omitted = 1
			return dockReport{Complete: true, Registry: read, Loops: []dockLoop{l}}
		}(), [2]string{accounts.RoutesUnknown, accounts.RoutesUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := accounts.View{Cards: []accounts.Card{{ID: named}, {ID: other}}}
			dockLinkAccountRoutes(&v, tc.report)
			if got := [2]string{v.Cards[0].Routes, v.Cards[1].Routes}; got != tc.want {
				t.Fatalf("routes %v, want %v", got, tc.want)
			}
		})
	}
}
