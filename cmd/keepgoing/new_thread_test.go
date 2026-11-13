//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// fakeBBWithSpawn is the fixture's fake bb plus thread creation. A created
// thread is added to the listing bb would return, whether or not the command
// then reports it, so a lost answer can be told from a refused creation.
const fakeBBWithSpawn = `#!/bin/sh
d="$FAKE_BB"; t="$3"
echo "$*" >> "$d/calls"
case "$1 $2" in
"thread show") if [ -f "$d/$t.show" ]; then cat "$d/$t.show"; else echo '{"thread":{"status":"idle"}}'; fi ;;
"thread list")
  if [ "$3" = "--project" ]; then
    if [ -f "$d/listing" ]; then cat "$d/listing"; else echo '[]'; fi
  else
    t="$4"; if [ -f "$d/$t.children" ]; then cat "$d/$t.children"; else echo '[]'; fi
  fi ;;
"thread spawn")
  cat > "$d/spawn.prompt"
  if [ -f "$d/spawn.refuse" ]; then echo '{"ok":false,"error":{"code":"provider_unavailable"}}'; exit 1; fi
  title=""; prev=""
  for a in "$@"; do [ "$prev" = "--title" ] && title="$a"; prev="$a"; done
  n=$(( $(cat "$d/spawn.count" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$d/spawn.count"
  printf '[{"id":"thr_spawned_%s","title":"%s"}]' "$n" "$title" > "$d/listing"
  if [ -f "$d/spawn.lost" ]; then exit 1; fi
  printf '{"id":"thr_spawned_%s","status":"active","projectId":"proj_example","environmentId":"env_example"}\n' "$n" ;;
"thread log")  if [ -f "$d/$t.log" ]; then cat "$d/$t.log"; else echo '[]'; fi ;;
"thread interactions") t="$4"; if [ -f "$d/$t.interactions" ]; then cat "$d/$t.interactions"; else echo '[]'; fi ;;
"thread tell") cat >> "$d/$t.told"; echo >> "$d/$t.told"; echo '{"ok":true}' ;;
*) echo '{"ok":true}' ;;
esac
`

