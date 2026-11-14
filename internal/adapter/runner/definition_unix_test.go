//go:build unix

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// These run the example definition through the journeys the built-in
// runners' fixtures run (runner_test.go), with the same stand-in helpers: a
// script in place of the agent, a real detached turn, and the files a turn
// leaves. Each test names the built-in fixture it follows.

func exampleRunner(t *testing.T, script string, edit func(*config.Custom)) (*Headless, string) {
	t.Helper()
	dir := t.TempDir()
	h, ok := New(exampleAgent(t, fakeCLI(t, script), dir, edit), filepath.Join(dir, "target"))
	if !ok {
		t.Fatal("no runner for the example definition")
	}
	return h, dir
}

func exampleLine(status, extra string) string {
	return `{"type":"result","status":"` + status + `","session":"` + exampleSessionToken + `"` + extra + `}`
}

// Follows TestClaudeTurnLifecycle.
func TestDefinedAgentTurnLifecycle(t *testing.T) {
	var dir string
	h, dir := exampleRunner(t, `printf '%s\n' "$@" > "$PWD/args"; cat > "$PWD/got-prompt"; sleep 0.3; echo '`+exampleLine("ok", "")+`'`, nil)
	ctx := context.Background()
	if obs, _ := h.Observe(ctx); obs.State != decide.Idle {
		t.Fatalf("never run should be idle: %+v", obs)
	}
	if err := h.Send(ctx, "first turn", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	if obs, _ := h.Observe(ctx); obs.State != decide.Active {
		t.Fatalf("expected active: %+v", obs)
	}
	if _, ok := h.Send(ctx, "x", adapter.ModeQueue).(adapter.ErrUnsupported); !ok {
		t.Error("send while running should be unsupported")
	}
	waitPhase(t, h.dir, Finished)
	if obs, _ := h.Observe(ctx); obs.State != decide.Idle || obs.Evidence.Source != "runner_exit" {
		t.Fatalf("expected idle after success: %+v", obs)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "got-prompt")); string(got) != "first turn" {
		t.Errorf("prompt = %q", got)
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); string(args) != "run\n--json\n--model\nexample-model\n" {
		t.Errorf("first turn args = %q", args)
	}
	// Second turn resumes the saved session.
	if err := h.Send(ctx, "second turn", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); string(args) != "run\n--json\n--model\nexample-model\n--session\n"+exampleSessionToken+"\n" {
		t.Errorf("second turn args = %q", args)
	}
	// The prompt never reaches argv or the turn record.
	record, _ := os.ReadFile(filepath.Join(dir, "target", "run.json"))
	if strings.Contains(string(record), "second turn") {
		t.Errorf("prompt in the turn record: %s", record)
	}
}

// Follows TestClaudeErrorClassification.
func TestDefinedAgentErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		out, exit string
		want      decide.ErrorClass
	}{
		{exampleLine("failed", `,"error":{"code":"quota_exceeded","status":429}`), "1", decide.ErrRateLimit},
		{exampleLine("failed", `,"error":{"code":"unauthorized"}`), "1", decide.ErrAuth},
		{exampleLine("failed", `,"error":{"code":"overloaded"}`), "1", decide.ErrOverloaded},
		{exampleLine("failed", `,"message":"You have hit your usage limit (429)"`), "1", decide.ErrUnknown},
		{`usage limit reached`, "1", decide.ErrUnknown},
		{``, "75", decide.ErrNetwork},
	} {
		h, _ := exampleRunner(t, "cat > /dev/null; echo '"+tc.out+"'; exit "+tc.exit, func(c *config.Custom) { c.Definition.Limits = nil })
		if err := h.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, h.dir, Finished)
		if obs, _ := h.Observe(context.Background()); obs.State != decide.Error || obs.ErrorClass != tc.want {
			t.Errorf("%s exit %s: %+v", tc.out, tc.exit, obs)
		}
	}
}

