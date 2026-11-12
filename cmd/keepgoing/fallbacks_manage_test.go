package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// newManageFixture is a codex loop with three hand-commented fallbacks and
// automatic handoff on.
func newManageFixture(t *testing.T) addFixture {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	f := newAddFixture(t, "--fallback", "claude:pool-a")
	body := strings.Join([]string{
		"# header note",
		"version: 1",
		"redundancy: {min_healthy: 1, span: group}",
		"candidates:",
		"  # about first",
		"  - id: first # inline first",
		"    adapter: claude",
		"    group: pool-a",
		"    claude: {workdir: " + f.project + "}",
		"    probe: {kind: claude}",
		"  # about second",
		"  - id: second",
		"    adapter: claude",
		"    group: pool-b",
		"    claude:",
		"      workdir: " + f.project,
		"      env: {CLAUDE_CONFIG_DIR: " + f.account("claude-b") + "}",
		"    probe: {kind: claude}",
		"  # about third",
		"  - id: third",
		"    adapter: codex",
		"    group: pool-c",
		"    codex:",
		"      workdir: " + f.project,
		"      env: {CODEX_HOME: " + f.account("codex-b") + "}",
		"    probe: {kind: codex}",
		"",
	}, "\n")
	if err := os.WriteFile(f.fallbacks, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.cfg)
	if err := os.WriteFile(f.cfg, append([]byte("# config note\n"), raw...), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.fallbacks, f.cfg} {
		mode := os.FileMode(0o640)
		if p == f.cfg {
			mode = 0o644
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(f.cfg)
	if err != nil || len(cfg.FallbackSet.Candidates) != 3 || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("fixture: %v", err)
	}
	return f
}

func (f addFixture) manage(t *testing.T, input, sub string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := fallbacksManage(env{context.Background(), &out, &errOut}, sub, append(args, "-c", f.cfg), strings.NewReader(input))
	return code, out.String(), errOut.String()
}

func (f addFixture) ids(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cfg.FallbackSet.Candidates {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func (f addFixture) modes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	for path, want := range map[string]os.FileMode{f.fallbacks: 0o640, f.cfg: 0o644} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", filepath.Base(path), info.Mode().Perm(), want)
		}
	}
}

