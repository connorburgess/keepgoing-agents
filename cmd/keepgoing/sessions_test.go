//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// secondLoop writes another loop beside the fixture's, with its own state
// directory, changing its configuration with edit, and returns its path.
func secondLoop(t *testing.T, f *fixture, name string, edit func(string) string) string {
	t.Helper()
	b, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(b), "name: example-loop", "name: "+name, 1)
	body = strings.Replace(body, "state_dir: state", "state_dir: "+filepath.Join(dir, "state"), 1)
	body = strings.Replace(body, "fallbacks: fallbacks.yaml", "fallbacks: "+filepath.Join(f.dir, "fallbacks.yaml"), 1)
	if edit != nil {
		body = edit(body)
	}
	path := filepath.Join(dir, "keepgoing.yaml")
	write(t, path, body, 0o600)
	return path
}

func runWith(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestEnableRefusesASecondLoopOnOneSession(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatalf("first enable: %d %s", code, errs)
	}
	other := secondLoop(t, f, "example-loop-b", nil)
	code, _, errs := runWith("enable", "-c", other)
	if code != exitFailed || !strings.Contains(errs, "loop example-loop (") || !strings.Contains(errs, "BB thread thr_primary") || !strings.Contains(errs, "--allow-second-loop") {
		t.Fatalf("second enable: %d %s", code, errs)
	}
	otherCfg, _ := config.Load(other)
	if ctl, _ := control.Load(otherCfg.Loop.StateDir); ctl.Enabled {
		t.Fatal("the refused loop was enabled")
	}
	// Asked for explicitly: enabled, with a warning naming the first.
	code, _, errs = runWith("enable", "-c", other, "--allow-second-loop")
	if code != exitOK || !strings.Contains(errs, "warning: loop example-loop (") || !strings.Contains(errs, "also supervises BB thread thr_primary") {
		t.Fatalf("allowed enable: %d %s", code, errs)
	}
	// Status and validate of either loop name the other.
	for _, c := range []struct{ cfg, other string }{{f.cfg, "example-loop-b"}, {other, "example-loop"}} {
		code, out, _ := runWith("status", "-c", c.cfg)
		if code != exitOK || !strings.Contains(out, "shared:    loop "+c.other+" (") {
			t.Fatalf("status of %s: %d %s", c.cfg, code, out)
		}
		code, _, errs := runWith("validate", "-c", c.cfg)
		if code != exitOK || !strings.Contains(errs, "warning: loop "+c.other+" (") {
			t.Fatalf("validate of %s: %d %s", c.cfg, code, errs)
		}
	}
	code, out, _ := runWith("status", "--json", "-c", f.cfg)
	var st struct {
		SharedWith []string `json:"sharedWith"`
	}
	// The two fixture loops share their fallbacks' threads too: each is
	// named, the primary first.
	if code != exitOK || json.Unmarshal([]byte(out), &st) != nil || len(st.SharedWith) != 3 || !strings.Contains(st.SharedWith[0], "also supervises BB thread thr_primary") ||
		!strings.Contains(strings.Join(st.SharedWith, "\n"), "fallback claude-other-account and this loop's fallback claude-other-account are the same BB thread thr_alt") {
		t.Fatalf("status json: %s", out)
	}
	// The flag belongs to enable, start and init only.
	if code, _, errs := runWith("pause", "-c", other, "--allow-second-loop"); code != exitUsage || !strings.Contains(errs, "only supported by enable, start and init") {
		t.Fatalf("pause --allow-second-loop: %d %s", code, errs)
	}
}