// Follows TestStopKillsTurnAndCrashIsUnknown, and adds that an agent whose
// definition does not declare it can be stopped is not signalled.
func TestDefinedAgentStop(t *testing.T) {
	h, _ := exampleRunner(t, "sleep 30", nil)
	if err := h.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Running)
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := h.dir.Status(); st.Phase != Running {
			if obs, _ := h.Observe(context.Background()); obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
				t.Errorf("after stop: %+v", obs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn still running after stop")
		}
		time.Sleep(20 * time.Millisecond)
	}

	undeclared, _ := exampleRunner(t, "sleep 5", func(c *config.Custom) { c.Definition.Capabilities.CanBeStopped = false })
	if err := undeclared.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, undeclared.dir, Running)
	if _, ok := undeclared.Stop(context.Background()).(adapter.ErrUnsupported); !ok {
		t.Error("stopped an agent that does not declare it can be stopped")
	}
	if st, _ := undeclared.dir.Status(); st.Phase != Running {
		t.Errorf("the turn was ended: %s", st.Phase)
	}
	// Clean up through the wrapper's own request, which the test owns.
	undeclared.dir.Stop()
}

// Follows TestAccountEnvironmentOnFreshAndResumedTurns.
func TestDefinedAgentEnvironmentOnFreshAndResumedTurns(t *testing.T) {
	t.Setenv("EXAMPLE_HOME", "/fictional/default")
	t.Setenv("KEEPGOING_TEST_INHERITED", "parent")
	accountPath := "/fictional/account-b with spaces"
	h, dir := exampleRunner(t, `test "$EXAMPLE_HOME" = '`+accountPath+`' || exit 3
 test "$KEEPGOING_TEST_INHERITED" = parent || exit 4
 test -z "$KEEPGOING_RUN_TURN_STDIN" || exit 5
 cat > /dev/null
 printf '%s\n' "$@" > "$PWD/args"
 echo '`+exampleLine("ok", "")+`'`, func(c *config.Custom) { c.Env = map[string]string{"EXAMPLE_HOME": accountPath} })
	for turn := 0; turn < 2; turn++ {
		if err := h.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
			t.Fatal(err)
		}
		if st := waitPhase(t, h.dir, Finished); st.ExitCode != 0 {
			t.Fatalf("turn %d used the wrong environment: exit %d", turn, st.ExitCode)
		}
		if obs, err := h.Observe(context.Background()); err != nil || obs.State != decide.Idle {
			t.Fatalf("observation: %+v %v", obs, err)
		}
	}
	if args, err := os.ReadFile(filepath.Join(dir, "args")); err != nil || !strings.Contains(string(args), "--session\n"+exampleSessionToken) {
		t.Fatalf("second turn did not resume: %s %v", args, err)
	}
	if record, err := os.ReadFile(filepath.Join(dir, "target", "run.json")); err != nil || strings.Contains(string(record), accountPath) {
		t.Fatalf("environment leaked into turn metadata: %s %v", record, err)
	}
	if os.Getenv("EXAMPLE_HOME") != "/fictional/default" {
		t.Fatal("parent environment changed")
	}
}

