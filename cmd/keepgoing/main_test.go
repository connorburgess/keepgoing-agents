//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/report"
)

// fakeBB emulates the bb CLI over files in $FAKE_BB: <thread>.show and
// <thread>.log are returned as-is (log fixtures carry seq numbers, as bb's
// events do); tell, update, and stop are recorded.
const fakeBB = `#!/bin/sh
d="$FAKE_BB"; t="$3"
echo "$*" >> "$d/calls"
case "$1 $2" in
"thread show") if [ -f "$d/$t.show" ]; then cat "$d/$t.show"; else echo '{"thread":{"status":"idle"}}'; fi ;;
"thread log")  if [ -f "$d/$t.log" ]; then cat "$d/$t.log"; else echo '[]'; fi ;;
"thread interactions") t="$4"; if [ -f "$d/$t.interactions" ]; then cat "$d/$t.interactions"; else echo '[]'; fi ;;
"thread tell") cat >> "$d/$t.told"; echo >> "$d/$t.told"; echo '{"ok":true}' ;;
*) echo '{"ok":true}' ;;
esac
`

const fakeClaude = `#!/bin/sh
echo "claude $*" >> "$FAKE_BB/probes"
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"OK"}'
`

const fakeCodex = `#!/bin/sh
echo "codex $*" >> "$FAKE_BB/probes"
printf '%s\n' '{"type":"thread.started","thread_id":"t"}' '{"type":"turn.completed","usage":{}}'
`

var secret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("fictional-e2e-secret-0123456789"))

type hooks struct {
	mu     sync.Mutex
	events []map[string]any
}

func (h *hooks) types() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var s []string
	for _, e := range h.events {
		s = append(s, fmt.Sprint(e["type"]))
	}
	return strings.Join(s, ",")
}

type fixture struct {
	t     *testing.T
	dir   string
	bbDir string
	cfg   string
	hooks *hooks
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	bbDir := filepath.Join(dir, "bb")
	os.Mkdir(bbDir, 0o700)
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o700)
	write(t, filepath.Join(bin, "bb"), fakeBB, 0o755)
	write(t, filepath.Join(bin, "claude"), fakeClaude, 0o755)
	write(t, filepath.Join(bin, "codex"), fakeCodex, 0o755)
	t.Setenv("FAKE_BB", bbDir)

	h := &hooks{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get("webhook-timestamp"), 10, 64)
		want, _ := report.Sign(secret, r.Header.Get("webhook-id"), time.Unix(ts, 0), body)
		if r.Header.Get("webhook-signature") != want["webhook-signature"] {
			w.WriteHeader(401)
			return
		}
		var v map[string]any
		json.Unmarshal(body, &v)
		h.mu.Lock()
		h.events = append(h.events, v)
		h.mu.Unlock()
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	t.Setenv("KEEPGOING_WEBHOOK_SECRET", secret)

	write(t, filepath.Join(dir, "fallbacks.yaml"), fmt.Sprintf(`version: 1
redundancy: {min_healthy: 2, span: provider}
probe: {every: 4h, ttl: 6h, timeout: 10s}
candidates:
  - id: claude-other-account
    adapter: bb
    provider: claude-code
    group: claude-account-b
    bb: {thread: thr_alt, cli: %[1]s/bb, model: claude-fable-5-1, reasoning: medium}
    probe: {model: claude-haiku-5-5, cli: %[1]s/claude}
  - id: codex-main
    adapter: bb
    provider: codex
    bb: {thread: thr_codex, cli: %[1]s/bb}
    probe: {cli: %[1]s/codex}
`, bin), 0o600)
	cfg := filepath.Join(dir, "keepgoing.yaml")
	write(t, cfg, fmt.Sprintf(`version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: bb
  provider: claude-code
  group: claude-account-a
  bb: {thread: thr_primary, cli: %s/bb}
  prompt: Continue the fictional loop.
  fallbacks: fallbacks.yaml
  report: {events: [handoff, degraded, recovered, stop, await_user]}
  daemon: {interval: 30s, control_poll: 100ms}
`, bin), 0o600)
	return &fixture{t: t, dir: dir, bbDir: bbDir, cfg: cfg, hooks: h}
}

