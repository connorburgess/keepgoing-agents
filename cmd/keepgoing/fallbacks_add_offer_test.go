package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

const addOfferList = "version: 1\nroutes:\n  - {id: codex-other, provider: codex, model: example-codex-large, group: codex-other, enter: headless}\n  - {id: same-conversation, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}\n"

func newAddOfferFixture(t *testing.T) addFixture {
	t.Helper()
	list := filepath.Join(t.TempDir(), config.RoutesFileName)
	if err := os.WriteFile(list, []byte(addOfferList), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.RoutesFileEnv, list)
	return newAddFixture(t)
}

func TestFallbacksAddOffersTheStandardListAndTurnsHandoffOn(t *testing.T) {
	f := newAddOfferFixture(t)
	before, _ := os.ReadFile(f.cfg)
	if !strings.Contains(string(before), "failover_on: []") {
		t.Fatalf("fixture: %s", before)
	}
	code, out, errOut := f.add(t, "yes\n")
	if code != exitOK {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	for _, want := range []string{
		"errors.failover_on: [] -> [rate_limit]",
		"Follow your standard route list and hand off to it on a rate limit, instead of adding a fallback? 1 of its 2 switched-on routes applies to this loop. Type yes to follow it (yes/no) [no]",
		"now follows the standard route list: codex-other",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	after, _ := os.ReadFile(f.cfg)
	// routes: standard goes below loop.name, at its indentation.
	nameLine := ""
	for _, line := range strings.SplitAfter(string(before), "\n") {
		if strings.TrimSpace(line) == "name: example-loop" {
			nameLine = line
		}
	}
	indent := nameLine[:len(nameLine)-len(strings.TrimLeft(nameLine, " "))]
	want := strings.Replace(strings.Replace(string(before), "failover_on: []", "failover_on: [rate_limit]", 1), nameLine, nameLine+indent+"routes: standard\n", 1)
	if string(after) != want {
		t.Fatalf("keepgoing.yaml:\n%s", after)
	}
	if _, err := os.Stat(f.fallbacks); !os.IsNotExist(err) {
		t.Fatalf("a fallbacks file was written: %v", err)
	}
	if got := strings.Join(timelineOps(t, f.cfg), ","); got != "follow_standard/terminal,handoff_on/terminal" {
		t.Fatalf("timeline: %s", got)
	}
}

func TestFallbacksAddOfferDeclinedChangesNothingAndContinues(t *testing.T) {
	f := newAddOfferFixture(t)
	before := f.files(t)
	code, out, _ := f.add(t, "no\n")
	if code == exitOK || !strings.Contains(out, "Follow your standard route list") || !strings.Contains(out, "Not following the standard route list") || f.files(t) != before {
		t.Fatalf("declined: %d\n%s", code, out)
	}
	// The wizard went on to its own questions, then ran out of input.
	if i := strings.Index(out, "Follow your standard route list"); !strings.Contains(out[i:], "?") || len(out[i:]) < 200 {
		t.Fatalf("the wizard did not continue:\n%s", out)
	}
}

func TestFallbacksAddOfferKeepsAHandoffSettingOfTheLoopsOwn(t *testing.T) {
	f := newAddOfferFixture(t)
	raw, _ := os.ReadFile(f.cfg)
	if err := os.WriteFile(f.cfg, []byte(strings.Replace(string(raw), "failover_on: []", "failover_on: [rate_limit, overloaded]", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.add(t, "y\n")
	if code != exitOK || strings.Contains(out, "errors.failover_on: [") || !strings.Contains(out, "hand off to it on rate_limit, overloaded, as the loop already does") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if after, _ := os.ReadFile(f.cfg); !strings.Contains(string(after), "failover_on: [rate_limit, overloaded]") {
		t.Fatalf("handoff setting changed:\n%s", after)
	}
}

func TestFallbacksAddMakesNoOfferWhenNothingApplies(t *testing.T) {
	f := newAddOfferFixture(t)
	if err := os.WriteFile(os.Getenv(config.RoutesFileEnv), []byte("version: 1\nroutes:\n  - {id: same-conversation, provider: claude-code, model: example-large, group: claude-large, enter: same_conversation}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ := f.add(t, "")
	if strings.Contains(out, "standard route list") {
		t.Fatalf("offered with nothing to apply:\n%s", out)
	}
	// Nor in a script.
	if err := os.WriteFile(os.Getenv(config.RoutesFileEnv), []byte(addOfferList), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ = f.add(t, "", "--non-interactive", "--agent", "codex", "--dry-run")
	if strings.Contains(out, "standard route list") {
		t.Fatalf("offered in a script:\n%s", out)
	}
}

func TestFallbacksAddOfferNeedsATypedYes(t *testing.T) {
	for name, input := range map[string]string{
		"blank line":          "\n",
		"end of input":        "",
		"anything else":       "codex\n\n\n\n\n\n\nno\n",
		"the wizard's answer": "claude\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newAddOfferFixture(t)
			before := f.files(t)
			_, out, _ := f.add(t, input)
			if !strings.Contains(out, "[no]: ") || strings.Contains(out, "now follows the standard route list") || f.files(t) != before {
				t.Fatalf("followed without a typed yes:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(f.cfg), "state", "incidents.json")); !os.IsNotExist(err) {
				t.Fatalf("a timeline record was written: %v", err)
			}
		})
	}
}

func TestFallbacksAddOfferWritesNothingUnderDryRun(t *testing.T) {
	f := newAddOfferFixture(t)
	before := f.files(t)
	_, out, _ := f.add(t, "yes\n", "--dry-run")
	if !strings.Contains(out, "errors.failover_on: [] -> [rate_limit]") || !strings.Contains(out, "Dry run: nothing written. Without --dry-run you would be asked") || strings.Contains(out, "[no]: ") || f.files(t) != before {
		t.Fatalf("dry run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.cfg), "state", "incidents.json")); !os.IsNotExist(err) {
		t.Fatalf("a timeline record was written: %v", err)
	}
}

func TestFallbacksAddMakesNoOfferWhenTheCommandLineDescribesAFallback(t *testing.T) {
	f := newAddOfferFixture(t)
	_, out, _ := f.add(t, "", "--agent", "codex")
	if strings.Contains(out, "standard route list") {
		t.Fatalf("offered although a fallback was described:\n%s", out)
	}
}

func TestFallbacksAddSaysWhyTheListIsNotOfferedForABlockFailoverOn(t *testing.T) {
	f := newAddOfferFixture(t)
	raw, _ := os.ReadFile(f.cfg)
	// A flow list over two lines cannot be changed line by line.
	block := strings.Replace(string(raw), "failover_on: []", "failover_on: [\n        ]", 1)
	if err := os.WriteFile(f.cfg, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, errOut := f.add(t, "")
	if !strings.Contains(out, "not written as an inline list") {
		t.Fatalf("no reason given:\n%s\n%s", out, errOut)
	}
}