// Follows TestCodexLimitCheckUsesTurnAccount and
// TestCodexTurnLimitClassifiedFromAppServerNotProse: an unclassified failure
// is asked about through the definition's lookup, in the turn's environment,
// and only then.
func TestDefinedAgentLimitLookup(t *testing.T) {
	script := `test "$EXAMPLE_HOME" = /fictional/account-b || exit 9
if [ "$1" = limits ]; then
  echo x >> "$PWD/lookups"
  echo '{"blocked":%s,"resets_at":"2026-01-01T12:00:00Z"}'
else
  cat > /dev/null
  echo '%s'
  exit 1
fi`
	account := func(c *config.Custom) { c.Env = map[string]string{"EXAMPLE_HOME": "/fictional/account-b"} }
	run := func(blocked, line string) (decide.Observation, int) {
		t.Helper()
		h, dir := exampleRunner(t, strings.Replace(strings.Replace(script, "%s", blocked, 1), "%s", line, 1), account)
		if err := h.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, h.dir, Finished)
		obs, err := h.Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		lookups, _ := os.ReadFile(filepath.Join(dir, "lookups"))
		return obs, strings.Count(string(lookups), "x")
	}
	obs, lookups := run("true", exampleLine("failed", `,"message":"usage limit reached"`))
	if obs.ErrorClass != decide.ErrRateLimit || !obs.ResetsAt.Equal(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) || obs.Evidence.Source != "custom_limits" || lookups != 1 {
		t.Fatalf("blocked: %+v lookups=%d", obs, lookups)
	}
	if obs, lookups := run("false", exampleLine("failed", "")); obs.ErrorClass != decide.ErrUnknown || !obs.ResetsAt.IsZero() || lookups != 1 {
		t.Fatalf("not blocked: %+v lookups=%d", obs, lookups)
	}
	// A failure with a declared code is not asked about.
	if obs, lookups := run("true", exampleLine("failed", `,"error":{"code":"unauthorized"}`)); obs.ErrorClass != decide.ErrAuth || lookups != 0 {
		t.Fatalf("classified: %+v lookups=%d", obs, lookups)
	}
}

// A turn that ends waiting on a person raises a hold once. After the
// supervisor has saved it, the same turn stops raising it, and the next send
// continues the session.
func TestDefinedAgentWaitingRaisesOneHold(t *testing.T) {
	h, dir := exampleRunner(t, `cat > /dev/null; printf '%s\n' "$@" > "$PWD/args"; echo '`+exampleLine("needs_input", "")+`'; exit 2`, nil)
	ctx := context.Background()
	if err := h.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	var adopter adapter.InteractionAdopter = h
	for i := 0; i < 3; i++ { // reported until adopted, including after a restart
		if obs, err := h.Observe(ctx); err != nil || !obs.DeferredInteraction || obs.AwaitingInput {
			t.Fatalf("check %d: %+v %v", i, obs, err)
		}
	}
	if err := adopter.AdoptInteraction(ctx); err != nil {
		t.Fatal(err)
	}
	obs, err := h.Observe(ctx)
	if err != nil || obs.DeferredInteraction || obs.State != decide.Error {
		t.Fatalf("after the hold was saved: %+v %v", obs, err)
	}
	// The next send continues the same session and can wait again.
	if err := h.Send(ctx, "go on", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); !strings.Contains(string(args), "--session\n"+exampleSessionToken) {
		t.Fatalf("did not resume: %q", args)
	}
	if obs, _ := h.Observe(ctx); !obs.DeferredInteraction {
		t.Fatalf("a later turn's wait was not raised: %+v", obs)
	}
}