func (f *fixture) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), append(args, "-c", f.cfg), &out, &errb)
	return code, out.String(), errb.String()
}

func (f *fixture) tick() map[string]any {
	f.t.Helper()
	code, out, errs := f.run("tick")
	if code != 0 {
		f.t.Fatalf("tick exit %d: %s %s", code, out, errs)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatalf("tick output: %v\n%s", err, out)
	}
	return v
}

func (f *fixture) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.bbDir, name))
	return string(b)
}

func TestEndToEndRateLimitHandoff(t *testing.T) {
	f := newFixture(t)
	if code, out, errs := f.run("validate"); code != 0 || !strings.Contains(out, "2 candidates") {
		t.Fatalf("validate: %d %s %s", code, out, errs)
	}
	// Disabled until enabled: nothing touches bb.
	if v := f.tick(); v["action"] != "none" || f.read("calls") != "" {
		t.Fatalf("disabled tick: %v calls=%q", v, f.read("calls"))
	}
	f.run("enable")

	// Idle primary: wake it. Scheduled probes run for both candidates.
	v := f.tick()
	if v["action"] != "wake" || !strings.Contains(f.read("thr_primary.told"), "Continue the fictional loop.") {
		t.Fatalf("wake: %v", v)
	}
	if probes := f.read("probes"); strings.Count(probes, "\n") != 2 || !strings.Contains(probes, "--model claude-haiku-5-5") {
		t.Fatalf("scheduled probes: %q", probes)
	}

	// Primary hits a structured rate limit with a far reset.
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
	reset := time.Now().Add(4 * time.Hour).UnixMilli()
	write(t, filepath.Join(f.bbDir, "thr_primary.log"), fmt.Sprintf(`[{"seq":1,"type":"turn/started","data":{}},
{"seq":2,"type":"provider/error","data":{"message":"Usage limit reached","willRetry":false,"errorInfo":{"category":"rate-limit","httpStatusCode":429,"providerCode":"rate_limit_event"}}},
{"seq":3,"type":"provider/rateLimits/updated","data":{"rateLimits":{"status":"blocked","windows":[{"status":"blocked","resetsAtMs":%d}]}}}]`, reset), 0o600)
	v = f.tick()
	if v["action"] != "handoff" || v["target"] != "claude-other-account" {
		t.Fatalf("handoff: %v", v)
	}
	if !strings.Contains(f.read("probes"), "--model claude-fable-5-1") {
		t.Errorf("exact probe did not use the target model: %q", f.read("probes"))
	}
	calls := f.read("calls")
	if !strings.Contains(calls, "thread update thr_alt --json --model claude-fable-5-1 --reasoning-level medium") {
		t.Errorf("model not switched: %s", calls)
	}
	if brief := f.read("thr_alt.told"); !strings.Contains(brief, "taking over") || !strings.Contains(brief, "rate_limit") {
		t.Errorf("brief = %q", brief)
	}
	if f.hooks.types() != "keepgoing.handoff" {
		t.Errorf("webhooks = %s", f.hooks.types())
	}

	// Status and fallbacks list reflect the move.
	if _, out, _ := f.run("status"); !strings.Contains(out, "target:    claude-other-account (1 handoffs") {
		t.Errorf("status:\n%s", out)
	}
	if _, out, _ := f.run("fallbacks", "list"); !strings.Contains(out, "codex-main") {
		t.Errorf("fallbacks list:\n%s", out)
	}

	// Stop: the active target is stopped once, and reported.
	write(t, filepath.Join(f.bbDir, "thr_alt.show"), `{"thread":{"status":"active"}}`, 0o600)
	f.run("stop", "--note", "reviewing by hand")
	if v := f.tick(); v["action"] != "stop" || !strings.Contains(f.read("calls"), "thread stop thr_alt --json") {
		t.Fatalf("stop: %v", v)
	}
	if v := f.tick(); v["action"] != "none" {
		t.Fatalf("second stop tick: %v", v)
	}
	if !strings.HasSuffix(f.hooks.types(), "keepgoing.stop") {
		t.Errorf("webhooks = %s", f.hooks.types())
	}

	// Retarget back to the primary by hand.
	f.run("resume")
	if code, out, _ := f.run("retarget", "primary"); code != 0 || !strings.Contains(out, "was claude-other-account") {
		t.Errorf("retarget: %d %s", code, out)
	}
	if code, _, _ := f.run("retarget", "nope"); code != exitUsage {
		t.Errorf("retarget unknown: %d", code)
	}
}

