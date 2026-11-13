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
// then reports it. spawn.refuse answers as the bb CLI does when it rejects the
// invocation before sending anything; spawn.lost creates the thread and gives
// no answer; spawn.servererror creates it and answers with a server error;
// spawn.unlisted keeps the created thread out of the listing;
// spawn.refusecreated creates the thread and then answers as a refusal.
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
  if [ -f "$d/spawn.refuse" ]; then echo '{"ok":false,"error":{"code":"invalid_value"}}'; exit 1; fi
  title=""; prev=""
  for a in "$@"; do [ "$prev" = "--title" ] && title="$a"; prev="$a"; done
  n=$(( $(cat "$d/spawn.count" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$d/spawn.count"
  if [ ! -f "$d/spawn.unlisted" ]; then printf '[{"id":"thr_spawned_%s","title":"%s"}]' "$n" "$title" > "$d/listing"; fi
  if [ -f "$d/spawn.lost" ]; then exit 1; fi
  if [ -f "$d/spawn.refusecreated" ]; then echo '{"ok":false,"error":{"code":"invalid_value"}}'; exit 1; fi
  if [ -f "$d/spawn.servererror" ]; then echo '{"ok":false,"error":{"code":"provider_unavailable"}}'; exit 1; fi
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
		!strings.HasPrefix(spawn, "thread spawn --project proj_example --environment env_example --provider claude-code --permission-mode accept-edits --title keepgoing: example-loop (continued, ") ||
		!strings.Contains(spawn, ") --prompt-file - --json --model claude-fable-5-1 --reasoning-level medium\n") {
		t.Fatalf("spawn command: %q", spawn)
	}
	prompt := f.read("spawn.prompt")
	if !strings.Contains(prompt, "taking over") || !strings.Contains(prompt, "rate_limit") || !strings.Contains(prompt, "Continue from @thread:thr_primary.") ||
		!strings.Contains(prompt, `Loop ownership: this conversation does not own the "example-loop" loop.`) || !strings.Contains(prompt, "you may run keepgoing pause, stop, await-user and complete") || strings.Contains(spawn, "taking over") {
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
	if code != exitOK || !strings.Contains(out, "thread:    thr_spawned_1\n") || !strings.Contains(out, "new threads: keepgoing has used 1 of at most 3 BB thread creation(s) for handoffs; the active target is one of them, created ") {
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

// bb gives no usable answer after BB created the thread: it is killed, or it
// reports a server error. The thread is looked for by the title keepgoing gave
// it in the same check, and the handoff completes; a second thread is never
// created.
func TestEndToEndUnprovenSpawnIsLookedUp(t *testing.T) {
	// spawn.refusecreated: a refusal is not believed over a thread that exists.
	for _, how := range []string{"spawn.lost", "spawn.servererror", "spawn.refusecreated"} {
		t.Run(how, func(t *testing.T) {
			f := newThreadFixture(t)
			write(t, filepath.Join(f.bbDir, how), "", 0o600)
			v := f.tick()
			handoff, _ := v["handoff"].(map[string]any)
			if v["action"] != "handoff" || v["target"] != "claude-other-account" || handoff["newThread"] != "thr_spawned_1" || f.read("spawn.count") != "1\n" || f.read("thr_codex.told") != "" {
				t.Fatalf("%v spawns %q", v, f.read("spawn.count"))
			}
			for i := 0; i < 3; i++ {
				f.tick()
			}
			st, _ := state.Load(filepath.Join(f.dir, "state"))
			if f.read("spawn.count") != "1\n" || st.NewThreadsCreated != 1 || st.NewThreadIntent != nil {
				t.Fatalf("spawns %q state %+v", f.read("spawn.count"), st)
			}
		})
	}
}

// The thread was created but cannot be found by its title (renamed, archived
// or not listed yet). That is not proof that nothing exists: the attempt stays
// counted, the loop is held for a person with a report, the spawn is not
// repeated and no other fallback is handed the loop. retarget then settles it.
func TestEndToEndCreatedButUnlistedThreadHoldsAndCounts(t *testing.T) {
	for _, how := range []string{"spawn.lost", "spawn.servererror"} {
		t.Run(how, func(t *testing.T) {
			f := newThreadFixture(t)
			write(t, filepath.Join(f.bbDir, how), "", 0o600)
			write(t, filepath.Join(f.bbDir, "spawn.unlisted"), "", 0o600)
			v := f.tick()
			reason, _ := v["reason"].(string)
			if v["action"] != "await_user" || !strings.Contains(reason, `titled "keepgoing: example-loop (continued, `) || !strings.Contains(reason, "cannot tell whether it exists") || f.hooks.types() != "keepgoing.await_user" {
				t.Fatalf("%v webhooks=%s", v, f.hooks.types())
			}
			st, _ := state.Load(filepath.Join(f.dir, "state"))
			if st.NewThreadsCreated != 1 || st.NewThreadIntent == nil || st.Target() != state.Primary || f.read("thr_codex.told") != "" {
				t.Fatalf("after the hold: %+v", st)
			}
			if _, out, _ := f.run("status"); !strings.Contains(out, "keepgoing has used 1 of at most 3 BB thread creation(s) for handoffs; one creation could not be confirmed") {
				t.Fatalf("status: %s", out)
			}
			// Resuming without settling it holds again; nothing is created or sent.
			os.Remove(filepath.Join(f.bbDir, how))
			for i := 0; i < 3; i++ {
				if code, _, errs := f.run("resume"); code != exitOK {
					t.Fatal(errs)
				}
				if v := f.tick(); v["action"] != "await_user" {
					t.Fatalf("resume %d: %v", i, v)
				}
			}
			if f.read("spawn.count") != "1\n" || f.read("thr_codex.told") != "" {
				t.Fatalf("spawns %q codex told %q", f.read("spawn.count"), f.read("thr_codex.told"))
			}
			// Not being listed proves nothing, so retarget alone refuses and
			// says to deal with the thread first.
			code, _, errs := f.run("retarget", "codex-main")
			st, _ = state.Load(filepath.Join(f.dir, "state"))
			if code != exitFailed || !strings.Contains(errs, "does not prove none was created") || !strings.Contains(errs, "stop or archive it first") || !strings.Contains(errs, "--discard-pending-thread") ||
				st.NewThreadIntent == nil || st.Target() != state.Primary {
				t.Fatalf("retarget without the flag: %d %s %+v", code, errs, st)
			}
			// A person who has looked says so: discarded, still counted.
			code, out, errs := f.run("retarget", "codex-main", "--discard-pending-thread")
			st, _ = state.Load(filepath.Join(f.dir, "state"))
			if code != exitOK || !strings.Contains(out, "is discarded and stays counted") || !strings.Contains(out, "a later handoff can start another conversation on the same task") || st.NewThreadIntent != nil || st.NewThreadsCreated != 1 || st.Target() != "codex-main" || len(st.NewThreads) != 0 {
				t.Fatalf("retarget: %d %s %s %+v", code, out, errs, st)
			}
		})
	}
}

// retarget looks for a thread whose creation is unsettled before it points
// the loop anywhere: found, it goes on record; unverifiable, nothing changes.
func TestRetargetReconcilesAnUnsettledCreation(t *testing.T) {
	held := func(t *testing.T) *fixture {
		f := newThreadFixture(t)
		write(t, filepath.Join(f.bbDir, "spawn.lost"), "", 0o600)
		write(t, filepath.Join(f.bbDir, "spawn.unlisted"), "", 0o600)
		if v := f.tick(); v["action"] != "await_user" {
			t.Fatalf("%v", v)
		}
		return f
	}
	t.Run("found", func(t *testing.T) {
		f := held(t)
		st, _ := state.Load(filepath.Join(f.dir, "state"))
		write(t, filepath.Join(f.bbDir, "listing"), fmt.Sprintf(`[{"id":"thr_spawned_1","title":%q}]`, st.NewThreadIntent.Title), 0o600)
		code, out, errs := f.run("retarget", "codex-main")
		st, _ = state.Load(filepath.Join(f.dir, "state"))
		if code != exitOK || !strings.Contains(out, "found the BB thread created earlier for claude-other-account") || st.NewThreadIntent != nil || st.NewThreads["claude-other-account"].Thread != "thr_spawned_1" || st.NewThreadsCreated != 1 || st.Target() != "codex-main" {
			t.Fatalf("%d %s %s %+v", code, out, errs, st)
		}
		// Nothing is pending any more, so there is nothing to discard.
		if code, _, errs := f.run("retarget", "primary", "--discard-pending-thread"); code != exitUsage || !strings.Contains(errs, "no BB thread creation with an unknown outcome") {
			t.Fatalf("discard with nothing pending: %d %s", code, errs)
		}
		if code, _, errs := f.run("resume", "--discard-pending-thread"); code != exitUsage || !strings.Contains(errs, "only supported by retarget") {
			t.Fatalf("discard on resume: %d %s", code, errs)
		}
		// The recorded thread can now be chosen by hand; nothing is created.
		if code, _, errs := f.run("retarget", "claude-other-account"); code != exitOK {
			t.Fatal(errs)
		}
		if f.read("spawn.count") != "1\n" {
			t.Fatalf("spawns %q", f.read("spawn.count"))
		}
	})
	for name, listing := range map[string]string{
		"BB cannot be asked": "not json",
		"two threads":        `[{"id":"thr_a","title":%q},{"id":"thr_b","title":%q}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := held(t)
			before, _ := state.Load(filepath.Join(f.dir, "state"))
			if strings.Contains(listing, "%q") {
				listing = fmt.Sprintf(listing, before.NewThreadIntent.Title, before.NewThreadIntent.Title)
			}
			write(t, filepath.Join(f.bbDir, "listing"), listing, 0o600)
			code, _, errs := f.run("retarget", "codex-main")
			st, _ := state.Load(filepath.Join(f.dir, "state"))
			if code != exitFailed || !strings.Contains(errs, "no retarget performed") || !strings.Contains(errs, "--discard-pending-thread") || st.NewThreadIntent == nil || st.Target() != state.Primary || st.NewThreadsCreated != 1 {
				t.Fatalf("%d %s %+v", code, errs, st)
			}
			// The way out when BB can never settle it: the same explicit flag.
			code, out, errs := f.run("retarget", "codex-main", "--discard-pending-thread")
			st, _ = state.Load(filepath.Join(f.dir, "state"))
			if code != exitOK || !strings.Contains(out, "is discarded and stays counted") || st.NewThreadIntent != nil || st.Target() != "codex-main" || st.NewThreadsCreated != 1 || len(st.NewThreads) != 0 {
				t.Fatalf("discard: %d %s %s %+v", code, out, errs, st)
			}
		})
	}
}

// The bb CLI rejects the invocation, which proves nothing was sent: the next
// fallback takes the loop in the same check, no thread is on record and the
// attempt is not counted.
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
