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
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// A stand-in bb with children: status per thread, a child list and a log
// come from files the test edits; what is told to a thread is kept, and a
// retry of its failed turn is accepted and counted.
const offlineBBScript = `#!/bin/sh
d="$FAKE_BB_DIR"
case "$1 $2" in
"thread show") if [ -f "$d/$3.show" ]; then cat "$d/$3.show"; else cat "$d/show"; fi ;;
"thread list") if [ -f "$d/children" ]; then cat "$d/children"; else echo '[]'; fi ;;
"thread log") cat "$d/log" ;;
"thread interactions") echo '[]' ;;
"thread tell") echo "tell $3" >> "$d/sends"; cat >> "$d/told"; echo >> "$d/told"; echo '{"ok":true}' ;;
"thread retry") echo "retry $3" >> "$d/sends"; echo '{"ok":true,"delivery":"sent","attempt":2}' ;;
*) echo '{"ok":true}' ;;
esac
`

// offlineReporter is a webhook that cannot be reached while the network is down.
type offlineReporter struct {
	online *bool
	types  []string
}

func (r *offlineReporter) Report(_ context.Context, e report.Event) error {
	if !*r.online {
		return fmt.Errorf("fictional network unreachable")
	}
	r.types = append(r.types, e.Type)
	return nil
}

