//go:build unix

package supervise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A stand-in bb executable: thread status and log come from files the test
// edits between checks, and every command is recorded.
const fakeBBScript = `#!/bin/sh
d="$FAKE_BB_DIR"
echo "$*" >> "$d/calls"
case "$1 $2" in
"thread show") cat "$d/show" ;;
"thread list") echo '[]' ;;
"thread log") cat "$d/log" ;;
"thread interactions") echo '[]' ;;
"thread tell") cat > /dev/null; echo '{"ok":true}' ;;
*) echo '{"ok":true}' ;;
esac
`

// The second observed incident, through the real BB adapter and the
// supervisor together at the default settings (stall_after 30m,
// min_wake_interval 60m, max_nudges 3). A re-prompt goes into a dead network:
// the new turn logs one retry notice while BB reports the thread active, then
// nothing. Checks run every ten minutes. Each nudge is appended to the fake
// log as queued input, as BB may do, and must not re-arm the notice.
func TestIncidentRepromptedTurnGoesSilentWhileActive(t *testing.T) {
	for _, tracked := range []bool{true, false} {
		t.Run(map[bool]string{true: "git_tracked", false: "no_git_tracking"}[tracked], func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Progress.MaxNudges = 3
			if tracked {
				w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
			}
			dir := t.TempDir()
			t.Setenv("FAKE_BB_DIR", dir)
			cli := filepath.Join(dir, "bb")
			if err := os.WriteFile(cli, []byte(fakeBBScript), 0o755); err != nil {
				t.Fatal(err)
			}
			w.cfg.Loop.BB.CLI = cli
			var events []string
			add := func(minute int, typ, data string) {
				events = append(events, fmt.Sprintf(`{"seq":%d,"createdAt":%d,"type":%q,"data":%s}`, len(events)+1, t0.Add(time.Duration(minute)*time.Minute).UnixMilli(), typ, data))
				if err := os.WriteFile(filepath.Join(dir, "log"), []byte("["+strings.Join(events, ",")+"]"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			show := func(status string, queued int) {
				if err := os.WriteFile(filepath.Join(dir, "show"), []byte(fmt.Sprintf(`{"thread":{"status":%q,"queuedMessageCount":%d}}`, status, queued)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			const turn = `{"providerThreadId":"p1"}`
			const notice = `{"willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}`
			add(-40, "turn/started", turn)
			add(-39, "provider/error", notice)
			add(-38, "turn/completed", `{"providerThreadId":"p1","status":"failed"}`)
			add(-20, "provider/error", notice)
			show("error", 0)

			sends := func() int {
				b, _ := os.ReadFile(filepath.Join(dir, "calls"))
				return strings.Count(string(b), "thread tell thr_primary ")
			}
			got := map[int]string{}
			queued := 0
			for minute := 0; minute <= 250; minute += 10 {
				opts := w.opts(t0.Add(time.Duration(minute) * time.Minute))
				opts.NewAdapter = factory.New
				opts.Fingerprint = func([]string) (string, error) { return "same", nil }
				before := sends()
				r, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatalf("+%dm: %v", minute, err)
				}
				if r.Action != "none" {
					got[minute] = r.Action
				}
				switch r.Action {
				case "reprompt": // BB starts a turn, which logs a notice and then nothing.
					add(minute, "client/turn/requested", `{}`)
					add(minute, "turn/started", turn)
					add(minute+1, "provider/error", notice)
					show("active", 0)
				case "nudge": // Queued behind the stuck turn, undelivered.
					queued++
					add(minute, "client/turn/requested", `{}`)
					show("active", queued)
				}
				if (sends() > before) != (r.Action == "reprompt" || r.Action == "nudge") {
					t.Fatalf("+%dm: %+v sends=%d", minute, r, sends())
				}
				if minute == 10 && r.Reason != "provider is retrying the turn" {
					t.Fatalf("fresh notice in the re-prompted turn not honored: %+v", r)
				}
				if minute == 20 && (r.Reason != "active" || r.RetrySignal != "expired") {
					t.Fatalf("silent notice still honored: %+v", r)
				}
			}
			want := map[int]string{0: "reprompt", 60: "nudge", 120: "nudge", 180: "nudge", 240: "await_user"}
			note := "no progress after 3 nudges"
			if !tracked {
				// First seen expired at +20m; patience is 30m + 3 x 60m.
				want = map[int]string{0: "reprompt", 230: "await_user"}
				note = "provider retry went silent on an active session for 3h30m0s"
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("actions by minute\n got %v\nwant %v", got, want)
			}
			ctl, _ := control.Load(w.dir)
			st, _ := state.Load(w.dir)
			if !ctl.AwaitingUser || !strings.HasPrefix(ctl.Note, note) || strings.Count(w.reportTypes(), "await_user") != 1 || st.Reprompts != 1 {
				t.Fatalf("hold=%+v reports=%s reprompts=%d", ctl, w.reportTypes(), st.Reprompts)
			}
		})
	}
}