func TestFallbacksHandoffOnAndOff(t *testing.T) {
	f := newManageFixture(t)
	before := f.files(t)
	// Declined, default and ended input write nothing; so does a dry run.
	for name, input := range map[string]string{"declined": "no\n", "default": "\n", "ended": ""} {
		code, out, errOut := f.manage(t, input, "handoff", "off")
		if name == "ended" && code != exitUsage || name != "ended" && (code != exitOK || !strings.Contains(out, "Cancelled: nothing written.")) || f.files(t) != before {
			t.Fatalf("%s: %d %s %s", name, code, out, errOut)
		}
	}
	code, out, _ := f.manage(t, "", "handoff", "off", "--dry-run")
	if code != exitOK || !strings.Contains(out, "Dry run: nothing written.") || f.files(t) != before {
		t.Fatalf("%d %s", code, out)
	}
	code, out, errOut := f.manage(t, "yes\n", "handoff", "off")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{"Now:    on for rate_limit", "After:  OFF", "The\n  fallbacks stay saved", "Handoff order (unchanged)", "Turn automatic handoff off? (yes/no) [no]", "Automatic handoff is off.", "Nothing is run: no agent, model or check."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(f.cfg)
	raw, _ := os.ReadFile(f.cfg)
	if err != nil || len(cfg.Loop.Errors.FailoverOn) != 0 || cfg.FallbackSet == nil || !strings.HasPrefix(string(raw), "# config note\n") {
		t.Fatalf("%v\n%s", err, raw)
	}
	f.modes(t)
	if code, out, _ := f.manage(t, "", "handoff", "off"); code != exitOK || !strings.Contains(out, "already off. Nothing was changed.") {
		t.Fatalf("%d %s", code, out)
	}
	// JSON: preview equals the written result; a repeat is "unchanged".
	var preview, changed, again fallbackEditReport
	code, out, errOut = f.manage(t, "", "handoff", "on", "--json", "--dry-run")
	if err := json.Unmarshal([]byte(out), &preview); code != exitOK || err != nil {
		t.Fatalf("%d %s %v", code, errOut, err)
	}
	if preview.Kind != "fallback_handoff" || preview.Outcome != "preview" || !preview.Handoff.Automatic || preview.Handoff.WasAutomatic || !preview.Handoff.Changed || len(preview.Order) != 3 || preview.Active != "primary" {
		t.Fatalf("bad preview: %+v", preview)
	}
	code, out, errOut = f.manage(t, "", "handoff", "on", "--json", "--yes")
	if err := json.Unmarshal([]byte(out), &changed); code != exitOK || err != nil || changed.Outcome != "changed" {
		t.Fatalf("%d %s %v", code, errOut, err)
	}
	changed.Outcome = "preview"
	a, _ := json.Marshal(preview)
	b, _ := json.Marshal(changed)
	if !bytes.Equal(a, b) {
		t.Fatalf("preview and result differ:\n%s\n%s", a, b)
	}
	if cfg, err := config.Load(f.cfg); err != nil || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != "rate_limit" {
		t.Fatalf("handoff not on: %v", err)
	}
	code, out, _ = f.manage(t, "", "handoff", "on", "--json", "--yes")
	if err := json.Unmarshal([]byte(out), &again); code != exitOK || err != nil || again.Outcome != "unchanged" || len(again.Files) != 0 {
		t.Fatalf("%d %s", code, out)
	}
	// An absent key means rate_limit; turning handoff off writes it explicitly.
	lines := strings.Split(string(mustRead(t, f.cfg)), "\n")
	for i, l := range lines {
		if strings.Contains(l, "failover_on") {
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
	}
	if err := os.WriteFile(f.cfg, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.manage(t, "", "handoff", "off", "--yes"); code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	if cfg, err := config.Load(f.cfg); err != nil || len(cfg.Loop.Errors.FailoverOn) != 0 || !strings.Contains(string(mustRead(t, f.cfg)), "failover_on: []") {
		t.Fatalf("default handoff not turned off: %v", err)
	}
	// On a fallback, turning handoff off says the loop stays where it is.
	if _, _, errOut := f.manage(t, "", "handoff", "on", "--yes"); errOut != "" {
		t.Fatal(errOut)
	}
	if err := state.Save(cfg.Loop.StateDir, state.File{ActiveTarget: "second"}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := f.manage(t, "", "handoff", "off", "--dry-run"); code != exitOK || !strings.Contains(out, "The loop stays on second, where it is now. This does not move it\n  back; use keepgoing retarget primary for that.") || !strings.Contains(out, "in use now") {
		t.Fatalf("%d %s", code, out)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFallbacksRemove(t *testing.T) {
	f := newManageFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := &health.Registry{Candidates: map[string]*health.Entry{"second": {ProbesToday: 2}, "third": {ProbesToday: 1}}}
	if err := reg.Save(cfg.Loop.StateDir); err != nil {
		t.Fatal(err)
	}
	failures := filepath.Join(cfg.Loop.StateDir, "probe-failures", "second")
	if err := os.MkdirAll(failures, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failures, "example.out"), []byte("fictional\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, cfgBefore := f.files(t), mustRead(t, f.cfg)
	if code, out, _ := f.manage(t, "\n", "remove", "second"); code != exitOK || !strings.Contains(out, "Cancelled: nothing written.") || f.files(t) != before {
		t.Fatalf("%d %s", code, out)
	}
	code, out, errOut := f.manage(t, "yes\n", "remove", "second")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{"Remove fallback second from loop \"example-loop\" (nothing written yet)", "Handoff order afterwards", "Removed: second, which was at place 2.", "The saved check results for second are deleted", "probe-failures) is left in place", "health.json", "Remove this fallback? (yes/no) [no]", "Removed fallback second."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if got := f.ids(t); got != "first,third" {
		t.Fatalf("order %s", got)
	}
	text := string(mustRead(t, f.fallbacks))
	for _, want := range []string{"# header note\n", "# about first\n", "id: first # inline first\n", "# about third\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("fallbacks file lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "about second") || strings.Index(text, "# about third") > strings.Index(text, "id: third") {
		t.Errorf("the removed entry's comment stayed, or another moved:\n%s", text)
	}
	f.modes(t)
	after, err := health.Load(cfg.Loop.StateDir)
	if err != nil || after.Candidates["second"] != nil || after.Candidates["third"] == nil || after.Candidates["third"].ProbesToday != 1 {
		t.Fatalf("health: %v %+v", err, after.Candidates)
	}
	if _, err := os.Stat(filepath.Join(failures, "example.out")); err != nil {
		t.Fatalf("saved failure output was removed: %v", err)
	}
	if !bytes.Equal(cfgBefore, mustRead(t, f.cfg)) {
		t.Error("keepgoing.yaml was rewritten")
	}

	// Refusals leave everything as it is.
	if err := state.Save(cfg.Loop.StateDir, state.File{ActiveTarget: "third"}); err != nil {
		t.Fatal(err)
	}
	before = f.files(t)
	for name, tc := range map[string]struct{ id, want string }{
		"in use":  {"third", "The loop is running on \"third\" right now. Move it first with:\n    keepgoing retarget primary"},
		"unknown": {"absent", "No fallback has the id \"absent\". This loop has: first, third."},
	} {
		code, out, errOut := f.manage(t, "yes\n", "remove", tc.id)
		if code != exitUsage || out != "" || !strings.Contains(errOut, tc.want) || f.files(t) != before {
			t.Fatalf("%s: %d %s", name, code, errOut)
		}
	}
	if err := state.Save(cfg.Loop.StateDir, state.File{}); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.manage(t, "", "remove", "third", "--yes"); code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	before = f.files(t)
	code, out, errOut = f.manage(t, "", "remove", "first", "--yes")
	if code != exitUsage || out != "" || !strings.Contains(errOut, "only fallback") || !strings.Contains(errOut, "keepgoing fallbacks handoff off") || f.files(t) != before {
		t.Fatalf("last fallback: %d %s", code, errOut)
	}
}

func TestFallbacksMove(t *testing.T) {
	f := newManageFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := &health.Registry{Candidates: map[string]*health.Entry{"third": {ProbesToday: 1}}}
	if err := reg.Save(cfg.Loop.StateDir); err != nil {
		t.Fatal(err)
	}
	healthBefore := mustRead(t, filepath.Join(cfg.Loop.StateDir, health.FileName))
	cfgBefore := mustRead(t, f.cfg)
	code, out, errOut := f.manage(t, "yes\n", "move", "third", "--to", "1")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{"Move fallback third in loop \"example-loop\" (nothing written yet)", "<- was 3.", "Recorded health is kept, and the loop stays on its current target.", "Move this fallback? (yes/no) [no]", "Moved fallback third to place 1 of 3."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if got := f.ids(t); got != "third,first,second" {
		t.Fatalf("order %s", got)
	}
	text := string(mustRead(t, f.fallbacks))
	order := []string{"# header note", "# about third", "id: third", "# about first", "id: first # inline first", "# about second", "id: second"}
	last := -1
	for _, want := range order {
		at := strings.Index(text, want)
		if at <= last {
			t.Fatalf("%q is missing or out of place: comments must travel with their entry:\n%s", want, text)
		}
		last = at
	}
	f.modes(t)
	if !bytes.Equal(healthBefore, mustRead(t, filepath.Join(cfg.Loop.StateDir, health.FileName))) || !bytes.Equal(cfgBefore, mustRead(t, f.cfg)) {
		t.Fatal("move changed recorded health or keepgoing.yaml")
	}
	if now, err := config.Load(f.cfg); err != nil || now.FallbackSet.Redundancy != cfg.FallbackSet.Redundancy {
		t.Fatalf("rules changed: %v", err)
	}
	before := f.files(t)
	if code, out, _ := f.manage(t, "", "move", "third", "--to", "1"); code != exitOK || !strings.Contains(out, "already at place 1. Nothing was changed.") || f.files(t) != before {
		t.Fatalf("%d %s", code, out)
	}
	for name, args := range map[string][]string{"too far": {"third", "--to", "4"}, "zero": {"third", "--to", "0"}, "no place": {"third"}, "unknown": {"absent", "--to", "1"}} {
		if code, out, errOut := f.manage(t, "yes\n", "move", args...); code != exitUsage || out != "" || f.files(t) != before {
			t.Fatalf("%s: %d %s %s", name, code, out, errOut)
		}
	}
	var r fallbackEditReport
	code, out, _ = f.manage(t, "", "move", "second", "--to", "2", "--json", "--yes")
	if err := json.Unmarshal([]byte(out), &r); code != exitOK || err != nil || r.Kind != "fallback_move" || r.Outcome != "changed" || r.From != 3 || r.To != 2 || r.Health != "kept" || r.Order[1].ID != "second" {
		t.Fatalf("%d %s %v", code, out, err)
	}
}

func TestFallbacksManageGuards(t *testing.T) {
	commands := map[string][]string{"handoff": {"off"}, "remove": {"second"}, "move": {"second", "--to", "1"}}
	t.Run("usage", func(t *testing.T) {
		f := newManageFixture(t)
		before := f.files(t)
		for name, tc := range map[string]struct {
			sub  string
			args []string
			want string
		}{
			"handoff word":  {"handoff", []string{"maybe"}, "usage: keepgoing fallbacks handoff on|off"},
			"handoff none":  {"handoff", nil, "usage: keepgoing fallbacks handoff on|off"},
			"remove none":   {"remove", nil, "usage: keepgoing fallbacks remove ID"},
			"remove two":    {"remove", []string{"first", "second"}, "usage: keepgoing fallbacks remove ID"},
			"to elsewhere":  {"remove", []string{"first", "--to", "2"}, "--to applies only to fallbacks move"},
			"json alone":    {"handoff", []string{"off", "--json"}, "--json requires --yes or --dry-run"},
			"unknown flag":  {"move", []string{"first", "--sideways"}, "flag provided but not defined"},
			"id like flags": {"remove", []string{"first\u009b2J"}, "No fallback has the id \"first 2J\""},
		} {
			code, out, errOut := f.manage(t, "yes\n", tc.sub, tc.args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, tc.want) || f.files(t) != before {
				t.Fatalf("%s: %d %q %s", name, code, out, errOut)
			}
		}
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{"fallbacks", "remove", "--help"}, &out, &errOut); code != exitOK || !strings.Contains(errOut.String(), "Nothing is run: no agent, model or check.") {
			t.Fatalf("help: %d %s", code, errOut.String())
		}
		out.Reset()
		errOut.Reset()
		if code := run(context.Background(), []string{"fallbacks", "move", "third", "--to", "1", "--dry-run", "-c", f.cfg}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "Dry run: nothing written.") || f.files(t) != before {
			t.Fatalf("dispatch: %d %s %s", code, out.String(), errOut.String())
		}
	})
	t.Run("no fallbacks", func(t *testing.T) {
		t.Setenv("BB_THREAD_ID", "")
		f := newAddFixture(t)
		before := f.files(t)
		for sub, args := range commands {
			code, out, errOut := f.manage(t, "yes\n", sub, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "This loop has no fallbacks. Add one first with: keepgoing fallbacks add") || f.files(t) != before {
				t.Fatalf("%s: %d %s", sub, code, errOut)
			}
		}
	})
	t.Run("not the owning thread", func(t *testing.T) {
		f := newManageFixture(t)
		t.Setenv("BB_THREAD_ID", "thr_example_owner")
		var out, errOut bytes.Buffer
		if code := run(context.Background(), []string{"enable", "-c", f.cfg}, &out, &errOut); code != exitOK {
			t.Fatalf("enable: %s", errOut.String())
		}
		t.Setenv("BB_THREAD_ID", "thr_example_visitor")
		before := f.files(t)
		for sub, args := range commands {
			for _, mode := range []string{"--yes", "--dry-run"} {
				code, stdout, stderr := f.manage(t, "", sub, append(args, mode)...)
				if code != exitFailed || stdout != "" || !strings.Contains(stderr, "owned by BB thread thr_example_owner") || f.files(t) != before {
					t.Fatalf("%s %s: %d %q %s", sub, mode, code, stdout, stderr)
				}
			}
		}
	})
	t.Run("pending transfer", func(t *testing.T) {
		f := newManageFixture(t)
		cfg, err := config.Load(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := control.Save(cfg.Loop.StateDir, control.File{BBTarget: "thr_example_moved"}); err != nil {
			t.Fatal(err)
		}
		before := f.files(t)
		for sub, args := range commands {
			code, stdout, stderr := f.manage(t, "", sub, append(args, "--yes")...)
			if code != exitSkipped || stdout != "" || !strings.Contains(stderr, "nothing written") || f.files(t) != before {
				t.Fatalf("%s: %d %q %s", sub, code, stdout, stderr)
			}
		}
	})
	t.Run("symlinked config", func(t *testing.T) {
		f := newManageFixture(t)
		link := filepath.Join(f.root, "link.yaml")
		if err := os.Symlink(f.cfg, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		before := f.files(t)
		var out, errOut bytes.Buffer
		code := fallbacksManage(env{context.Background(), &out, &errOut}, "remove", []string{"second", "--yes", "-c", link}, strings.NewReader(""))
		if code != exitUsage || !strings.Contains(errOut.String(), "symbolic link") || f.files(t) != before {
			t.Fatalf("%d %s", code, errOut.String())
		}
	})
	t.Run("busy stale or failing write", func(t *testing.T) {
		f := newManageFixture(t)
		cfg, err := config.Load(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		remove, err := planFallbackRemove(cfg, "second")
		if err != nil {
			t.Fatal(err)
		}
		handoff, err := planFallbackHandoff(cfg, false)
		if err != nil {
			t.Fatal(err)
		}
		before := f.files(t)
		l, err := lock.Acquire(cfg.Loop.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if code, err := writeFallbackEdit(remove); code != exitSkipped || err == nil || f.files(t) != before {
			t.Fatalf("held lock: %d %v", code, err)
		}
		if err := l.Release(); err != nil {
			t.Fatal(err)
		}
		// The loop hands off to the fallback after the preview.
		if err := state.Save(cfg.Loop.StateDir, state.File{ActiveTarget: "second"}); err != nil {
			t.Fatal(err)
		}
		if code, err := writeFallbackEdit(remove); code != exitSkipped || err == nil || !strings.Contains(err.Error(), "since the preview") || f.files(t) != before {
			t.Fatalf("moved target: %d %v", code, err)
		}
		if err := state.Save(cfg.Loop.StateDir, state.File{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fallbackAddReplace = replaceFile })
		fallbackAddReplace = func(string, []byte) error { return errors.New("injected failure") }
		for name, edit := range map[string]*fallbackEdit{"remove": remove, "handoff": handoff} {
			if code, err := writeFallbackEdit(edit); code != exitFailed || err == nil || !strings.Contains(err.Error(), "nothing written") || f.files(t) != before {
				t.Fatalf("%s with a failing write: %d %v", name, code, err)
			}
		}
		fallbackAddReplace = replaceFile
		if err := os.WriteFile(f.fallbacks, append(mustRead(t, f.fallbacks), []byte("# edited elsewhere\n")...), 0o640); err != nil {
			t.Fatal(err)
		}
		before = f.files(t)
		if code, err := writeFallbackEdit(handoff); code != exitSkipped || err == nil || f.files(t) != before {
			t.Fatalf("changed source: %d %v", code, err)
		}
	})
}
