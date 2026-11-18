package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/prbacklog"
	"github.com/looptender/looptender/internal/prompt"
	"github.com/looptender/looptender/internal/state"
)

const emptyBacklog = `{"data":{"repository":{"nameWithOwner":"example/project","pullRequests":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}`

func backlogWorld(t *testing.T) (*world, *fakeAdapter) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.MinWakeInterval.Duration = time.Minute
	w.cfg.Loop.PRBacklog = &config.PRBacklog{Repo: "example/project", MaxOpen: 2, RefreshInterval: config.Duration{Duration: 15 * time.Minute}, MaxAge: config.Duration{Duration: time.Hour}}
	return w, w.primary(decide.Observation{State: decide.Idle})
}
func TestPRBacklogOrdinaryWakeConsultsAndCaches(t *testing.T) {
	w, p := backlogWorld(t)
	calls := 0
	step := func(at time.Duration, fail bool) string {
		t.Helper()
		o := w.opts(t0.Add(at))
		o.BacklogQuery = func(context.Context, ...string) ([]byte, error) {
			calls++
			if fail {
				return nil, errors.New("private-secret")
			}
			return []byte(emptyBacklog), nil
		}
		r, err := Tick(context.Background(), w.cfg, o)
		if err != nil || r.Action != "wake" {
			t.Fatalf("%+v %v", r, err)
		}
		return p.sent[len(p.sent)-1]
	}
	if got := step(0, false); !strings.Contains(got, "status=below_threshold") {
		t.Fatal(got)
	}
	if got := step(time.Minute, false); strings.Contains(got, "pr-backlog") {
		t.Fatal("repeated unchanged notice", got)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if got := step(15*time.Minute, true); !strings.Contains(got, "status=unknown") || !strings.Contains(got, "historical") || strings.Contains(got, "private-secret") {
		t.Fatal(got)
	}
	if got := step(16*time.Minute, true); strings.Contains(got, "pr-backlog") {
		t.Fatal("repeated failure notice", got)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	if got := step(30*time.Minute, false); !strings.Contains(got, "status=below_threshold") {
		t.Fatal("repair transition lost", got)
	}
	st, _ := state.Load(w.dir)
	if st.PRBacklog == nil || !st.PRBacklog.Valid(w.cfg.Loop.PRBacklog.Policy(), t0.Add(30*time.Minute)) {
		t.Fatal(st.PRBacklog)
	}
	if len(w.probes) != 0 || len(p.sent) != 5 {
		t.Fatalf("new effects: reports=%v probes=%v sent=%d", w.reportTypes(), w.probes, len(p.sent))
	}
}
func TestPRBacklogThresholdAndNoticeRetryBound(t *testing.T) {
	w, p := backlogWorld(t)
	p.sendErr = errors.New("uncertain adapter return")
	pol := w.cfg.Loop.PRBacklog.Policy()
	c := prbacklog.Update(nil, pol, prbacklog.Report{Complete: true, Summary: &prbacklog.Summary{Open: 2, Oldest: t0.Add(-time.Hour)}}, t0)
	if err := state.Save(w.dir, state.File{PRBacklog: c}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for i := 0; i < 4; i++ {
		o := w.opts(t0.Add(time.Duration(i) * time.Minute))
		o.BacklogQuery = func(context.Context, ...string) ([]byte, error) {
			calls++
			t.Fatal("fresh cache queried")
			return nil, nil
		}
		Tick(context.Background(), w.cfg, o)
	}
	if calls != 0 || len(p.sent) != 4 {
		t.Fatalf("calls=%d sent=%d", calls, len(p.sent))
	}
	for i, s := range p.sent {
		if strings.Contains(s, "integration_first") != (i < 3) {
			t.Fatalf("attempt %d: %s", i, s)
		}
	}
	st, _ := state.Load(w.dir)
	if st.PRBacklog.Notice.Attempts != 3 {
		t.Fatal(st.PRBacklog)
	}
	p.sendErr = nil
	p.convKey = "example-new-conversation"
	p.convKnown = true
	o := w.opts(t0.Add(4 * time.Minute))
	o.BacklogQuery = func(context.Context, ...string) ([]byte, error) { t.Fatal("cache queried"); return nil, nil }
	Tick(context.Background(), w.cfg, o)
	if len(p.sent) != 5 || !strings.Contains(p.sent[4], "integration_first") {
		t.Fatal("new conversation suppressed")
	}
}
func TestPRBacklogControlsWinDuringRead(t *testing.T) {
	for _, hold := range []string{"stop", "pause", "awaiting", "completed", "disabled", "superseded"} {
		t.Run(hold, func(t *testing.T) {
			w, p := backlogWorld(t)
			calls := 0
			o := w.opts(t0)
			o.BacklogQuery = func(context.Context, ...string) ([]byte, error) {
				calls++
				_, err := control.Update(w.dir, func(f *control.File) {
					switch hold {
					case "stop":
						f.Stop = true
					case "pause":
						f.Paused = true
					case "awaiting":
						f.AwaitingUser = true
					case "completed":
						f.Completed = true
					case "disabled":
						f.Enabled = false
					case "superseded":
						f.SupersededBy = "thr_example_successor"
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				return []byte(emptyBacklog), nil
			}
			r, err := Tick(context.Background(), w.cfg, o)
			if err != nil || r.Action != "none" || calls != 1 || len(p.sent) != 0 {
				t.Fatalf("%+v %v calls=%d sent=%v", r, err, calls, p.sent)
			}
			used, _ := state.LoadSends(w.dir)
			if used != 0 {
				t.Fatal("reserved a suppressed wake", used)
			}
		})
	}
}
func TestPRBacklogNoQueriesOutsideOrdinaryWakes(t *testing.T) {
	for _, kind := range []string{"absent", "dry", "active", "paused", "stopped", "disabled", "superseded", "awaiting", "completed", "child-notice"} {
		t.Run(kind, func(t *testing.T) {
			w, p := backlogWorld(t)
			o := w.opts(t0)
			switch kind {
			case "absent":
				w.cfg.Loop.PRBacklog = nil
			case "dry":
				o.DryRun = true
			case "active":
				p.obs.State = decide.Active
			case "child-notice":
				p.obs.ActiveChildren = 1
				p.obs.ErroredChildren = []string{"thr_example_child"}
			default:
				_, err := control.Update(w.dir, func(f *control.File) {
					switch kind {
					case "paused":
						f.Paused = true
					case "stopped":
						f.Stop = true
					case "disabled":
						f.Enabled = false
					case "superseded":
						f.SupersededBy = "thr_example_successor"
					case "awaiting":
						f.AwaitingUser = true
					case "completed":
						f.Completed = true
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			o.BacklogQuery = func(context.Context, ...string) ([]byte, error) { t.Fatal("unexpected GitHub read"); return nil, nil }
			_, err := Tick(context.Background(), w.cfg, o)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "absent" && (len(p.sent) != 1 || p.sent[0] != "keep going") {
				t.Fatal("default send changed", p.sent)
			}
		})
	}
}
func TestPRBacklogOptionalBudgetAndStaleInvalidEvidence(t *testing.T) {
	w, _ := backlogWorld(t)
	p := w.cfg.Loop.PRBacklog.Policy()
	old := prbacklog.Update(nil, p, prbacklog.Report{Complete: true, Summary: &prbacklog.Summary{}}, t0.Add(-2*time.Hour))
	tk := &tick{ctx: context.Background(), loop: w.cfg.Loop, cfg: w.cfg, st: state.File{PRBacklog: old}, res: &Result{Target: state.Primary}, now: t0, deadline: t0.Add(10 * time.Second), reserve: 10 * time.Second, opts: Options{Clock: func() time.Time { return t0 }, BacklogQuery: func(context.Context, ...string) ([]byte, error) { t.Fatal("borrowed send reserve"); return nil, nil }}}
	a := w.adapters[state.Primary]
	if a == nil {
		a = w.primary(decide.Observation{State: decide.Idle})
	}
	m, n, allowed := tk.withBacklog(a, prompt.Message{Text: "keep going"})
	if !allowed || n == nil || !strings.Contains(m.Text, "status=unknown") || tk.st.PRBacklog.Failure != "budget_unavailable" || tk.left() != 0 {
		t.Fatalf("%s %+v %v", m.Text, n, allowed)
	}
	// A policy copied from another repo must never lend its previous zero count.
	changed := w.cfg.Loop.PRBacklog
	changed.Repo = "other/project"
	tk.loop.PRBacklog = changed
	m, _, allowed = tk.withBacklog(a, prompt.Message{Text: "keep going"})
	if !allowed || !strings.Contains(m.Text, "No complete current inventory") || strings.Contains(m.Text, "below_threshold") {
		t.Fatal(m.Text)
	}
}