// A loop that was never registered is still found: enabling it recorded it
// in the session directory, outside every loop's own directory.
func TestAnUnregisteredLoopIsFoundThroughTheSessionRecord(t *testing.T) {
	f := newFixture(t)
	if code, _, _ := f.run("enable"); code != exitOK {
		t.Fatal("enable")
	}
	entries, err := os.ReadDir(os.Getenv(registryDirEnv))
	if err == nil && len(entries) > 0 {
		t.Fatalf("the fixture loop is registered: %v", entries)
	}
	other := secondLoop(t, f, "example-loop-b", nil)
	if code, _, errs := runWith("enable", "-c", other); code != exitFailed || !strings.Contains(errs, "loop example-loop (") {
		t.Fatalf("%d %s", code, errs)
	}
	// A loop that is disabled, or whose configuration no longer names the
	// session, no longer counts.
	if code, _, _ := f.run("disable"); code != exitOK {
		t.Fatal("disable")
	}
	if code, _, errs := runWith("enable", "-c", other); code != exitOK || strings.Contains(errs, "example-loop (") {
		t.Fatalf("after the first was disabled: %d %s", code, errs)
	}
	third := secondLoop(t, f, "example-loop-c", nil)
	b, _ := os.ReadFile(other)
	write(t, other, strings.Replace(string(b), "thread: thr_primary", "thread: thr_elsewhere", 1), 0o600)
	if code, _, errs := runWith("enable", "-c", third); code != exitOK || strings.Contains(errs, "already supervises") || strings.Contains(errs, "example-loop-b ("+other+") also supervises") {
		t.Fatalf("after the second moved to another thread: %d %s", code, errs)
	}
}

func TestRegisteredLoopsAreFoundAndStartAndInitRefuse(t *testing.T) {
	f := newFixture(t)
	if code, _, _ := f.run("enable"); code != exitOK {
		t.Fatal("enable")
	}
	// Registered and enabled before this build kept session records: the
	// registry finds it.
	if err := os.RemoveAll(os.Getenv(session.DirEnv)); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runWith("registry", "add", "-c", f.cfg); code != exitOK {
		t.Fatalf("registry add: %d %s", code, errs)
	}
	other := secondLoop(t, f, "example-loop-b", nil)
	code, _, errs := runWith("start", "-c", other)
	if code != exitFailed || !strings.Contains(errs, "start: loop example-loop (") || !strings.Contains(errs, "--allow-second-loop to start this one") {
		t.Fatalf("start: %d %s", code, errs)
	}
	otherCfg, _ := config.Load(other)
	if ctl, _ := control.Load(otherCfg.Loop.StateDir); ctl.Enabled {
		t.Fatal("the refused loop was started")
	}
	var out, ierr bytes.Buffer
	sf := newSetupFixture(t)
	project := sf.project
	code = setupLoop(env{context.Background(), &out, &ierr}, []string{"--non-interactive", "--dry-run", "--name", "example-loop-c", "--agent", "bb", "--thread", "thr_primary", "--provider", "claude-code", "--project", project, "--task", "Fictional task", "--directory", filepath.Join(f.dir, "setup-c")}, strings.NewReader(""), sf.lookPath)
	if code != exitFailed || !strings.Contains(ierr.String(), "init: loop example-loop (") {
		t.Fatalf("init: %d %s %s", code, out.String(), ierr.String())
	}
	out.Reset()
	ierr.Reset()
	code = setupLoop(env{context.Background(), &out, &ierr}, []string{"--non-interactive", "--dry-run", "--allow-second-loop", "--name", "example-loop-c", "--agent", "bb", "--thread", "thr_primary", "--provider", "claude-code", "--project", project, "--task", "Fictional task", "--directory", filepath.Join(f.dir, "setup-c")}, strings.NewReader(""), sf.lookPath)
	if code != exitOK || !strings.Contains(ierr.String(), "warning: loop example-loop (") {
		t.Fatalf("init allowed: %d %s %s", code, out.String(), ierr.String())
	}
}

