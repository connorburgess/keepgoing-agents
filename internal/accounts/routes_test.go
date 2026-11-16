package accounts

import (
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/config"
)

const (
	workID     = "0123456789abcdef0123456789abcdef"
	personalID = "fedcba9876543210fedcba9876543210"
	otherID    = "00000000000000000000000000000001"
	codexID    = "11111111111111111111111111111111"
	missingID  = "22222222222222222222222222222222"
)

func routeCatalog() *File {
	return &File{SchemaVersion: 1, Entries: []Entry{
		{ID: workID, Label: "work", Provider: "claude-code", Mode: "explicit_directory", Directory: "/example/accounts/work"},
		{ID: personalID, Label: "personal", Provider: "claude-code", Mode: "inherited"},
		{ID: codexID, Label: "work codex", Provider: "codex", Mode: "explicit_directory", Directory: "/example/codex/work"},
		{ID: otherID, Label: "studio tool", Provider: "other", Mode: "externally_managed"},
	}}
}

func claudeRoute(account, dir string) config.Agent {
	a := config.Agent{Adapter: "claude", Provider: "claude-code", Group: "work", Account: account, Claude: &config.Claude{Workdir: "/example/project"}}
	if dir != "" {
		a.Claude.Env = map[string]string{"CLAUDE_CONFIG_DIR": dir}
	}
	return a
}

func TestCheckRouteComparesOnlyWhatIsStated(t *testing.T) {
	bb := config.Agent{Adapter: "bb", Provider: "claude-code", Group: "work", BB: &config.BB{Thread: "thr_example_one"}}
	custom := config.Agent{Adapter: "custom", Provider: "studio-tool", Group: "studio", Custom: &config.Custom{Name: "studio-tool"}}
	codex := config.Agent{Adapter: "codex", Provider: "codex", Group: "codex", Codex: &config.Codex{Workdir: "/example/project", Env: map[string]string{"CODEX_HOME": "/example/codex/work/"}}}
	with := func(a config.Agent, id string) config.Agent { a.Account = id; return a }
	for _, tc := range []struct {
		name    string
		catalog *File
		agent   config.Agent
		check   string
		label   string
		refused bool
	}{
		{"same directory", routeCatalog(), claudeRoute(workID, "/example/accounts/work"), RouteConsistent, "work", false},
		{"route names no directory", routeCatalog(), claudeRoute(workID, ""), RouteConsistent, "work", false},
		{"entry names no directory", routeCatalog(), claudeRoute(personalID, "/example/accounts/personal"), RouteConsistent, "personal", false},
		{"cleaned directory matches", routeCatalog(), with(codex, codexID), RouteConsistent, "work codex", false},
		{"other directory", routeCatalog(), claudeRoute(workID, "/example/accounts/personal"), RouteDirectoryMismatch, "work", true},
		{"other provider", routeCatalog(), with(codex, workID), RouteProviderMismatch, "work", true},
		{"not in catalog", routeCatalog(), claudeRoute(missingID, ""), RouteNotInCatalog, "", true},
		{"empty catalog", &File{SchemaVersion: 1, Entries: []Entry{}}, claudeRoute(workID, ""), RouteNotInCatalog, "", true},
		{"catalog unreadable", nil, claudeRoute(workID, "/example/accounts/personal"), RouteCatalogUnavailable, "", false},
		{"bb thread taken as given", routeCatalog(), with(bb, codexID), RouteNotVerified, "work codex", false},
		{"bb thread not in catalog", routeCatalog(), with(bb, missingID), RouteNotInCatalog, "", false},
		{"defined agent on an other entry", routeCatalog(), with(custom, otherID), RouteConsistent, "studio tool", false},
		{"claude route on an other entry", routeCatalog(), claudeRoute(otherID, ""), RouteProviderMismatch, "studio tool", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckRoute(tc.catalog, tc.agent)
			if got.Check != tc.check || got.Label != tc.label || got.Refused != tc.refused || got.Assigned != "by_you" || got.Account != tc.agent.Account || got.Context != "" {
				t.Fatalf("got %+v, want check %s label %q refused %v", got, tc.check, tc.label, tc.refused)
			}
			if RouteCheckText(got.Check) == "" || strings.Contains(RouteCheckText(got.Check), "/example") {
				t.Fatalf("text for %s: %q", got.Check, RouteCheckText(got.Check))
			}
		})
	}
}

func TestRouteWithNoAccountCarriesOnlyItsContext(t *testing.T) {
	for _, f := range []*File{nil, routeCatalog()} {
		got := CheckRoute(f, claudeRoute("", "/example/accounts/work"))
		if got.Assigned != "none" || got.Check != "" || got.Account != "" || got.Label != "" || got.Refused || len(got.Context) != 32 {
			t.Fatalf("got %+v", got)
		}
		if strings.Contains(got.Context, "example") {
			t.Fatalf("context holds a path: %q", got.Context)
		}
	}
}

func TestRouteContextKeysProviderSelectionDirectoryAndGroup(t *testing.T) {
	base := claudeRoute("", "/example/accounts/work")
	same := claudeRoute("", "/example/accounts/work/")
	same.Claude.Model = "a-different-model"
	same.Claude.Workdir = "/example/elsewhere"
	if RouteContext(base) != RouteContext(same) {
		t.Fatal("a model, working directory or trailing slash changed the account context")
	}
	stated := base
	stated.Account = workID
	if RouteContext(base) != RouteContext(stated) {
		t.Fatal("the stated account changed the derived context")
	}
	otherDir := claudeRoute("", "/example/accounts/personal")
	inherited := claudeRoute("", "")
	otherGroup := claudeRoute("", "/example/accounts/work")
	otherGroup.Group = "personal"
	codex := config.Agent{Adapter: "codex", Provider: "codex", Group: "work", Codex: &config.Codex{Env: map[string]string{"CODEX_HOME": "/example/accounts/work"}}}
	bb := config.Agent{Adapter: "bb", Provider: "claude-code", Group: "work", BB: &config.BB{Thread: "thr_example_one"}}
	seen := map[string]string{RouteContext(base): "base"}
	for name, a := range map[string]config.Agent{"other directory": otherDir, "inherited": inherited, "other group": otherGroup, "other provider": codex, "bb thread": bb} {
		k := RouteContext(a)
		if prev, dup := seen[k]; dup {
			t.Fatalf("%s has the same context as %s", name, prev)
		}
		seen[k] = name
	}
	if s, d := RouteSelection(bb); s != "bb_thread" || d != "" {
		t.Fatalf("bb selection %q %q", s, d)
	}
	if s, d := RouteSelection(inherited); s != "inherited" || d != "" {
		t.Fatalf("inherited selection %q %q", s, d)
	}
}