// With prompt: file the agent is given the private prompt file and an empty
// standard input, and the prompt is still not an argument.
func TestDefinedAgentPromptFile(t *testing.T) {
	h, dir := exampleRunner(t, `cat > "$PWD/stdin"; printf '%s\n' "$@" > "$PWD/args"
for a in "$@"; do case "$a" in --prompt-file=*) cat "${a#--prompt-file=}" > "$PWD/got-prompt";; esac; done
echo '`+exampleLine("ok", "")+`'`, func(c *config.Custom) {
		c.Definition.Prompt = "file"
		c.Definition.Start = append(c.Definition.Start, config.Arg{Parts: []string{"--prompt-file={prompt_file}"}})
		c.Definition.Resume = append(c.Definition.Resume, config.Arg{Parts: []string{"--prompt-file={prompt_file}"}})
	})
	t.Setenv("KEEPGOING_RUN_TURN_STDIN", "prompt") // an inherited value cannot change it
	if err := h.Send(context.Background(), "a private task", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	got, _ := os.ReadFile(filepath.Join(dir, "got-prompt"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(got) != "a private task" || len(stdin) != 0 || strings.Contains(string(args), "a private task") || !strings.Contains(string(args), "--prompt-file="+filepath.Join(dir, "target", "prompt")) {
		t.Fatalf("prompt %q stdin %q args %q", got, stdin, args)
	}
	if info, err := os.Stat(filepath.Join(dir, "target", "prompt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt file: %v %v", info, err)
	}
}

// A built-in agent still gets its prompt on standard input when the variable
// that says otherwise is set in the supervisor's or the loop's environment.
func TestStdinCannotBeTakenAwayByEnvironment(t *testing.T) {
	t.Setenv("KEEPGOING_RUN_TURN_STDIN", "none")
	dir := t.TempDir()
	cli := fakeCLI(t, `cat > "$PWD/got-prompt"; echo '`+claudeOK+`'`)
	c := NewClaude(config.Claude{CLI: cli, Workdir: dir, Env: map[string]string{"KEEPGOING_RUN_TURN_STDIN": "none"}}, filepath.Join(dir, "target"))
	if err := c.Send(context.Background(), "the task", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Finished)
	if got, _ := os.ReadFile(filepath.Join(dir, "got-prompt")); string(got) != "the task" {
		t.Fatalf("prompt = %q", got)
	}
}

// Output over the definition's limit is not read at all: nothing is
// concluded from part of it, however healthy the part that fits looks. The
// turn is an unclassified failed turn, so the ordinary bounded re-prompts
// follow, and no session is taken from it.
func TestDefinedAgentOutputBound(t *testing.T) {
	h, _ := exampleRunner(t, `cat > /dev/null; head -c 3000 /dev/zero | tr '\0' 'x'; echo; echo '`+exampleLine("ok", "")+`'`, func(c *config.Custom) { c.Definition.Output.MaxBytes = config.MinCustomOutput })
	if err := h.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(string(h.dir), "exit")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	obs, err := h.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown || obs.Evidence.Source != "runner_exit" {
		t.Fatalf("%+v %v", obs, err)
	}
	if h.dir.Session() != "" {
		t.Fatalf("a session was taken from unread output: %q", h.dir.Session())
	}
	// Stop needs only the turn's record, so it does not read the output.
	if st, err := h.dir.statusWithOutputLimit(skipOutput); err != nil || st.Phase != Finished || st.Stdout != nil {
		t.Fatalf("%+v %v", st, err)
	}
	// The next turn can be started; it does not need the file removed. The
	// part of the unread output that fits holds a session line; it is not
	// taken by the send either.
	if err := h.Send(context.Background(), "again", adapter.ModeAuto); err != nil {
		t.Fatalf("could not send after oversize output: %v", err)
	}
	if h.dir.Session() != "" {
		t.Fatalf("the send took a session from unread output: %q", h.dir.Session())
	}
	waitPhase(t, Dir(string(h.dir)), Running)
	h.dir.Stop()
}

// A saved session is checked again when it is read. One that is not a
// token the definition would have saved (the file was changed or damaged) is
// never passed to the agent, and no new conversation is started in its
// place: the loop is held for a person, as it is when a built-in agent
// cannot resume. A finished turn that reported its session repairs it.
func TestDefinedAgentLostSessionHoldsInsteadOfStartingAnother(t *testing.T) {
	ctx := context.Background()
	damage := func(h *Headless, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(string(h.dir), "session"), []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The last turn failed without reporting a session, and the file is
	// damaged: nothing says which conversation to continue.
	h, dir := exampleRunner(t, `cat > /dev/null; echo x >> "$PWD/turns"; echo '{"type":"result","status":"failed"}'; exit 1`, func(c *config.Custom) { c.Definition.Limits = nil })
	if err := h.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	for _, bad := range []string{"--dangerous-flag", "two words", "{"} {
		damage(h, bad)
		obs, err := h.Observe(ctx)
		if err != nil || !obs.SessionLost {
			t.Fatalf("saved %q: %+v %v", bad, obs, err)
		}
		if args := strings.Join(h.Args(), "\n"); strings.Contains(args, bad) || strings.Contains(args, "--session") {
			t.Fatalf("saved %q reached argv: %q", bad, args)
		}
		if err := h.Send(ctx, "again", adapter.ModeAuto); err == nil || !strings.Contains(err.Error(), "refusing to start a new conversation") {
			t.Fatalf("saved %q: a new conversation was started: %v", bad, err)
		}
	}
	if turns, _ := os.ReadFile(filepath.Join(dir, "turns")); strings.Count(string(turns), "x") != 1 {
		t.Fatalf("turns started: %q", turns)
	}
	// A person restores it (or removes the file to start over on purpose).
	damage(h, exampleSessionToken)
	if obs, _ := h.Observe(ctx); obs.SessionLost || !strings.Contains(strings.Join(h.Args(), "\n"), exampleSessionToken) {
		t.Fatalf("restored: %+v %q", obs, h.Args())
	}
	os.Remove(filepath.Join(string(h.dir), "session"))
	if obs, _ := h.Observe(ctx); obs.SessionLost || strings.Contains(strings.Join(h.Args(), "\n"), "--session") {
		t.Fatalf("removed: %+v %q", obs, h.Args())
	}

	// The last turn did report its session: observing it saves it again.
	h, _ = exampleRunner(t, `cat > /dev/null; echo '`+exampleLine("ok", "")+`'`, nil)
	if err := h.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	damage(h, "--dangerous-flag")
	if obs, err := h.Observe(ctx); err != nil || obs.SessionLost || obs.State != decide.Idle || h.dir.Session() != exampleSessionToken {
		t.Fatalf("%+v %v session %q", obs, err, h.dir.Session())
	}

	// Without exact resume there is no session to lose, and the built-in
	// agents keep their own rule: whatever was saved is used.
	bare, _ := exampleRunner(t, "cat > /dev/null", func(c *config.Custom) {
		d := &c.Definition
		d.Session, d.Resume = nil, nil
		d.Capabilities.ExactResume = false
	})
	os.MkdirAll(string(bare.dir), 0o700)
	damage(bare, "--dangerous-flag")
	if obs, _ := bare.Observe(ctx); obs.SessionLost || strings.Contains(strings.Join(bare.Args(), " "), "--dangerous-flag") {
		t.Fatalf("bare: %+v %q", obs, bare.Args())
	}
	claudeDir := t.TempDir()
	c := NewClaude(config.Claude{CLI: "claude", Workdir: claudeDir}, filepath.Join(claudeDir, "target"))
	c.dir.SaveSession("sess-1")
	if obs, _ := c.Observe(ctx); obs.SessionLost || !strings.Contains(strings.Join(c.Args(), " "), "--resume sess-1") {
		t.Errorf("claude: %+v %q", obs, c.Args())
	}
}

// A person resets the session on purpose: the saved session is forgotten,
// the turn that reported it cannot bring it back, and the next turn starts a
// new conversation. Nothing is reset while a turn is running.
func TestSessionResetStartsANewConversationOnPurpose(t *testing.T) {
	ctx := context.Background()
	h, dir := exampleRunner(t, `cat > /dev/null; printf '%s\n' "$@" > "$PWD/args"; echo '`+exampleLine("ok", "")+`'`, nil)
	if had, err := h.ResetSession(); had || err != nil {
		t.Fatalf("never run: had=%v %v", had, err)
	}
	if err := h.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	if obs, _ := h.Observe(ctx); obs.State != decide.Idle || h.dir.Session() != exampleSessionToken {
		t.Fatalf("%+v session %q", obs, h.dir.Session())
	}
	had, err := h.ResetSession()
	if !had || err != nil || h.dir.Session() != "" {
		t.Fatalf("had=%v %v session %q", had, err, h.dir.Session())
	}
	// The finished turn still says which session it was; observing it, any
	// number of times, does not save it again.
	for i := 0; i < 3; i++ {
		if obs, err := h.Observe(ctx); err != nil || obs.State != decide.Idle || obs.SessionLost || h.dir.Session() != "" {
			t.Fatalf("check %d: %+v %v session %q", i, obs, err, h.dir.Session())
		}
	}
	if err := h.Send(ctx, "start over", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.dir, Finished)
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); strings.Contains(string(args), "--session") {
		t.Fatalf("the reset session was resumed: %q", args)
	}
	// The new turn's session is saved as usual, and the one after resumes it.
	if obs, _ := h.Observe(ctx); obs.State != decide.Idle || h.dir.Session() != exampleSessionToken {
		t.Fatalf("after the new turn: %+v session %q", obs, h.dir.Session())
	}

	// It is the way out of a lost session.
	if err := os.WriteFile(filepath.Join(string(h.dir), "session"), []byte("--not-a-session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(string(h.dir), "stdout"), []byte(`{"type":"result","status":"failed"}`), 0o600)
	os.WriteFile(filepath.Join(string(h.dir), "exit"), []byte("1\n"), 0o600)
	if obs, _ := h.Observe(ctx); !obs.SessionLost {
		t.Fatalf("not lost: %+v", obs)
	}
	if had, err := h.ResetSession(); !had || err != nil {
		t.Fatalf("had=%v %v", had, err)
	}
	if obs, _ := h.Observe(ctx); obs.SessionLost || obs.State != decide.Error {
		t.Fatalf("after reset: %+v", obs)
	}
	if err := h.Send(ctx, "start over", adapter.ModeAuto); err != nil {
		t.Fatalf("a send after the reset: %v", err)
	}
	waitPhase(t, h.dir, Finished)

	// While a turn runs, nothing is reset.
	running, _ := exampleRunner(t, "sleep 30", nil)
	running.dir.SaveSession(exampleSessionToken)
	if err := running.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, running.dir, Running)
	if had, err := running.ResetSession(); had || !errors.Is(err, ErrTurnRunning) || running.dir.Session() != exampleSessionToken {
		t.Fatalf("had=%v %v session %q", had, err, running.dir.Session())
	}
	running.Stop(ctx)
}

// A conversation keepgoing did not start is adopted: the next turn resumes
// it, the turn that ran before cannot overwrite it, and nothing replaces a
// conversation the loop already continues.
func TestAdoptSessionResumesAConversationKeepgoingDidNotStart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cli := fakeCLI(t, `printf '%s\n' "$@" > "$PWD/args"; cat > /dev/null; echo '`+claudeOK+`'`)
	c := NewClaude(config.Claude{CLI: cli, Workdir: dir}, filepath.Join(dir, "target"))
	const adopted = "00000000-0000-4000-8000-0000000000bb"
	if err := c.AdoptSession(adopted); err != nil {
		t.Fatalf("never run: %v", err)
	}
	if err := c.Send(ctx, "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, c.dir, Finished)
	if args, _ := os.ReadFile(filepath.Join(dir, "args")); !strings.Contains(string(args), "--resume\n"+adopted) {
		t.Fatalf("the adopted conversation was not resumed: %q", args)
	}
	// The loop now continues a conversation: adopting would replace it,
	// whether or not a check has saved it yet.
	if err := c.AdoptSession("00000000-0000-4000-8000-0000000000cc"); !errors.Is(err, ErrSessionSaved) {
		t.Fatalf("before the turn was observed: %v", err)
	}
	c.Observe(ctx)
	if err := c.AdoptSession("00000000-0000-4000-8000-0000000000cc"); !errors.Is(err, ErrSessionSaved) {
		t.Fatalf("after: %v", err)
	}
	// After a reset, the turn that reported the old session cannot bring it
	// back over the adopted one.
	if _, err := c.ResetSession(); err != nil {
		t.Fatal(err)
	}
	if err := c.AdoptSession(adopted); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c.Observe(ctx)
		if got := c.dir.Session(); got != adopted {
			t.Fatalf("observe %d replaced the adopted session with %q", i, got)
		}
	}
	if !ValidClaudeSession(adopted) || ValidClaudeSession("--resume") || ValidClaudeSession("my parser conversation") {
		t.Fatal("ValidClaudeSession")
	}
}