func TestEndToEndAgentWaitingOnAPerson(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	// The agent asked the person something: an idle thread with a pending interaction.
	write(t, filepath.Join(f.bbDir, "thr_primary.interactions"), `[{"id":"pint_example1","status":"pending"}]`, 0o600)
	if v := f.tick(); v["action"] != "none" || f.read("thr_primary.told") != "" {
		t.Fatalf("woke a thread that is waiting on a person: %v", v)
	}
	if f.hooks.types() != "keepgoing.await_user" {
		t.Errorf("webhooks = %s", f.hooks.types())
	}
	// Answered, but the agent then declares it needs the person for something else.
	os.Remove(filepath.Join(f.bbDir, "thr_primary.interactions"))
	if code, _, _ := f.run("await-user", "--note", "run the fictional probe command"); code != 0 {
		t.Fatal("await-user failed")
	}
	if !strings.HasSuffix(f.hooks.types(), "keepgoing.await_user,keepgoing.await_user") {
		t.Errorf("await-user did not report: %s", f.hooks.types())
	}
	if v := f.tick(); v["action"] != "none" || f.read("thr_primary.told") != "" {
		t.Fatalf("woke a loop marked awaiting user: %v", v)
	}
	// The person answers and resumes the loop: the idle thread is woken again.
	f.run("resume")
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("after resume: %v", v)
	}
}

func TestEndToEndUnknownErrorNeverHandsOff(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
	// Prose says "usage limit" but the structured category is unknown.
	write(t, filepath.Join(f.bbDir, "thr_primary.log"), `[{"seq":1,"type":"turn/started","data":{}},{"seq":2,"type":"provider/error","data":{"message":"usage limit reached","willRetry":false,"errorInfo":{"category":"unknown","httpStatusCode":null,"providerCode":"unknown"}}}]`, 0o600)
	if v := f.tick(); v["action"] != "reprompt" || v["target"] != "primary" {
		t.Fatalf("%v", v)
	}
	if f.read("thr_alt.told") != "" || f.read("thr_codex.told") != "" {
		t.Error("a fallback was messaged on an unknown error")
	}
}

func TestEndToEndLockAndDryRun(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	if code, out, _ := f.run("tick", "--dry-run"); code != 0 || !strings.Contains(out, `"dryRun":true`) || f.read("thr_primary.told") != "" || f.read("probes") != "" {
		t.Fatalf("dry run sent or probed: %s", out)
	}
	// Hold the lock from another process: flock is per open file description.
	held := filepath.Join(f.dir, "held")
	cmd := execHolder(t, filepath.Join(f.dir, "state", "tick.lock"), held)
	defer cmd.Process.Kill()
	if code, _, errs := f.run("tick"); code != exitSkipped || !strings.Contains(errs, "lock") {
		t.Errorf("held lock: %d %s", code, errs)
	}
}

func TestDaemonActsOnStopWithinOnePoll(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var out, errb safeBuffer
	go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, &out, &errb) }()
	waitFor(t, func() bool { return strings.Count(out.String(), "\n") >= 1 }, "first tick")
	// A second daemon refuses to start.
	if code := run(context.Background(), []string{"daemon", "-c", f.cfg}, io.Discard, io.Discard); code != exitSkipped {
		t.Errorf("second daemon exit = %d", code)
	}
	start := time.Now()
	f.run("stop")
	waitFor(t, func() bool { return strings.Contains(f.read("calls"), "thread stop thr_primary") }, "stop")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stop took %v; the interval is 30s, so the daemon must react to control changes", d)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("daemon exit = %d: %s", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit on cancel")
	}
}

