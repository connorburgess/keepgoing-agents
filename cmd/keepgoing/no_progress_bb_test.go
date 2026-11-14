//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Through the CLI's real checks against the stand-in bb: every answer BB can
// give to a turn retry, and what the flag then counts, from the run graph and
// incident history those checks wrote. A message is what bb received: a
// prompt it was told, or a retry it accepted. The graph marks every turn
// retry "returned" whatever BB answered, so it cannot be counted from there.
func TestNoProgressCountsWhatBBReceived(t *testing.T) {
	for name, tc := range map[string]struct {
		answer    string
		showAfter string
		ticks     int
		messages  int
	}{
		"dispatched again":   {"", "", 1, 1},
		"queued":             {`{"ok":true,"delivery":"queued","turnRequestId":"req_example1","attempt":3,"queuedMessageId":"qm_example1","waitingOn":{"kind":"thread-busy"},"sendAt":null}`, "", 1, 1},
		"no failed turn":     {`{"ok":false,"error":{"code":"no_failed_turn","message":"PRIVATE_RETRY_MESSAGE"}}`, "", 1, 1},
		"no longer in error": {`{"ok":false,"error":{"code":"no_failed_turn","message":"PRIVATE_RETRY_MESSAGE"}}`, `{"thread":{"status":"active"}}`, 1, 0},
		"BB without retry":   {`{"ok":false,"error":{"code":"unknown_command","message":"unknown command 'retry'"}}`, "", 1, 1},
		"already queued":     {`{"ok":false,"error":{"code":"retry_already_queued","message":"PRIVATE_RETRY_MESSAGE"}}`, "", 1, 0},
		"already queued ×3":  {`{"ok":false,"error":{"code":"retry_already_queued","message":"PRIVATE_RETRY_MESSAGE"}}`, "", 3, 0},
	} {
		f := newFixture(t)
		write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0o600)
		write(t, filepath.Join(f.bbDir, "thr_primary.log"), `[{"seq":1,"type":"turn/started","data":{"providerThreadId":"provider_example"}},{"seq":2,"type":"turn/completed","data":{"providerThreadId":"provider_example","status":"failed"}},{"seq":3,"type":"system/error","data":{"code":"thread_command_failed"}}]`, 0o600)
		if tc.answer != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.retry"), tc.answer, 0o600)
		}
		if tc.showAfter != "" {
			write(t, filepath.Join(f.bbDir, "thr_primary.show-after-retry"), tc.showAfter, 0o600)
		}
		f.run("enable")
		before := time.Now().UTC().Add(-time.Second)
		for range tc.ticks {
			f.tick()
		}
		dir := filepath.Join(f.dir, "state")
		// What bb received: the prompts it was told, and the retries it
		// accepted (by the checks' recorded answers).
		j, err := incident.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		accepted := 0
		for _, r := range j.Records {
			if r.TurnRetry == "retried" || r.TurnRetry == "queued" {
				accepted++
			}
		}
		received := strings.Count(f.read("calls"), "thread tell thr_primary ") + accepted
		if received != tc.messages {
			t.Fatalf("%s: bb received %d, the case expects %d\n%s", name, received, tc.messages, f.read("calls"))
		}
		g, availability := rungraph.Load(dir)
		if availability != rungraph.Available || len(g.Attempts) == 0 {
			t.Fatalf("%s: run graph %s with %d attempts", name, availability, len(g.Attempts))
		}
		cfg, err := config.Load(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
		threshold := config.FlagCount(1)
		cfg.Loop.Progress.FlagAfterSends = &threshold
		st, err := state.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		st.Fingerprint, st.ProgressChangedAt = "fictional", before
		ctl, err := control.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		v := noProgressOf(cfg, st, ctl, &j, &g, time.Now().UTC().Add(time.Second))
		if v.Messages == nil || v.Messages.Count != tc.messages || v.Messages.AtLeast {
			t.Errorf("%s: the flag counts %+v, bb received %d: %q", name, v.Messages, received, v.Text)
		}
	}
}
