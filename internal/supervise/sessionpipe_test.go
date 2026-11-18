package supervise

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/report"
	"github.com/looptender/looptender/internal/session"
	"github.com/looptender/looptender/internal/state"
)

// A fresh reporter each tick must not turn one uncertain episode into new
// Sessionpipe events. Separate failed-check reports are genuinely new events.
func TestSessionpipeUnknownClaimEpisodeDoesNotRetry(t *testing.T) {
	posts := 0
	accepted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"protocol":[1],"max_tier":0,"capabilities":["events"],"auth":["bearer"],"endpoints":{"events":"/events"}}`)
			return
		}
		posts++
		w.WriteHeader(202)
		if accepted {
			fmt.Fprint(w, `{"accepted":2,"duplicates":0,"rejected":[],"max_tier":0}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	t.Setenv("EXAMPLE_PIPE_URL", srv.URL)
	t.Setenv("EXAMPLE_PIPE_TOKEN", "fictional-token")
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Format: "sessionpipe", Scheme: "bearer", URLEnv: "EXAMPLE_PIPE_URL", SecretEnv: "EXAMPLE_PIPE_TOKEN", Events: []string{"all"}, Timeout: config.Duration{Duration: time.Second}}
	p := w.primary(decide.Observation{State: decide.Idle})
	take := func(since time.Time) *session.Claim {
		c, err := session.Take(w.sessions, session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "example-loop-b", PID: os.Getpid(), Since: since, Until: time.Now().Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	holder := take(time.Now().Add(-time.Hour))
	defer func() { holder.Release() }()
	for i := 0; i < 3; i++ {
		if i > 0 {
			accepted = true
		}
		r, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		opts := w.opts(t0.Add(time.Duration(i) * time.Hour))
		opts.Reporter = r
		_, _ = Tick(context.Background(), w.cfg, opts)
		if posts != i+2 {
			t.Fatalf("tick %d: got %d POSTs; want one episode plus %d independent failed-check reports", i, posts, i+1)
		}
	}
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.StuckClaimReport == nil || st.StuckClaimReport.Attempts != 1 || st.StuckClaimReport.Sent || !st.StuckClaimReport.RetryStopped {
		t.Fatalf("episode receipt/attempt count %+v", st.StuckClaimReport)
	}
	oldKey := st.StuckClaimReport.Key
	holder.Release()
	holder = take(time.Now().Add(-2 * time.Hour))
	for i := 3; i < 5; i++ {
		r, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		opts := w.opts(t0.Add(time.Duration(i) * time.Hour))
		opts.Reporter = r
		_, _ = Tick(context.Background(), w.cfg, opts)
	}
	st, err = state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 7 || st.StuckClaimReport.Key == oldKey || !st.StuckClaimReport.Sent || st.StuckClaimReport.RetryStopped || st.StuckClaimReport.Attempts != 1 {
		t.Fatalf("new episode posts=%d state=%+v", posts, st.StuckClaimReport)
	}
	if p.observed != 0 || len(p.sent) != 0 {
		t.Fatal("supervisor acted on claimed session")
	}
}

func TestOrdinaryWebhookClaimEpisodeStillRetries(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++; w.WriteHeader(503) }))
	defer srv.Close()
	t.Setenv("EXAMPLE_WEBHOOK_URL", srv.URL)
	t.Setenv("EXAMPLE_WEBHOOK_TOKEN", "fictional-token")
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Format: "text", Scheme: "bearer", URLEnv: "EXAMPLE_WEBHOOK_URL", SecretEnv: "EXAMPLE_WEBHOOK_TOKEN", Events: []string{"all"}, Timeout: config.Duration{Duration: time.Second}}
	p := w.primary(decide.Observation{State: decide.Idle})
	otherLoopStuck(t, w, "thr_primary")
	for i := 0; i < 3; i++ {
		r, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		opts := w.opts(t0.Add(time.Duration(i) * time.Hour))
		opts.Reporter = r
		_, _ = Tick(context.Background(), w.cfg, opts)
	}
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if posts != 12 || st.StuckClaimReport == nil || st.StuckClaimReport.Attempts != 3 || st.StuckClaimReport.Sent || st.StuckClaimReport.RetryStopped {
		t.Fatalf("posts=%d report=%+v", posts, st.StuckClaimReport)
	}
	if p.observed != 0 || len(p.sent) != 0 {
		t.Fatal("supervisor acted on claimed session")
	}
}