func TestCIWaitAndNetRetryCommands(t *testing.T) {
	dir := t.TempDir()
	sha := strings.Repeat("a", 40)
	gh := filepath.Join(dir, "gh")
	write(t, gh, `#!/bin/sh
case "$1 $2" in
"pr view")  echo '{"headRefName":"cb/example","headRefOid":"`+sha+`"}' ;;
"run list") echo '[{"databaseId":3,"headSha":"`+sha+`","status":"completed","conclusion":"success","workflowName":"Checks"}]' ;;
"run view") echo '{"jobs":[{"name":"Build and test","conclusion":"success"}]}' ;;
esac
`, 0o755)
	var out bytes.Buffer
	code := run(context.Background(), []string{"ci-wait", "--repo", "example-org/example-app", "--pr", "7", "--gh", gh}, &out, io.Discard)
	if code != 0 || !strings.Contains(out.String(), "Build and test: success") {
		t.Errorf("ci-wait: %d %s", code, out.String())
	}
	t.Setenv("KEEPGOING_NET_PROBE", "true") // online: a real failure is returned at once
	if code := run(context.Background(), []string{"net-retry", "--", "/bin/sh", "-c", "exit 4"}, io.Discard, io.Discard); code != 4 {
		t.Errorf("net-retry exit = %d", code)
	}
	if code := run(context.Background(), []string{"net-retry"}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("net-retry usage = %d", code)
	}
}

func TestReportTestDryRunAndBadConfig(t *testing.T) {
	f := newFixture(t)
	if code, out, _ := f.run("report", "test", "--dry-run"); code != 0 || !strings.Contains(out, `"type":"keepgoing.test"`) {
		t.Errorf("%d %s", code, out)
	}
	if code, _, _ := f.run("report", "test"); code != 0 || f.hooks.types() != "keepgoing.test" {
		t.Errorf("delivery: %d %s", code, f.hooks.types())
	}
	bad := filepath.Join(f.dir, "bad.yaml")
	write(t, bad, "version: 1\nloop: {name: Bad Name}\n", 0o600)
	if code := run(context.Background(), []string{"validate", "-c", bad}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("bad config exit = %d", code)
	}
}

func TestDaemonWakesForControlChangeDuringAPass(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	code, _, _ := f.run("validate")
	if code != 0 {
		t.Fatal("fixture does not validate")
	}
	cfgPath := f.cfg
	cfgLoaded := loadFixture(t, cfgPath)
	sig := controlSignature(cfgLoaded.Loop.StateDir)
	time.Sleep(20 * time.Millisecond)
	f.run("stop") // written "during the pass", after the signature was taken
	start := time.Now()
	if !waitForNext(context.Background(), cfgLoaded, sig) {
		t.Fatal("waitForNext asked the daemon to exit")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("waited %v for a change made during the pass; interval is 30s", d)
	}
}

func TestFallbacksCheckExitsOneWhenRedundancyIsUnmet(t *testing.T) {
	f := newFixture(t)
	// Break the codex candidate's probe so only one provider is healthy.
	os.WriteFile(filepath.Join(f.dir, "bin", "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	code, out, _ := f.run("fallbacks", "check")
	if code != exitFailed || !strings.Contains(out, "handoffs refused") {
		t.Errorf("%d\n%s", code, out)
	}
	if code, _, _ := f.run("fallbacks", "check", "--id", "claude-other-account"); code != exitFailed {
		t.Errorf("--id check still reports the unmet rule: %d", code)
	}
}

func TestRetargetPrimaryAppliesItsModel(t *testing.T) {
	f := newFixture(t)
	b, _ := os.ReadFile(f.cfg)
	s := strings.Replace(string(b), "bb: {thread: thr_primary, cli:", "bb: {thread: thr_primary, model: claude-opus-5-5, cli:", 1)
	os.WriteFile(f.cfg, []byte(s), 0o600)
	if code, out, errs := f.run("retarget", "primary"); code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if !strings.Contains(f.read("calls"), "thread update thr_primary --json --model claude-opus-5-5") {
		t.Errorf("calls = %s", f.read("calls"))
	}
}
