package supervise

import (
	"context"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/report"
	"github.com/looptender/looptender/internal/state"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscordUnknownClaimEpisodeDoesNotRetry(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		posts.Add(1)
		c, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		c.Close()
	}))
	defer srv.Close()
	t.Setenv("EXAMPLE_DISCORD_URL", srv.URL+"/api/webhooks/123456789012345678/fictional")
	w := newWorld(t, true, false)
	w.cfg.Loop.Report = &config.Report{Format: "discord", Scheme: "discord-webhook", URLEnv: "EXAMPLE_DISCORD_URL", Events: []string{"all"}, Timeout: config.Duration{Duration: time.Second}}
	p := w.primary(decide.Observation{State: decide.Idle})
	otherLoopStuck(t, w, "thr_primary")
	for i := 0; i < 3; i++ {
		reporter, err := report.NewRecorded(*w.cfg.Loop.Report, w.dir)
		if err != nil {
			t.Fatal(err)
		}
		opts := w.opts(t0.Add(time.Duration(i) * time.Hour))
		opts.Reporter = reporter
		_, _ = Tick(context.Background(), w.cfg, opts)
	}
	st, err := state.Load(w.dir)
	if err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 4 || st.StuckClaimReport == nil || st.StuckClaimReport.Attempts != 1 || !st.StuckClaimReport.RetryStopped || st.StuckClaimReport.Sent {
		t.Fatalf("uncertain episode retried: posts=%d state=%+v", posts.Load(), st.StuckClaimReport)
	}
	if p.observed != 0 || len(p.sent) != 0 {
		t.Fatal("acted on claimed session")
	}
}