// The owner's scenario, for an outage of any length: someone is working at
// home, the network drops, the lid is closed, and hours, days or weeks later
// the machine wakes on a new network behind a captive portal, which is then
// cleared.
//
//   - Before: the supervised BB thread is mid-turn with two children working.
//     One of three re-prompts was already used earlier.
//   - t0: the network drops. The provider logs its retry notices for about
//     two and a half minutes and the turn ends failed, class unknown. Both
//     children fail too.
//   - While offline the scheduler runs a check only now and then, at
//     irregular intervals, with one long stretch of none at all.
//   - Then five minutes behind a captive portal, where the check target does
//     not answer as itself, and then a usable network.
//
// Required at every length: nothing that needs the network is spent while
// offline or behind the portal; no hold is raised because of the outage; the
// dead retry notice is not waited out again; time-based limits do not hold or
// misreport because the clock advanced; the first check after connectivity
// returns takes exactly one recovery action; and the record tells the story.
func TestOfflineForAnyLengthThenRecoversOnce(t *testing.T) {
	for _, outage := range []time.Duration{20 * time.Minute, 4 * time.Hour, 3 * 24 * time.Hour, 21 * 24 * time.Hour} {
		t.Run(outage.String(), func(t *testing.T) {
			w := newWorld(t, true, true)
			w.cfg.Loop.Errors.MaxReprompts = 3
			w.cfg.Loop.Progress.MaxNudges = 3
			w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
			w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: 2 * time.Hour}
			w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
			w.cfg.Loop.Probe = config.Probe{Kind: "command", Command: []string{"x"}} // lets the provider be asked directly
			w.cfg.Loop.Network.VerifyAfter = config.Duration{Duration: time.Hour}
			w.cfg.Loop.Report = &config.Report{}
			dir := t.TempDir()
			t.Setenv("FAKE_BB_DIR", dir)
			cli := filepath.Join(dir, "bb")
			testexec.Shared(t, cli, offlineBBScript)
			w.cfg.Loop.BB.CLI = cli
			put := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Date(2030, 3, 4, 9, 0, 0, 0, time.UTC) // t0
			var events []string
			add := func(at time.Time, typ, data string) {
				events = append(events, fmt.Sprintf(`{"seq":%d,"createdAt":%d,"type":%q,"data":%s}`, len(events)+1, at.UnixMilli(), typ, data))
				put("log", "["+strings.Join(events, ",")+"]")
			}
			children := func(status string) {
				put("children", `[{"id":"thr_child_a"},{"id":"thr_child_b"}]`)
				for _, id := range []string{"thr_child_a", "thr_child_b"} {
					put(id+".show", fmt.Sprintf(`{"thread":{"id":%q,"parentThreadId":"thr_primary","status":%q}}`, id, status))
				}
			}
			parent := func(status string) {
				put("show", fmt.Sprintf(`{"thread":{"status":%q,"queuedMessageCount":0}}`, status))
			}
			sends := func() int {
				b, _ := os.ReadFile(filepath.Join(dir, "sends"))
				return strings.Count(string(b), "tell ")
			}
			retries := func() int {
				b, _ := os.ReadFile(filepath.Join(dir, "sends"))
				return strings.Count(string(b), "retry ")
			}

			online, fallbackProbes, verifications := true, 0, 0
			rep := &offlineReporter{online: &online}
			check := func(at time.Time) (Result, state.File) {
				t.Helper()
				opts := w.opts(at)
				opts.NewAdapter = factory.New
				opts.Fingerprint = func([]string) (string, error) { return "same", nil }
				opts.Online = func(context.Context) bool { return online }
				opts.Reporter = rep
				opts.Probe = func(_ context.Context, c config.Candidate, tier probe.Tier, _ time.Duration) probe.Result {
					if c.ID == state.Primary {
						verifications++
					} else {
						fallbackProbes++
					}
					if !online {
						return probe.Result{ErrorClass: "unknown", Detail: "timeout", TimedOut: true}
					}
					return probe.Result{OK: true}
				}
				r, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatalf("%s: %v", at.Sub(start), err)
				}
				st, _ := state.Load(w.dir)
				return r, st
			}

			// Before the outage: a turn is running with two children working, and
			// the fallbacks are confirmed. One re-prompt was used earlier today.
			const turn = `{"providerThreadId":"p1"}`
			add(start.Add(-30*time.Minute), "turn/started", turn)
			parent("active")
			children("active")
			if err := state.Save(w.dir, state.File{Reprompts: 1, LastRepromptAt: start.Add(-3 * time.Hour), LastState: "active"}); err != nil {
				t.Fatal(err)
			}
			for _, c := range w.cfg.FallbackSet.Candidates {
				w.probeOK[c.ID] = true
			}
			if r, _ := check(start.Add(-10 * time.Minute)); r.Reason != "child work running" || fallbackProbes == 0 {
				t.Fatalf("before the outage: %+v", r)
			}
			fallbackProbes = 0

			// t0: the network drops; the provider retries for about 145 seconds,
			// gives up, and the turn and both children fail.
			online = false
			at := start
			for _, gap := range []int{0, 1, 1, 2, 4, 9, 18, 36, 37, 36} {
				at = at.Add(time.Duration(gap) * time.Second)
				add(at, "provider/error", `{"willRetry":true,"errorInfo":{"category":"unknown","providerCode":"unknown"}}`)
			}
			add(at.Add(5*time.Second), "provider/error", `{"errorInfo":{"category":"unknown","providerCode":"unknown"}}`)
			add(at.Add(6*time.Second), "turn/completed", `{"providerThreadId":"p1","status":"failed"}`)
			parent("error")
			children("error")

			// Checks while offline: a few early ones, then irregular ones, with
			// none at all in the middle third of the outage.
			var offsets []time.Duration
			for _, early := range []time.Duration{3 * time.Minute, 12 * time.Minute, 19 * time.Minute, 55 * time.Minute, 130 * time.Minute, 220 * time.Minute} {
				if early < outage {
					offsets = append(offsets, early)
				}
			}
			for off, step := 7*time.Hour, 0; off < outage; off, step = off+time.Duration(5+step%4*3)*time.Hour+13*time.Minute, step+1 {
				if off > outage/3 && off < 2*outage/3 {
					continue // the lid stays shut: no check runs at all
				}
				offsets = append(offsets, off)
			}
			// Behind the captive portal the target does not answer as itself.
			offsets = append(offsets, outage+time.Minute, outage+4*time.Minute)

			sawRetrying, sawNetworkDown := false, false
			for _, off := range offsets {
				r, st := check(start.Add(off))
				ctl, _ := control.Load(w.dir)
				switch r.Reason {
				case "provider is retrying the turn":
					sawRetrying = true
					if off > 17*time.Minute+30*time.Second {
						t.Fatalf("+%s: the dead retry notice is still being waited out", off)
					}
				case "network down; waiting for connectivity":
					sawNetworkDown = true
				default:
					t.Fatalf("+%s: unexpected reason %q (%+v)", off, r.Reason, r)
				}
				if r.Action != "none" || r.Outcome != "succeeded" || ctl.AwaitingUser || sends() != 0 || retries() != 0 || fallbackProbes != 0 ||
					st.Reprompts != 1 || st.Nudges != 0 || st.Target() != state.Primary || len(rep.types) != 0 {
					t.Fatalf("+%s: spent or held while offline: %+v held=%t sends=%d fallbackProbes=%d reprompts=%d reports=%v",
						off, r, ctl.AwaitingUser, sends(), fallbackProbes, st.Reprompts, rep.types)
				}
			}
			// The provider gave up before the turn failed, so its notices are not
			// waited on at all here; either way they are dead long before the end.
			if !sawNetworkDown {
				t.Fatalf("retrying=%t networkDown=%t", sawRetrying, sawNetworkDown)
			}
			// The provider is asked directly ever less often: about nine times
			// in three weeks.
			if limit := min(int(outage/time.Hour)+2, 10); verifications > limit {
				t.Fatalf("%d verification probes in %s", verifications, outage)
			}
			_, offline := check(start.Add(outage + 4*time.Minute + 30*time.Second))
			downSince := offline.NetworkDownSince
			if downSince.IsZero() || downSince.After(start.Add(20*time.Minute)) {
				t.Fatalf("outage recorded from %v", downSince)
			}

			// The portal is cleared. The first check takes exactly one recovery
			// action: the failed turn is retried once, with no new prompt, and
			// because its children are still in error the parent is told so in
			// one notice-only message.
			online = true
			back := start.Add(outage + 8*time.Minute)
			r, st := check(back)
			ctl, _ := control.Load(w.dir)
			if r.Action != "reprompt" || r.Recovery != "turn_retry" || retries() != 1 || sends() != 1 || st.Reprompts != 2 || ctl.AwaitingUser || r.Network != "restored" ||
				r.NetworkDownFor != back.Sub(downSince).Truncate(time.Second).String() || !st.NetworkDownSince.IsZero() || !r.ChildErrorNote {
				t.Fatalf("first check after connectivity: %+v reprompts=%d sends=%d held=%t (down since %v)", r, st.Reprompts, sends(), ctl.AwaitingUser, downSince)
			}
			told, _ := os.ReadFile(filepath.Join(dir, "told"))
			if !strings.HasPrefix(string(told), "[keepgoing:child-errors count=2 ") || strings.Contains(string(told), "keep going") {
				t.Fatalf("not the notice-only message about the failed children: %q", told)
			}
			// The thread runs again; nothing more is sent.
			add(back, "client/turn/requested", `{}`)
			add(back.Add(time.Second), "turn/started", turn)
			parent("active")
			if r, _ := check(back.Add(10 * time.Minute)); r.Action != "none" || r.Reason != "active" || sends() != 1 || retries() != 1 {
				t.Fatalf("after recovery: %+v sends=%d retries=%d", r, sends(), retries())
			}

			// The fallbacks were not marked failed by the outage.
			reg, _ := health.Load(w.dir)
			for _, id := range reg.IDs() {
				if e := reg.Candidates[id]; e.Last == nil || !e.Last.OK {
					t.Fatalf("%s was marked failed by the outage: %+v", id, e.Last)
				}
			}
			// The record tells the story: the offline period is one retained
			// entry spanning it, however long, followed by the recovery.
			j, err := incident.Load(w.dir)
			if err != nil {
				t.Fatal(err)
			}
			var waited *incident.Record
			var story []string
			for i, rec := range j.Records {
				if len(story) == 0 || story[len(story)-1] != rec.Cause {
					story = append(story, rec.Cause)
				}
				if rec.Cause == "network_down" {
					waited = &j.Records[i]
				}
			}
			got := strings.Join(story, ",")
			if !strings.HasSuffix(got, "network_down,other,active") || waited == nil {
				t.Fatalf("incident history: %s", got)
			}
			first, last := waited.CheckStartedAt, waited.CheckStartedAt
			if c := waited.Collapsed; c != nil {
				first, last = c.FirstAt, c.LastAt
			}
			if outage > time.Hour && (first.After(start.Add(time.Hour)) || last.Before(start.Add(outage))) {
				t.Fatalf("the offline period is not retained as one span: %v to %v of %s", first.Sub(start), last.Sub(start), outage)
			}
		})
	}
}

