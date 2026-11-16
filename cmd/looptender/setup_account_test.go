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
	"unicode/utf8"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// setupCatalogFixture writes a private accounts catalog holding entries and
// makes init and fallbacks add read it. It returns the entries' IDs in order.
func setupCatalogFixture(t *testing.T, entries ...accounts.Entry) []string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "fixture-cli")
	if err := os.WriteFile(cli, []byte("fictional stand-in"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "catalog")
	var ids []string
	err = accounts.Update(dir, func(f *accounts.File) error {
		for _, e := range entries {
			if e.Provider == "claude-code" || e.Provider == "codex" {
				e.CLI, e.Workdir = cli, root
			}
			n := accounts.NewEntry(*f, e, "", time.Now().UTC(), nil)
			f.Entries = append(f.Entries, n)
			ids = append(ids, n.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := setupAccountsDir
	setupAccountsDir = dir
	t.Cleanup(func() { setupAccountsDir = before })
	return ids
}

// addCatalog is a catalog for an add fixture: a Claude Code subscription in
// its own directory, a Codex account in its own directory, and a Codex
// account on the normal login.
func addCatalog(t *testing.T, f addFixture) (claude, codex, codexInherited string) {
	t.Helper()
	ids := setupCatalogFixture(t,
		accounts.Entry{Label: "Personal Pro", Provider: "claude-code", Mode: "explicit_directory", Directory: f.account("claude-b")},
		accounts.Entry{Label: "team codex", Provider: "codex", Mode: "explicit_directory", Directory: f.account("codex-b")},
		accounts.Entry{Label: "codex-everyday", Provider: "codex", Mode: "inherited"},
	)
	return ids[0], ids[1], ids[2]
}

func TestFallbacksAddNamesAnAccount(t *testing.T) {
	f := newAddFixture(t)
	claude, _, _ := addCatalog(t, f)
	code, out, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--account", claude)
	if code != exitOK {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "Named account: Personal Pro, stated by you (written as account: "+claude+")") {
		t.Errorf("preview does not name the account:\n%s", out)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.Account != claude || c.Group != "personal-pro" || c.ProbeEnv()["CLAUDE_CONFIG_DIR"] != f.account("claude-b") {
		t.Fatalf("candidate: account %q group %q env %v", c.Account, c.Group, c.ProbeEnv())
	}
	if !strings.Contains(string(mustRead(t, f.fallbacks)), "account: "+claude) {
		t.Fatal("fallbacks.yaml does not state the account")
	}
	if r := accounts.CheckRoute(setupCatalog(), c.Agent); r.Check != accounts.RouteConsistent {
		t.Fatalf("validate would say %s", r.Check)
	}
}

func TestFallbacksAddJSONReportsTheNamedAccount(t *testing.T) {
	f := newAddFixture(t)
	_, codex, _ := addCatalog(t, f)
	// An entry on the normal login is named without a directory.
	code, out, errOut := f.add(t, "", "--non-interactive", "--agent", "codex", "--account", codex, "--dry-run", "--json")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	var r fallbackAddReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if a := r.Candidate.CatalogAccount; a == nil || a.ID != codex || a.Label != "team codex" || r.Candidate.Group != "team-codex" || r.Candidate.Account != "explicit_directory" {
		t.Fatalf("report: %+v", r.Candidate)
	}
}

func TestFallbacksAddNamedAccountOnTheNormalLogin(t *testing.T) {
	f := newAddFixture(t)
	_, _, inherited := addCatalog(t, f)
	code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "codex", "--account", inherited, "--model", "example-other")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[len(cfg.FallbackSet.Candidates)-1]
	if c.Account != inherited || c.Group != "codex-everyday" || len(c.ProbeEnv()) != 0 {
		t.Fatalf("candidate: account %q group %q env %v", c.Account, c.Group, c.ProbeEnv())
	}
}

func TestFallbacksAddRefusesAnAccountThatDoesNotMatch(t *testing.T) {
	f := newAddFixture(t)
	claude, _, _ := addCatalog(t, f)
	before := f.files(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"another provider":  {[]string{"--agent", "codex", "--account", claude}, "the accounts catalog entry is for another provider"},
		"another directory": {[]string{"--agent", "claude", "--account", claude, "--account-dir", f.account("codex-b")}, "names another account directory than this route"},
		"not in catalog":    {[]string{"--agent", "claude", "--account", "0123456789abcdef0123456789abcdef"}, "has no entry with ID"},
	} {
		code, out, errOut := f.add(t, "", append([]string{"--non-interactive"}, tc.args...)...)
		if code != exitUsage || !strings.Contains(errOut, tc.want) || !strings.Contains(errOut, "nothing written") {
			t.Errorf("%s: %d %s %s", name, code, out, errOut)
		}
	}
	setupAccountsDir = filepath.Join(t.TempDir(), "unreadable")
	if err := os.Mkdir(setupAccountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(setupAccountsDir, accounts.FileName), "not a catalog", 0o600)
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--account", claude); code != exitUsage || !strings.Contains(errOut, "could not be read") {
		t.Errorf("unreadable catalog: %d %s", code, errOut)
	}
	if f.files(t) != before {
		t.Fatal("a refused --account changed the loop's files")
	}
}

func TestFallbacksAddWizardOffersNamedAccounts(t *testing.T) {
	f := newAddFixture(t)
	claude, _, _ := addCatalog(t, f)
	// agent, a number out of range, the first account, group (default),
	// model, probe model, handoff, confirmation
	input := "claude\n4\n1\n\n\n\nno\nyes\n"
	code, out, errOut := f.add(t, input)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	for _, want := range []string{"Your named accounts", "1) Personal Pro  its own signed-in directory", "! Choose a number from 1 to 1", "Named account: Personal Pro", "Quota group [personal-pro]"} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "team codex") {
		t.Error("the wizard offered a Codex account for a Claude Code fallback")
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.FallbackSet.Candidates[0]; c.Account != claude || c.ProbeEnv()["CLAUDE_CONFIG_DIR"] != f.account("claude-b") {
		t.Fatalf("candidate: %q %v", c.Account, c.ProbeEnv())
	}
}

// A directory typed in place of a number states no account, even when it
// is an entry's directory: looptender never derives the statement.
func TestFallbacksAddWizardDirectoryStatesNoAccount(t *testing.T) {
	f := newAddFixture(t)
	addCatalog(t, f)
	input := "claude\n" + f.account("claude-b") + "\npool-b\n\n\nno\nyes\n"
	if code, out, errOut := f.add(t, input); code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.FallbackSet.Candidates[0]; c.Account != "" {
		t.Fatalf("a typed directory stated account %q", c.Account)
	}
}

func TestFallbacksAddWizardWithNoNamedAccountsSaysHow(t *testing.T) {
	f := newAddFixture(t)
	input := "claude\n" + f.account("claude-b") + "\npool-b\n\n\nno\nyes\n"
	code, out, errOut := f.add(t, input)
	if code != exitOK || !strings.Contains(out, "Name your accounts with keepgoing accounts add to pick them here.") {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
}

func TestInitNamesTheFirstRoutesAccount(t *testing.T) {
	f := newSetupFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "accounts", "codex-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	ids := setupCatalogFixture(t,
		accounts.Entry{Label: "Main Codex", Provider: "codex", Mode: "explicit_directory", Directory: filepath.Join(f.root, "accounts", "codex-a")},
		accounts.Entry{Label: "studio", Provider: "claude-code", Mode: "inherited"},
	)
	code, out, errOut := f.run(t, append(f.args(), "--account", ids[0]), "")
	if code != exitOK {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "Named account: Main Codex, stated by you") {
		t.Errorf("output does not name the account:\n%s", out)
	}
	cfg, err := config.Load(filepath.Join(f.destination, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Account != ids[0] || cfg.Loop.Group != "main-codex" || cfg.Loop.Codex.Env["CODEX_HOME"] != filepath.Join(f.root, "accounts", "codex-a") {
		t.Fatalf("loop: account %q group %q env %v", cfg.Loop.Account, cfg.Loop.Group, cfg.Loop.Codex.Env)
	}

	// Another provider's entry is refused, and nothing is written.
	g := newSetupFixture(t)
	if code, _, errOut := g.run(t, append(g.args(), "--account", ids[1]), ""); code == exitOK || !strings.Contains(errOut, "another provider") {
		t.Fatalf("provider mismatch: %d %s", code, errOut)
	}
	if _, err := os.Stat(g.destination); !os.IsNotExist(err) {
		t.Fatalf("a refused setup wrote %s: %v", g.destination, err)
	}
}

func TestInitJSONCarriesTheNamedAccount(t *testing.T) {
	f := newSetupFixture(t)
	ids := setupCatalogFixture(t, accounts.Entry{Label: "everyday", Provider: "codex", Mode: "inherited"})
	code, out, errOut := f.run(t, append(f.args(), "--account", ids[0], "--json", "--dry-run"), "")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	var r initReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Primary.CatalogAccount != ids[0] || r.Primary.Account != "inherited" || r.Primary.Agent.Group != "everyday" {
		t.Fatalf("primary: %+v", r.Primary)
	}
}

func TestInitWizardOffersNamedAccountsForAThread(t *testing.T) {
	f := newSetupFixture(t)
	ids := setupCatalogFixture(t,
		accounts.Entry{Label: "studio", Provider: "claude-code", Mode: "inherited"},
		accounts.Entry{Label: "bb-home", Provider: "bb", Mode: "externally_managed"},
		accounts.Entry{Label: "team codex", Provider: "codex", Mode: "inherited"},
	)
	args := []string{"--name", "example-loop", "--agent", "bb", "--thread", "thr_example", "--provider", "claude-code", "--project", f.project, "--directory", f.destination, "--task", "Complete and verify the fictional example task.", "--with-goal=false", "--routes", "own"}
	// the account (second offered), then defaults for group and limits
	input := "2\n\n\n\n\n\n\n\n\n"
	code, out, errOut := f.run(t, args, input)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "1) studio") || !strings.Contains(out, "2) bb-home") || strings.Contains(out, "team codex") {
		t.Errorf("offered the wrong accounts:\n%s", out)
	}
	cfg, err := config.Load(filepath.Join(f.destination, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Loop.Account != ids[1] || cfg.Loop.Group != "bb-home" {
		t.Fatalf("loop: account %q group %q", cfg.Loop.Account, cfg.Loop.Group)
	}
	if r := accounts.CheckRoute(setupCatalog(), cfg.Loop.Agent); r.Check != accounts.RouteNotVerified {
		t.Fatalf("validate would say %s", r.Check)
	}
}

// A BB thread's account is chosen by BB per project, so a named account on
// a BB thread fallback is the person's statement, not compared.
func TestFallbacksAddNamesAnAccountForABBThread(t *testing.T) {
	f := newAddFixture(t)
	ids := setupCatalogFixture(t, accounts.Entry{Label: "bb-home", Provider: "bb", Mode: "externally_managed"})
	code, out, errOut := f.add(t, "", "--non-interactive", "--agent", "bb", "--thread", "thr_example_other", "--provider", "claude-code", "--account", ids[0], "--dry-run", "--json")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	var r fallbackAddReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if a := r.Candidate.CatalogAccount; a == nil || a.ID != ids[0] || a.Label != "bb-home" {
		t.Fatalf("report: %+v", r.Candidate)
	}
}

// validatesAs runs validate's account comparison on what setup wrote, with
// the catalog setup read, and returns its output.
func validatesAs(t *testing.T, cfgPath string) string {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if !validateRouteAccounts(env{context.Background(), &out, &errOut}, cfg, setupAccountsDir) || errOut.Len() != 0 {
		t.Fatalf("validate rejects what setup wrote: %s %s", out.String(), errOut.String())
	}
	return out.String()
}

func TestSetupWritesWhatValidateAccepts(t *testing.T) {
	f := newAddFixture(t)
	claude, codex, _ := addCatalog(t, f)
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "claude", "--account", claude); code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	if code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "codex", "--account", codex); code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	out := validatesAs(t, f.cfg)
	for _, want := range []string{"Personal Pro, assigned by you; the entry's provider and directory match this route", "team codex, assigned by you; the entry's provider and directory match this route"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output missing %q:\n%s", want, out)
		}
	}
}

// An entry on the normal login does not fit a route that names its own
// directory, and the refusal names the entries that do.
func TestSetupRefusesANormalLoginEntryForARouteWithADirectory(t *testing.T) {
	f := newAddFixture(t)
	_, _, inherited := addCatalog(t, f)
	before := f.files(t)
	code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "codex", "--account", inherited, "--account-dir", f.account("codex-b"))
	if code != exitUsage || !strings.Contains(errOut, `account "codex-everyday" does not fit this route: the entry uses your normal login, but this route names its own account directory`) || !strings.Contains(errOut, `Named accounts that fit it: "team codex".`) {
		t.Fatalf("%d %s", code, errOut)
	}
	if f.files(t) != before {
		t.Fatal("a refused --account changed the loop's files")
	}
}

// A BB thread route fits only an entry for BB or for the thread's provider,
// the entries the guided questions offer.
func TestSetupRefusesAnotherProvidersEntryForABBThread(t *testing.T) {
	f := newAddFixture(t)
	ids := setupCatalogFixture(t,
		accounts.Entry{Label: "team codex", Provider: "codex", Mode: "inherited"},
		accounts.Entry{Label: "bb-home", Provider: "bb", Mode: "externally_managed"},
		accounts.Entry{Label: "example-cursor", Provider: "cursor", Mode: "externally_managed"},
	)
	for _, id := range []string{ids[0], ids[2]} {
		code, _, errOut := f.add(t, "", "--non-interactive", "--agent", "bb", "--thread", "thr_example_other", "--provider", "claude-code", "--account", id, "--dry-run")
		if code != exitUsage || !strings.Contains(errOut, "not for BB or this thread's provider (claude-code)") || !strings.Contains(errOut, `Named accounts that fit it: "bb-home".`) {
			t.Errorf("%d %s", code, errOut)
		}
	}
	g := newSetupFixture(t)
	args := []string{"--non-interactive", "--name", "example-loop", "--agent", "bb", "--thread", "thr_example", "--provider", "claude-code", "--project", g.project, "--directory", g.destination, "--task", "Complete and verify the fictional example task.", "--account", ids[0]}
	if code, _, errOut := g.run(t, args, ""); code == exitOK || !strings.Contains(errOut, "not for BB or this thread's provider") {
		t.Fatalf("init: %d %s", code, errOut)
	}
}

// The init guide offers named accounts for each fallback it asks about,
// and what it writes is what validate accepts.
func TestInitWizardOffersNamedAccountsForAFallback(t *testing.T) {
	f := newSetupFixture(t)
	dir := filepath.Join(f.root, "accounts", "claude-b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ids := setupCatalogFixture(t,
		accounts.Entry{Label: "Personal Pro", Provider: "claude-code", Mode: "explicit_directory", Directory: dir},
		accounts.Entry{Label: "team codex", Provider: "codex", Mode: "inherited"},
	)
	args := []string{"--name", "example-loop", "--agent", "codex", "--account-dir", "", "--project", f.project, "--directory", f.destination, "--task", "Complete and verify the fictional example task.", "--with-goal=false", "--routes", "own", "--group", "codex-main"}
	// model (default), a fallback on claude, the first named account, its group (default),
	// no more fallbacks, then defaults for the limits
	input := "\nclaude\n1\n\n\n\n\n\n\n\n\n\n\n"
	code, out, errOut := f.run(t, args, input)
	if code != exitOK {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "1) Personal Pro  its own signed-in directory") || strings.Contains(out, "team codex") {
		t.Errorf("offered the wrong accounts:\n%s", out)
	}
	cfgPath := filepath.Join(f.destination, config.FileName)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.FallbackSet.Candidates[0]
	if c.Account != ids[0] || c.Group != "personal-pro" || c.ProbeEnv()["CLAUDE_CONFIG_DIR"] != dir {
		t.Fatalf("fallback: account %q group %q env %v", c.Account, c.Group, c.ProbeEnv())
	}
	if cfg.Loop.Account != "" {
		t.Fatalf("the first route states %q, though none was named", cfg.Loop.Account)
	}
	if out := validatesAs(t, cfgPath); !strings.Contains(out, "account (fallback-1): Personal Pro, assigned by you; the entry's provider and directory match this route") {
		t.Errorf("validate: %s", out)
	}
}

// A long name is shown bounded, and the list's second column lines up by
// characters, not bytes.
func TestSetupPickListBoundsAndAlignsNames(t *testing.T) {
	long := strings.Repeat("ö", 60)
	setupCatalogFixture(t,
		accounts.Entry{Label: long, Provider: "codex", Mode: "inherited"},
		accounts.Entry{Label: "short", Provider: "codex", Mode: "inherited"},
	)
	var b bytes.Buffer
	printSetupAccountChoices(&b, setupAccountChoices(setupCatalog(), "codex", ""))
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", lines)
	}
	col := func(line string) int { return utf8.RuneCountInString(line[:strings.Index(line, "your normal login")]) }
	if !strings.Contains(lines[1], strings.Repeat("ö", setupLabelWidth-1)+"…") || strings.Contains(lines[1], strings.Repeat("ö", setupLabelWidth)) {
		t.Errorf("long name not bounded: %q", lines[1])
	}
	if col(lines[1]) != col(lines[2]) {
		t.Errorf("columns differ: %q / %q", lines[1], lines[2])
	}
}
