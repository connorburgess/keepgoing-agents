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

// records is every runtime record a fallback command may touch, so a
// refusal can be shown to have left all of them alone.
func (f addFixture) records(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(f.destination, "state")
	var b strings.Builder
	b.WriteString(f.files(t))
	for _, name := range []string{health.FileName, state.FileName} {
		raw, err := os.ReadFile(filepath.Join(stateDir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		b.WriteString(name + "\n" + string(raw) + "\n")
	}
	for _, dir := range []string{"targets", "removed-targets"} {
		entries, _ := os.ReadDir(filepath.Join(stateDir, dir))
		for _, e := range entries {
			name := e.Name()
			if dir == "removed-targets" { // timestamped
				name = name[:strings.LastIndex(name, "-")]
			}
			b.WriteString(dir + "/" + name + "\n")
		}
	}
	return b.String()
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
	if err := state.Save(cfg.Loop.StateDir, state.File{HandoffBriefs: map[string]state.BriefRecord{"second": {Failed: 2}, "third": {Failed: 1}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"second", "third"} {
		if err := os.MkdirAll(filepath.Join(cfg.Loop.StateDir, "targets", id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Loop.StateDir, "targets", id, "session"), []byte("fictional-session\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	failures := filepath.Join(cfg.Loop.StateDir, "probe-failures", "second")
	if err := os.MkdirAll(failures, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failures, "example.out"), []byte("fictional\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, cfgBefore := f.records(t), mustRead(t, f.cfg)
	if code, out, _ := f.manage(t, "\n", "remove", "second"); code != exitOK || !strings.Contains(out, "Cancelled: nothing written.") || f.records(t) != before {
		t.Fatalf("%d %s", code, out)
	}
	code, out, errOut := f.manage(t, "yes\n", "remove", "second")
	if code != exitOK {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{"Remove fallback second from loop \"example-loop\" (nothing written yet)", "Handoff order afterwards", "Removed: second, which was at place 2.", "Comment lines directly above it are removed with it:\n    # about second", "Records of second", "deleted   saved check results (health.json)", "deleted   count of rejected handoff messages (state.json)", "archived  saved session and turn files, moved to removed-targets/", "kept      saved output from failed checks (probe-failures/), if any", "starts a new session with no check results", "Remove this fallback? (yes/no) [no]", "Removed fallback second."} {
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
	// The session a later fallback with this id would resume is moved aside,
	// and its rejected-handoff count is cleared; other targets keep theirs.
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, "targets", "second")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed fallback's session directory is still in place: %v", err)
	}
	archived, _ := filepath.Glob(filepath.Join(cfg.Loop.StateDir, "removed-targets", "second-*", "session"))
	if len(archived) != 1 || string(mustRead(t, archived[0])) != "fictional-session\n" {
		t.Fatalf("session was not archived intact: %v", archived)
	}
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, "targets", "third", "session")); err != nil {
		t.Fatalf("another target's session was touched: %v", err)
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil || len(st.HandoffBriefs) != 1 || st.HandoffBriefs["third"].Failed != 1 {
		t.Fatalf("handoff counts: %v %+v", err, st.HandoffBriefs)
	}
	if !bytes.Equal(cfgBefore, mustRead(t, f.cfg)) {
		t.Error("keepgoing.yaml was rewritten")
	}

	// Refusals leave everything as it is.
	if err := state.Save(cfg.Loop.StateDir, state.File{ActiveTarget: "third"}); err != nil {
		t.Fatal(err)
	}
	before = f.records(t)
	for name, tc := range map[string]struct{ id, want string }{
		"in use":  {"third", "The loop is running on \"third\" right now. Move it first with:\n    keepgoing retarget primary"},
		"unknown": {"absent", "No fallback has the id \"absent\". This loop has: first, third."},
	} {
		code, out, errOut := f.manage(t, "yes\n", "remove", tc.id)
		if code != exitUsage || out != "" || !strings.Contains(errOut, tc.want) || f.records(t) != before {
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
		for name, ctl := range map[string]control.File{
			"pending transfer record": {AwaitingUser: true, PendingTransfer: &control.PendingBBTransfer{}},
			"moved BB target":         {BBTarget: "thr_example_moved"},
		} {
			if err := control.Save(cfg.Loop.StateDir, ctl); err != nil {
				t.Fatal(err)
			}
			before := f.records(t)
			for sub, args := range commands {
				code, stdout, stderr := f.manage(t, "", sub, append(args, "--yes")...)
				if code != exitSkipped || stdout != "" || !strings.Contains(stderr, "nothing written") || f.records(t) != before {
					t.Fatalf("%s, %s: %d %q %s", name, sub, code, stdout, stderr)
				}
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

func TestFallbacksHandoffKeepsOtherClassesAndComments(t *testing.T) {
	set := func(t *testing.T, f addFixture, value string) {
		t.Helper()
		lines := strings.Split(string(mustRead(t, f.cfg)), "\n")
		for i, l := range lines {
			if at := strings.Index(l, "failover_on"); at >= 0 {
				lines[i] = l[:at] + value
				if strings.HasSuffix(strings.TrimSpace(lines[i+1]), "- rate_limit") {
					lines = append(lines[:i+1], lines[i+2:]...)
				}
				break
			}
		}
		if err := os.WriteFile(f.cfg, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct{ before, after string }{
		"one other class":   {"[overloaded]", "overloaded,rate_limit"},
		"two other classes": {"[auth, network]", "auth,network,rate_limit"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManageFixture(t)
			set(t, f, "failover_on: "+tc.before+" # keep this note")
			var r fallbackEditReport
			code, out, errOut := f.manage(t, "", "handoff", "on", "--json", "--dry-run")
			if err := json.Unmarshal([]byte(out), &r); code != exitOK || err != nil || r.Outcome != "preview" || !r.RateLimitAdded || strings.Join(r.Handoff.FailoverOn, ",") != tc.after || !r.Handoff.FailoverOnWritten {
				t.Fatalf("%d %s %s", code, out, errOut)
			}
			code, out, errOut = f.manage(t, "yes\n", "handoff", "on")
			if code != exitOK || !strings.Contains(out, "but not for rate limits") || !strings.Contains(out, "Rate limits are added to the error types that already hand off") || !strings.Contains(out, "Add rate limits to automatic handoff? (yes/no) [no]") {
				t.Fatalf("%d %s\n%s", code, errOut, out)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil || strings.Join(cfg.Loop.Errors.FailoverOn, ",") != tc.after {
				t.Fatalf("%v %v", err, cfg.Loop.Errors.FailoverOn)
			}
			if raw := string(mustRead(t, f.cfg)); !strings.Contains(raw, "# keep this note") || !strings.HasPrefix(raw, "# config note\n") {
				t.Fatalf("a comment was dropped:\n%s", raw)
			}
			if code, out, _ := f.manage(t, "", "handoff", "on"); code != exitOK || !strings.Contains(out, "already on for: "+strings.ReplaceAll(tc.after, ",", ", ")+". Nothing was changed.") {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
	// Turning a custom list off says it will not come back, and keeps the comment.
	f := newManageFixture(t)
	set(t, f, "failover_on: [rate_limit, overloaded] # keep this note")
	listBefore := string(mustRead(t, f.cfg))
	code, out, errOut := f.manage(t, "yes\n", "handoff", "off")
	if code != exitOK || !strings.Contains(out, "The current list (rate_limit, overloaded) is not remembered: turning handoff") || !strings.Contains(out, "restores rate limits only.") {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if raw := string(mustRead(t, f.cfg)); raw != strings.Replace(listBefore, "failover_on: [rate_limit, overloaded] # keep this note", "failover_on: [] # keep this note", 1) {
		t.Fatalf("more than the list changed:\n%s", raw)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 80 && !strings.Contains(line, f.root) {
			t.Errorf("line wider than 80 columns: %q", line)
		}
	}
}

// Only a comment block directly above an entry belongs to it. Headings and
// notes set apart by a blank line are layout and stay where they are, and
// every byte outside the entry's own lines is left as it was.
func TestFallbacksRemoveAndMoveLeaveDetachedCommentsAndLayout(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		name := map[string]string{"\n": "LF", "\r\n": "CRLF"}[eol]
		t.Run(name, func(t *testing.T) {
			f := newManageFixture(t)
			entry := func(id, group string) []string {
				return []string{"  - id: " + id, "    adapter: claude", "    group: " + group, "    claude: {workdir: " + f.project + ", model: example}", "    probe: {kind: claude}"}
			}
			head := []string{"# header note", "version: 1", "", "candidates:", "  # ---- Claude accounts ----", ""}
			first := append([]string{"  # about first"}, entry("first", "pool-a")...)
			gapA := []string{"  # below first, set apart", "", "  # ---- more accounts ----", ""}
			second := entry("second", "pool-b")
			gapB := []string{""}
			third := append([]string{"  # about third", "  # second line about third"}, entry("third", "pool-c")...)
			tail := []string{"", "# closing note", "redundancy: {min_healthy: 1, span: group}", ""}
			join := func(parts ...[]string) string {
				var all []string
				for _, p := range parts {
					all = append(all, p...)
				}
				return strings.Join(all, eol)
			}
			original := join(head, first, gapA, second, gapB, third, tail)
			reset := func() {
				if err := os.WriteFile(f.fallbacks, []byte(original), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			for name, tc := range map[string]struct {
				sub  string
				args []string
				want string
				says string
			}{
				"remove first":    {"remove", []string{"first"}, join(head, gapA, second, gapB, third, tail), "removed with it:\n    # about first"},
				"remove second":   {"remove", []string{"second"}, join(head, first, gapA, gapB, third, tail), ""},
				"remove third":    {"remove", []string{"third"}, join(head, first, gapA, second, gapB, tail), "# second line about third"},
				"move first last": {"move", []string{"first", "--to", "3"}, join(head, gapA, second, gapB, third, first, tail), "move with it:\n    # about first"},
				"move third up":   {"move", []string{"third", "--to", "1"}, join(head, third, first, gapA, second, gapB, tail), "move with it:\n    # about third\n    # second line about third"},
				"move second up":  {"move", []string{"second", "--to", "1"}, join(head, second, first, gapA, gapB, third, tail), ""},
			} {
				reset()
				code, out, errOut := f.manage(t, "", tc.sub, append(tc.args, "--yes")...)
				if code != exitOK {
					t.Fatalf("%s: %d %s", name, code, errOut)
				}
				if got := string(mustRead(t, f.fallbacks)); got != tc.want {
					t.Errorf("%s: file differs from the original with only the entry's lines cut or pasted:\n%q\nwant\n%q", name, got, tc.want)
				}
				if tc.says != "" && !strings.Contains(out, tc.says) || tc.says == "" && strings.Contains(out, "with it:") {
					t.Errorf("%s: preview does not list exactly the comment lines that travel:\n%s", name, out)
				}
				if strings.Contains(out, "Claude accounts") || strings.Contains(out, "set apart") {
					t.Errorf("%s: preview lists a detached comment:\n%s", name, out)
				}
			}
		})
	}
	t.Run("layouts that cannot be cut by line are refused", func(t *testing.T) {
		f := newManageFixture(t)
		flow := "version: 1\ncandidates: [{id: first, adapter: claude, group: pool-a, claude: {workdir: " + f.project + "}, probe: {kind: claude}}, {id: second, adapter: claude, group: pool-b, claude: {workdir: " + f.project + "}, probe: {kind: claude}}]\n"
		if err := os.WriteFile(f.fallbacks, []byte(flow), 0o640); err != nil {
			t.Fatal(err)
		}
		before := f.records(t)
		for sub, args := range map[string][]string{"remove": {"first"}, "move": {"first", "--to", "2"}} {
			code, out, errOut := f.manage(t, "", sub, append(args, "--yes")...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "edit it by hand") || f.records(t) != before {
				t.Fatalf("%s: %d %s", sub, code, errOut)
			}
		}
	})
}

func TestFallbacksRemoveDecidesRecordsUnderTheLockAndCanBeFinished(t *testing.T) {
	f := newManageFixture(t)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is recorded at the preview; a check records the fallback before the answer.
	plan, err := planFallbackRemove(cfg, "second")
	if err != nil || plan.leftovers.any() {
		t.Fatalf("%v %+v", err, plan.leftovers)
	}
	reg := &health.Registry{Candidates: map[string]*health.Entry{"second": {ProbesToday: 1}, "third": {ProbesToday: 1}}}
	if err := reg.Save(cfg.Loop.StateDir); err != nil {
		t.Fatal(err)
	}
	if code, err := writeFallbackEdit(plan); code != exitOK || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	after, err := health.Load(cfg.Loop.StateDir)
	if err != nil || after.Candidates["second"] != nil || after.Candidates["third"] == nil {
		t.Fatalf("a record written after the preview survived the removal: %v %+v", err, after.Candidates)
	}

	// The health write fails after the file is written: the error says so, and
	// running the command again finishes the job.
	if err := os.MkdirAll(filepath.Join(cfg.Loop.StateDir, "targets", "third"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fallbackHealthSave = func(r *health.Registry, dir string) error { return r.Save(dir) } })
	fallbackHealthSave = func(*health.Registry, string) error { return errors.New("injected failure") }
	code, out, errOut := f.manage(t, "", "remove", "third", "--yes")
	if code != exitFailed || !strings.Contains(errOut, "the fallback was removed, but its saved check results could not be deleted. Run the same command again to finish; do not reuse the id third until it succeeds") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if got := f.ids(t); got != "first" {
		t.Fatalf("order %s", got)
	}
	if after, _ := health.Load(cfg.Loop.StateDir); after.Candidates["third"] == nil {
		t.Fatal("fixture: the record should still be there")
	}
	fallbackHealthSave = func(r *health.Registry, dir string) error { return r.Save(dir) }
	filesBefore := f.files(t)
	code, out, errOut = f.manage(t, "yes\n", "remove", "third")
	if code != exitOK || !strings.Contains(out, "third is no longer a fallback of loop \"example-loop\", but left records behind") || !strings.Contains(out, "Clear these leftover records? (yes/no) [no]") || !strings.Contains(out, "Cleared the leftover records of third.") {
		t.Fatalf("%d %s\n%s", code, errOut, out)
	}
	if after, _ := health.Load(cfg.Loop.StateDir); after.Candidates["third"] != nil {
		t.Fatal("retry did not clear the record")
	}
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, "targets", "third")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retry did not move the session aside")
	}
	if f.files(t) != filesBefore {
		t.Fatal("a cleanup rewrote the YAML files")
	}
	// With nothing left, the id is simply unknown again; odd ids never reach the filesystem.
	for _, id := range []string{"third", "../first", "primary"} {
		before := f.records(t)
		if code, out, errOut := f.manage(t, "", "remove", id, "--yes"); code != exitUsage || out != "" || !strings.Contains(errOut, "No fallback has the id") || f.records(t) != before {
			t.Fatalf("%s: %d %s", id, code, errOut)
		}
	}
	// An unreadable state file is reported as that, not as a handoff.
	if err := os.WriteFile(filepath.Join(cfg.Loop.StateDir, state.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.manage(t, "", "handoff", "off", "--yes"); code != exitUsage || !strings.Contains(errOut, "saved state is unreadable") {
		t.Fatalf("%d %s", code, errOut)
	}
}