// The same scenario with a turn that never fails: the host sleeps in the
// middle of a request, so BB still shows the turn as active, with no error
// and no retry notice, for as long as the network is gone. A turn that looks
// stalled while offline is not nudged and not held, at any length; the
// outage is recorded; no_progress_timeout does not count it; and when the
// network is back the turn gets stall_after of active time before a nudge.
func TestOfflineWithAHungTurnForAnyLength(t *testing.T) {
	for _, outage := range []time.Duration{20 * time.Minute, 4 * time.Hour, 3 * 24 * time.Hour, 21 * 24 * time.Hour} {
		t.Run(outage.String(), func(t *testing.T) {
			w := newWorld(t, true, true)
			w.cfg.Loop.Progress.MaxNudges = 3
			w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
			w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: 2 * time.Hour}
			w.cfg.Loop.Daemon.Interval = config.Duration{Duration: 10 * time.Minute}
			w.cfg.Loop.Report = &config.Report{}
			dir := t.TempDir()
			t.Setenv("FAKE_BB_DIR", dir)
			cli := filepath.Join(dir, "bb")
			testexec.Shared(t, cli, offlineBBScript)
			w.cfg.Loop.BB.CLI = cli
			start := time.Date(2030, 3, 4, 9, 0, 0, 0, time.UTC)
			for name, body := range map[string]string{
				"show": `{"thread":{"status":"active","queuedMessageCount":0}}`,
				"log":  fmt.Sprintf(`[{"seq":1,"createdAt":%d,"type":"turn/started","data":{"providerThreadId":"p1"}}]`, start.Add(-20*time.Minute).UnixMilli()),
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			sends := func() int {
				b, _ := os.ReadFile(filepath.Join(dir, "sends"))
				return strings.Count(string(b), "tell ")
			}
			online, connectivityChecks := true, 0
			rep := &offlineReporter{online: &online}
			check := func(at time.Time) (Result, state.File) {
				t.Helper()
				opts := w.opts(at)
				opts.NewAdapter = factory.New
				opts.Fingerprint = func([]string) (string, error) { return "same", nil }
				opts.Online = func(context.Context) bool { connectivityChecks++; return online }
				opts.Reporter = rep
				r, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatalf("%s: %v", at.Sub(start), err)
				}
				st, _ := state.Load(w.dir)
				return r, st
			}
			for _, c := range w.cfg.FallbackSet.Candidates {
				w.probeOK[c.ID] = true
			}
			// Working, checked every ten minutes, with Git last changed twenty
			// minutes before the network drops.
			for _, before := range []time.Duration{-20 * time.Minute, -10 * time.Minute} {
				if r, _ := check(start.Add(before)); r.Action != "none" || r.Reason != "active" {
					t.Fatalf("before the outage: %+v", r)
				}
			}
			online = false
			var offsets []time.Duration
			for _, early := range []time.Duration{3 * time.Minute, 12 * time.Minute, 19 * time.Minute, 55 * time.Minute, 130 * time.Minute, 220 * time.Minute} {
				if early < outage {
					offsets = append(offsets, early)
				}
			}
			for off, step := 7*time.Hour, 0; off < outage; off, step = off+time.Duration(5+step%4*3)*time.Hour+13*time.Minute, step+1 {
				if off > outage/3 && off < 2*outage/3 {
					continue
				}
				offsets = append(offsets, off)
			}
			offsets = append(offsets, outage+time.Minute, outage+4*time.Minute) // behind the portal
			known := false
			for _, off := range offsets {
				r, st := check(start.Add(off))
				ctl, _ := control.Load(w.dir)
				if r.Action != "none" || r.Outcome != "succeeded" || ctl.AwaitingUser || sends() != 0 || st.Nudges != 0 || len(rep.types) != 0 {
					t.Fatalf("+%s: spent or held while offline: %+v held=%t sends=%d nudges=%d reports=%v", off, r, ctl.AwaitingUser, sends(), st.Nudges, rep.types)
				}
				if r.Network == "down" {
					known = true
				} else if known {
					t.Fatalf("+%s: the outage was forgotten while it lasted: %+v", off, r)
				}
			}
			if !known || connectivityChecks == 0 {
				t.Fatalf("the outage was never noticed (%d connectivity checks)", connectivityChecks)
			}
			// The portal is cleared and the hung request goes through. The
			// turn is not nudged or held for the outage.
			online = true
			back := start.Add(outage + 8*time.Minute)
			r, st := check(back)
			if ctl, _ := control.Load(w.dir); r.Action != "none" || sends() != 0 || ctl.AwaitingUser || r.Network != "restored" || !st.NetworkDownSince.IsZero() {
				t.Fatalf("first check after connectivity: %+v sends=%d", r, sends())
			}
			for _, after := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
				if r, _ := check(back.Add(after)); r.Action != "none" || r.Reason != "active" || sends() != 0 {
					t.Fatalf("+%s after the outage: %+v", after, r)
				}
			}
			// Half an hour of active time with no Git change is a stall, as ever.
			if r, _ := check(back.Add(30 * time.Minute)); r.Action != "nudge" || sends() != 1 {
				t.Fatalf("an ordinary stall is no longer nudged: %+v sends=%d", r, sends())
			}
		})
	}
}