// newThreadFixture is the end-to-end fixture with its first fallback changed
// to start a new thread at handoff, and the primary in a rate limit.
func newThreadFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	write(t, filepath.Join(f.dir, "bin", "bb"), fakeBBWithSpawn, 0o755)
	fallbacks, _ := os.ReadFile(filepath.Join(f.dir, "fallbacks.yaml"))
	old := "bb: {thread: thr_alt, cli: " + f.dir + "/bin/bb, model: claude-fable-5-1, reasoning: medium}"
	if !strings.Contains(string(fallbacks), old) {
		t.Fatal("fixture fallbacks changed")
	}
	write(t, filepath.Join(f.dir, "fallbacks.yaml"), strings.Replace(string(fallbacks), old, "bb: {new_thread: true, cli: "+f.dir+"/bin/bb, model: claude-fable-5-1, reasoning: medium}", 1), 0o600)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("wake: %v", v)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"error","projectId":"proj_example","environmentId":"env_example"}}`, 0o600)
	reset := time.Now().Add(4 * time.Hour).UnixMilli()
	write(t, filepath.Join(f.bbDir, "thr_primary.log"), fmt.Sprintf(`[{"seq":1,"type":"turn/started","data":{}},
{"seq":2,"type":"provider/error","data":{"message":"Usage limit reached","willRetry":false,"errorInfo":{"category":"rate-limit","httpStatusCode":429,"providerCode":"rate_limit_event"}}},
{"seq":3,"type":"provider/rateLimits/updated","data":{"rateLimits":{"status":"blocked","windows":[{"status":"blocked","resetsAtMs":%d}]}}}]`, reset), 0o600)
	return f
}

// Through the real CLI and BB adapter against a fake bb: a handoff to a
// fallback with bb.new_thread creates one thread, with the brief as its first
// prompt, and supervision moves to it. The old conversation is not touched.
func TestEndToEndHandoffToANewThread(t *testing.T) {
	f := newThreadFixture(t)
	told := f.read("thr_primary.told")
	v := f.tick()
	handoff, _ := v["handoff"].(map[string]any)
	if v["action"] != "handoff" || v["target"] != "claude-other-account" || handoff["newThread"] != "thr_spawned_1" || handoff["continues"] != "thr_primary" {
		t.Fatalf("handoff: %v", v)
	}
	calls := f.read("calls")
	var spawn string
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "thread spawn ") {
			spawn += line + "\n"
		}
	}
	if strings.Count(spawn, "\n") != 1 ||
		!strings.HasPrefix(spawn, "thread spawn --project proj_example --environment env_example --provider claude-code --title keepgoing: example-loop (continued, ") ||
		!strings.Contains(spawn, ") --prompt-file - --json --model claude-fable-5-1 --reasoning-level medium\n") {
		t.Fatalf("spawn command: %q", spawn)
	}
	prompt := f.read("spawn.prompt")
	if !strings.Contains(prompt, "taking over") || !strings.Contains(prompt, "rate_limit") || !strings.Contains(prompt, "This conversation continues @thread:thr_primary.") || strings.Contains(spawn, "taking over") {
		t.Fatalf("first prompt: %q", prompt)
	}
	// The conversation that was left is not stopped, archived, updated or sent to.
	for _, bad := range []string{"thread stop", "thread archive", "thread delete", "thread update", "thread fork", "thread tell thr_primary --message-file - --mode auto --json\nthread spawn"} {
		if strings.Contains(calls, bad) {
			t.Fatalf("unexpected command %q in:\n%s", bad, calls)
		}
	}
	if f.read("thr_primary.told") != told {
		t.Fatal("the old conversation was sent a message")
	}
	if f.hooks.types() != "keepgoing.handoff" {
		t.Fatalf("webhooks = %s", f.hooks.types())
	}
	// Later checks supervise the created thread and create nothing more.
	for i := 0; i < 3; i++ {
		if v := f.tick(); v["action"] != "wake" || v["target"] != "claude-other-account" {
			t.Fatalf("check %d: %v", i, v)
		}
	}
	if f.read("spawn.count") != "1\n" || strings.Count(f.read("thr_spawned_1.told"), "Continue the fictional loop.") != 3 {
		t.Fatalf("spawns %q, told %q", f.read("spawn.count"), f.read("thr_spawned_1.told"))
	}

	code, out, errs := f.run("status")
	if code != exitOK || !strings.Contains(out, "thread:    thr_spawned_1\n") || !strings.Contains(out, "new threads: keepgoing has created 1 of at most 3 BB thread(s) for handoffs; the active target is one of them, created ") {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	code, out, errs = f.run("timeline")
	if code != exitOK || !strings.Contains(out, "NEW THREAD: keepgoing created a BB thread for this handoff and left the earlier conversation as it was") || strings.Contains(out, "thr_spawned") {
		t.Fatalf("timeline: %d %s %s", code, out, errs)
	}
	code, out, errs = f.run("runs", "--json")
	if code != exitOK || !strings.Contains(out, "thr_spawned_1") || !strings.Contains(out, "thr_primary") || !strings.Contains(out, "created_thread") {
		t.Fatalf("runs: %d %s %s", code, out, errs)
	}

	// Returning to the primary by hand keeps the created thread on record and
	// touches neither thread.
	if code, _, errs := f.run("retarget", "primary"); code != exitOK {
		t.Fatal(errs)
	}
	st, _ := state.Load(filepath.Join(f.dir, "state"))
	if st.Target() != state.Primary || st.NewThreads["claude-other-account"].Thread != "thr_spawned_1" || st.NewThreadsCreated != 1 {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(f.read("calls"), "thread stop") || strings.Contains(f.read("calls"), "thread archive") {
		t.Fatal("a thread was stopped or archived")
	}
}

// bb is killed after BB created the thread, so no answer comes back. The next
// check finds the thread by the title keepgoing gave it and completes the
// handoff; a second thread is never created.
func TestEndToEndLostSpawnAnswerIsReconciled(t *testing.T) {
	f := newThreadFixture(t)
	write(t, filepath.Join(f.bbDir, "spawn.lost"), "", 0o600)
	code, out, _ := f.run("tick")
	if code != exitOK || !strings.Contains(out, `"action":"none"`) || !strings.Contains(out, "a new thread may have been created for this handoff") {
		t.Fatalf("%d %s", code, out)
	}
	st, _ := state.Load(filepath.Join(f.dir, "state"))
	if st.NewThreadIntent == nil || st.Target() != state.Primary || f.read("thr_codex.told") != "" {
		t.Fatalf("after the lost answer: %+v", st)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "one creation is not yet confirmed") {
		t.Fatalf("status: %s", out)
	}
	os.Remove(filepath.Join(f.bbDir, "spawn.lost"))
	v := f.tick()
	handoff, _ := v["handoff"].(map[string]any)
	if v["target"] != "claude-other-account" || handoff["newThread"] != "thr_spawned_1" || handoff["reconciled"] != true || f.read("spawn.count") != "1\n" {
		t.Fatalf("%v spawns %q", v, f.read("spawn.count"))
	}
	for i := 0; i < 3; i++ {
		f.tick()
	}
	if f.read("spawn.count") != "1\n" {
		t.Fatalf("spawns %q", f.read("spawn.count"))
	}
}

// bb refuses to create the thread: the next fallback takes the loop in the
// same check, and no thread is on record.
func TestEndToEndRefusedSpawnFallsThroughAndRetargetNeverCreates(t *testing.T) {
	f := newThreadFixture(t)
	// Retarget never creates a thread for a fallback that has none yet.
	if code, _, errs := f.run("retarget", "claude-other-account"); code != exitUsage || !strings.Contains(errs, "retarget never creates a thread") {
		t.Fatalf("retarget: %d %s", code, errs)
	}
	write(t, filepath.Join(f.bbDir, "spawn.refuse"), "", 0o600)
	v := f.tick()
	if v["action"] != "handoff" || v["target"] != "codex-main" || f.read("spawn.count") != "" || !strings.Contains(f.read("thr_codex.told"), "taking over") {
		t.Fatalf("%v", v)
	}
	st, _ := state.Load(filepath.Join(f.dir, "state"))
	if len(st.NewThreads) != 0 || st.NewThreadsCreated != 0 || st.NewThreadIntent != nil {
		t.Fatalf("%+v", st)
	}
}