// Two headless loops in one checkout are two conversations, so neither
// refuses the other; each is warned about the other.
func TestHeadlessLoopsInOneDirectoryAreWarnedNotRefused(t *testing.T) {
	f := newFixture(t)
	work := filepath.Join(f.dir, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	headless := func(adapter string) func(string) string {
		return func(body string) string {
			body = strings.Replace(body, "  adapter: bb\n", "  adapter: "+adapter+"\n", 1)
			body = strings.Replace(body, "  provider: claude-code\n", "", 1)
			body = strings.Replace(body, "  group: claude-account-a\n", "", 1)
			lines := strings.Split(body, "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, "  bb: {thread: thr_primary") {
					lines[i] = "  " + adapter + ": {workdir: " + work + ", cli: " + filepath.Join(f.dir, "bin", adapter) + "}"
				}
				if strings.HasPrefix(l, "  fallbacks:") {
					lines[i] = ""
				}
			}
			return strings.Join(lines, "\n") + "  progress: {repositories: [" + work + "]}\n"
		}
	}
	claude := secondLoop(t, f, "example-claude", headless("claude"))
	codex := secondLoop(t, f, "example-codex", headless("codex"))
	if code, _, errs := runWith("enable", "-c", claude); code != exitOK {
		t.Fatalf("claude: %d %s", code, errs)
	}
	code, _, errs := runWith("enable", "-c", codex)
	if code != exitOK || !strings.Contains(errs, "warning: loop example-claude (") || !strings.Contains(errs, "its current agent works in the same directory") || !strings.Contains(errs, "it tracks the same repository") {
		t.Fatalf("codex: %d %s", code, errs)
	}
	// The same CLI in the same directory is one session: refused.
	claudeTwo := secondLoop(t, f, "example-claude-two", headless("claude"))
	if code, _, errs := runWith("enable", "-c", claudeTwo); code != exitFailed || !strings.Contains(errs, "claude session in ") {
		t.Fatalf("second claude: %d %s", code, errs)
	}
}

// When the variables move the registry or the claims, doctor says where.
func TestDoctorNamesTheRegistryAndClaimDirectoriesWhenMoved(t *testing.T) {
	f := newFixture(t)
	var out, errs bytes.Buffer
	doctor(env{context.Background(), &out, &errs}, []string{"--json", "-c", f.cfg}, doctorExec)
	var r struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	found := map[string]string{}
	for _, c := range r.Checks {
		found[c.ID] = c.Message
	}
	if !strings.Contains(found["registry.directory"], os.Getenv(registryDirEnv)) || !strings.Contains(found["session.directory"], os.Getenv(session.DirEnv)) {
		t.Fatalf("%v", found)
	}
	// Each cause is named as it is, with its own fix.
	check := func(want, fix string) {
		t.Helper()
		out.Reset()
		doctor(env{context.Background(), &out, &errs}, []string{"--json", "-c", f.cfg}, doctorExec)
		var r struct {
			Checks []doctorCheck `json:"checks"`
		}
		found := false
		if json.Unmarshal(out.Bytes(), &r) == nil {
			for _, c := range r.Checks {
				found = found || (c.ID == "session.directory" && c.Status == "error" && strings.Contains(c.Message, want) && strings.Contains(c.Fix, fix))
			}
		}
		if !found {
			t.Fatalf("doctor did not say %q / %q: %s", want, fix, out.String())
		}
		code, text, _ := f.run("status")
		if code != exitOK || !strings.Contains(text, want) || !strings.Contains(text, fix) {
			t.Fatalf("status did not say %q / %q: %d %s", want, fix, code, text)
		}
	}
	// No user state directory: checks run unprotected.
	t.Setenv(session.DirEnv, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	check("HOME and XDG_STATE_HOME are not set", "set HOME, or "+session.DirEnv)
	// Something planted at the path: not "set HOME".
	planted := filepath.Join(f.dir, "planted")
	write(t, planted, "", 0o600)
	t.Setenv(session.DirEnv, planted)
	check(planted+" is not a directory", "remove "+planted)
	t.Setenv(registryDirEnv, "")
	t.Setenv(session.DirEnv, "")
	t.Setenv("HOME", f.dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(f.dir, "state-home"))
	out.Reset()
	doctor(env{context.Background(), &out, &errs}, []string{"--json", "-c", f.cfg}, doctorExec)
	if strings.Contains(out.String(), "registry.directory") || strings.Contains(out.String(), "session.directory") {
		t.Fatal("named a directory that was not moved")
	}
}

// init --json and start --json refuse with a code and the other loop's name
// only: never its path, so a program can show it as it is.
func TestJSONRefusalsNameTheOtherLoopOnly(t *testing.T) {
	f := newFixture(t)
	if code, _, _ := f.run("enable"); code != exitOK {
		t.Fatal("enable")
	}
	want := `{"schemaVersion":1,"outcome":"refused","refusal":{"code":"second_loop_on_session","otherLoop":"example-loop"}}` + "\n"
	sf := newSetupFixture(t)
	project := sf.project
	var out, errs bytes.Buffer
	code := setupLoop(env{context.Background(), &out, &errs}, []string{"--non-interactive", "--json", "--name", "example-loop-c", "--agent", "bb", "--thread", "thr_primary", "--provider", "claude-code", "--project", project, "--task", "Fictional task", "--directory", filepath.Join(f.dir, "setup-c")}, strings.NewReader(""), sf.lookPath)
	if code == exitOK || out.String() != want || !strings.Contains(errs.String(), "init: loop example-loop (") {
		t.Fatalf("init --json: %d %q %s", code, out.String(), errs.String())
	}
	if _, err := os.Lstat(filepath.Join(f.dir, "setup-c")); !os.IsNotExist(err) {
		t.Fatal("a refused init wrote files")
	}
	other := secondLoop(t, f, "example-loop-b", nil)
	out.Reset()
	errs.Reset()
	code = startCommand(env{context.Background(), &out, &errs}, []string{"--background", "--json", "-c", other}, nil)
	if code == exitOK || out.String() != want || !strings.Contains(errs.String(), "start: loop example-loop (") {
		t.Fatalf("start --json: %d %q %s", code, out.String(), errs.String())
	}
	if strings.Contains(out.String(), f.dir) {
		t.Fatal("a path is in the JSON")
	}
	// --json goes with --background; other failures are an object too.
	if code := startCommand(env{context.Background(), &out, &errs}, []string{"--json", "-c", other}, nil); code != exitUsage {
		t.Fatalf("start --json in the foreground: %d", code)
	}
	out.Reset()
	if code := startCommand(env{context.Background(), &out, &errs}, []string{"--background", "--json", "-c", filepath.Join(f.dir, "missing.yaml")}, nil); code != exitUsage || strings.TrimSpace(out.String()) != `{"schemaVersion":1,"outcome":"failed","exitCode":2}` {
		t.Fatalf("start --json, no configuration: %d %q", code, out.String())
	}
}

// The setup result says whether start accepts --background --json, so a
// program can decide from that field alone, in a preview and on creation.
func TestInitJSONAdvertisesStartJSON(t *testing.T) {
	f := newFixture(t)
	for _, dryRun := range []bool{true, false} {
		sf := newSetupFixture(t)
		project := sf.project
		args := []string{"--non-interactive", "--json", "--name", "example-loop-d", "--agent", "bb", "--thread", "thr_example_d", "--provider", "claude-code", "--project", project, "--task", "Fictional task", "--directory", filepath.Join(f.dir, "setup-d", map[bool]string{true: "preview", false: "created"}[dryRun])}
		if dryRun {
			args = append(args, "--dry-run")
		}
		var out, errs bytes.Buffer
		if code := setupLoop(env{context.Background(), &out, &errs}, args, strings.NewReader(""), sf.lookPath); code != exitOK {
			t.Fatalf("init: %d %s", code, errs.String())
		}
		var r map[string]any
		if err := json.Unmarshal(out.Bytes(), &r); err != nil || r["startJson"] != true || r["schemaVersion"] != float64(1) {
			t.Fatalf("dry run %v: %v %s", dryRun, err, out.String())
		}
	}
}

// Stop wins: a daemon whose stop met another loop's claim tries again at the
// next control poll, not after a whole interval.
func TestDaemonRetriesASkippedStopAtTheControlPoll(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	dir, _, err := session.Dir(true)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := session.Take(dir, session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "example-loop-b", PID: os.Getpid(), Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()
	f.run("stop")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	var out, errb safeBuffer
	go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, &out, &errb) }()
	waitFor(t, func() bool { return strings.Contains(errb.String(), "being checked by loop example-loop-b") }, "the skipped stop")
	if strings.Contains(f.read("calls"), "thread stop thr_primary") {
		t.Fatal("stopped while another loop's check held the session")
	}
	// The other loop's check ends; the interval is 30 s and the poll 100 ms.
	released := time.Now()
	claim.Release()
	waitFor(t, func() bool { return strings.Contains(f.read("calls"), "thread stop thr_primary") }, "stop")
	if d := time.Since(released); d > 5*time.Second {
		t.Fatalf("the stop took %v after the claim ended", d)
	}
	cancel()
	<-done
}
