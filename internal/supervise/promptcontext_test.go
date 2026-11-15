package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/incident"
	"github.com/looptender/looptender/internal/loopcontext"
	"github.com/looptender/looptender/internal/prompt"
	"github.com/looptender/looptender/internal/rungraph"
	"github.com/looptender/looptender/internal/state"
)

var contextTag = prompt.ContextTag(loopcontext.SchemaVersion)

// The journey of TestPromptContextOffSendsMainBytes with the key on: the
// context goes with the first wake, both briefs, and nothing else. The
// notice-only wake never carries it. On a BB thread the wake header stays
// the first line and the context comes after the whole message.
func TestPromptContextOnlyWhereDue(t *testing.T) {
	got := sentJourney(t, func(l *config.Loop) { l.PromptContext = true })
	sends := regexp.MustCompile(`(?m)^=== `).Split(got, -1)[1:]
	want := map[string]bool{
		"0s wake primary":               true,
		"1h1m0s nudge primary":          false,
		"2h0m0s reprompt primary":       false,
		"2h30m0s handoff codex-main":    true,
		"4h0m0s wake codex-main":        false,
		"4h40m0s switch_back primary":   true,
		"child-error notice primary #0": false,
	}
	if len(sends) != len(want) {
		t.Fatalf("%d sends:\n%s", len(sends), got)
	}
	header := prompt.WakeHeader("example-loop")
	for _, s := range sends {
		label := s[:strings.Index(s, "\n")]
		text := s[len(label)+1:]
		var key string
		for k := range want {
			if strings.HasPrefix(label, k) {
				key = k
			}
		}
		if key == "" {
			t.Fatalf("unexpected send %q", label)
		}
		if n := strings.Count(text, contextTag); n != map[bool]int{true: 1, false: 0}[want[key]] {
			t.Errorf("%s: %d context blocks:\n%s", key, n, text)
		}
		if strings.Contains(key, "primary") && !strings.HasPrefix(text, header+"\n\n") {
			t.Errorf("%s: the wake header is not first:\n%s", key, text)
		}
		if i := strings.Index(text, contextTag); i >= 0 {
			// Everything the message had comes first, then the block.
			if !strings.Contains(text[:i], "keep going") || strings.Contains(text[i:], "keep going") {
				t.Errorf("%s: the block is not after the message:\n%s", key, text)
			}
			if !strings.Contains(text[i:], "\nloop:      example-loop\n") || !strings.Contains(text[i:], "authority:\n  Supervision grants no permission.\n") {
				t.Errorf("%s: not the builder's text:\n%s", key, text[i:])
			}
		}
	}
}

// promptContextWorld is one BB loop with the key on and a primary that is idle.
func promptContextWorld(t *testing.T) (*world, *fakeAdapter, func(time.Duration) string) {
	t.Helper()
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptContext = true
	p := w.primary(decide.Observation{State: decide.Idle})
	n := 0
	wake := func(at time.Duration) string {
		t.Helper()
		o := w.opts(t0.Add(at))
		o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return bbNamedAdapter{p}, nil }
		res, err := Tick(context.Background(), w.cfg, o)
		if res.Action != "wake" || len(p.sent) != n+1 {
			t.Fatalf("at %v: %+v %v", at, res, err)
		}
		n++
		return p.sent[n-1]
	}
	return w, p, wake
}

func carries(text string) bool { return strings.Contains(text, contextTag) }

// The context goes again after a control save (resume, a change of owner),
// a change of its content, a failed send, and the key being turned off and
// on; not for the figures that move with every send.
func TestPromptContextResendRules(t *testing.T) {
	w, p, wake := promptContextWorld(t)
	w.cfg.Loop.Limits.MaxSends = 40
	hour := 0
	next := func() time.Duration { hour += 2; return time.Duration(hour) * time.Hour }
	step := func(name string, want bool) {
		t.Helper()
		if got := carries(wake(next())); got != want {
			t.Fatalf("%s: carries context = %v, want %v", name, got, want)
		}
	}
	update := func(f func(*control.File)) {
		t.Helper()
		if _, err := control.Update(w.dir, f); err != nil {
			t.Fatal(err)
		}
	}
	step("first send after the key is on", true)
	step("unchanged; one more send used", false)
	step("still unchanged", false)
	update(func(f *control.File) { f.Paused = true })
	update(func(f *control.File) { f.Paused = false })
	step("after pause and resume", true)
	step("unchanged after the resume", false)
	update(func(f *control.File) { f.Ownership = &control.Ownership{ThreadID: "thr_example_owner", Generation: 1} })
	step("after a change of owner", true)
	step("unchanged after the change of owner", false)
	w.cfg.Loop.Limits.MaxSends = 60
	step("after its content changed", true)

	w.cfg.Loop.Limits.MaxSends = 80
	p.sendErr = errors.New("uncertain delivery")
	o := w.opts(t0.Add(next()))
	o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return bbNamedAdapter{p}, nil }
	Tick(context.Background(), w.cfg, o)
	if !carries(p.sent[len(p.sent)-1]) {
		t.Fatal("changed content not sent")
	}
	p.sendErr = nil
	p.sent = p.sent[:len(p.sent)-1]
	step("after a send that returned an error", true)
	step("unchanged after that", false)

	w.cfg.Loop.PromptContext = false
	step("key off", false)
	if st, _ := state.Load(w.dir); st.ContextSent != nil {
		t.Fatalf("record kept with the key off: %+v", st.ContextSent)
	}
	w.cfg.Loop.PromptContext = true
	step("key on again", true)
}

// A send that carries the context records its length with the send's
// provenance: the last prompt send, the run graph's attempt and the check's
// incident record. A send without it records nothing. state.json keeps a
// digest, never the text.
func TestPromptContextBytesAreRecordedWithTheSend(t *testing.T) {
	w, _, wake := promptContextWorld(t)
	text := wake(0)
	i := strings.Index(text, "\n\n"+contextTag)
	if i < 0 {
		t.Fatalf("no block: %q", text)
	}
	n := len(text) - i
	st, err := state.Load(w.dir)
	if err != nil || st.LastPromptSend == nil || st.LastPromptSend.ContextBytes != n || st.ContextSent == nil || st.ContextSent.Bytes != n {
		t.Fatalf("send=%+v record=%+v err=%v want %d", st.LastPromptSend, st.ContextSent, err, n)
	}
	b, _ := os.ReadFile(filepath.Join(w.dir, state.FileName))
	if strings.Contains(string(b), "Supervision grants no permission") || strings.Contains(string(b), contextTag) {
		t.Fatal("state.json holds the context's text")
	}
	g, _ := rungraph.Load(w.dir)
	if len(g.Attempts) != 1 || g.Attempts[0].ContextBytes != n {
		t.Fatalf("attempts %+v", g.Attempts)
	}
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) == 0 || j.Records[len(j.Records)-1].ContextBytes != n {
		t.Fatalf("incident %+v %v", j.Records, err)
	}

	wake(2 * time.Hour)
	st, _ = state.Load(w.dir)
	g, _ = rungraph.Load(w.dir)
	j, _ = incident.Load(w.dir)
	if st.LastPromptSend.ContextBytes != 0 || g.Attempts[len(g.Attempts)-1].ContextBytes != 0 || j.Records[len(j.Records)-1].ContextBytes != 0 {
		t.Fatalf("a send without the context recorded a length: %+v", st.LastPromptSend)
	}
}

// A headless target whose session has been reset starts a new conversation
// on its next turn, so that turn carries the context again; the fence the
// reset leaves is what says so.
func TestPromptContextAfterASessionReset(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptContext = true
	w.cfg.Loop.Agent = config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: t.TempDir()}}
	p := w.primary(decide.Observation{State: decide.Idle})
	wake := func(at time.Duration) string {
		t.Helper()
		before := len(p.sent)
		if res := runTick(t, w, t0.Add(at)); res.Action != "wake" || len(p.sent) != before+1 {
			t.Fatalf("at %v: %+v", at, res)
		}
		return p.sent[before]
	}
	if text := wake(0); !carries(text) || strings.HasPrefix(text, "Automatic looptender wake") {
		t.Fatalf("first turn: %q", text)
	}
	if carries(wake(2 * time.Hour)) {
		t.Fatal("repeated in the same session")
	}
	dir := filepath.Join(w.dir, "targets", state.Primary)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fence := filepath.Join(dir, "session-reset")
	if err := os.WriteFile(fence, []byte("example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !carries(wake(4 * time.Hour)) {
		t.Fatal("not sent into the conversation the reset starts")
	}
	os.Remove(fence) // the runner consumes it when that turn starts
	if carries(wake(6 * time.Hour)) {
		t.Fatal("repeated after the new conversation got it")
	}
}

// The first message of a BB thread looptender creates for a handoff carries
// the context last, after the ownership note, and records its length. The
// thread's identity is not known yet, so the context says so; the first wake
// in the thread, once it is recorded, carries the context again.
func TestPromptContextInANewThreadsFirstMessage(t *testing.T) {
	w, _, f := newThreadWorld(t)
	w.cfg.Loop.PromptContext = true
	res, err := threadTick(t, w, f, t0, map[string]string{})
	if err != nil || res.Action != "handoff" || len(f.spawned) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	first := f.spawned[0].Prompt
	i, j := strings.Index(first, "Loop ownership:"), strings.Index(first, "\n\n"+contextTag+"\n")
	if i < 0 || j < i || strings.Count(first, contextTag) != 1 {
		t.Fatalf("first prompt: %q", first)
	}
	if !strings.Contains(first[j:], `target:    unavailable (the active route "claude-other" starts a new BB thread, and its thread has not been recorded yet)`) {
		t.Fatalf("context: %q", first[j:])
	}
	st, _ := state.Load(w.dir)
	if st.LastPromptSend == nil || st.LastPromptSend.ContextBytes != len(first)-j || st.ContextSent == nil {
		t.Fatalf("send=%+v record=%+v", st.LastPromptSend, st.ContextSent)
	}
	c := w.adapters["claude-other"]
	if c == nil {
		c = &fakeAdapter{obs: decide.Observation{State: decide.Idle}}
		w.adapters["claude-other"] = c
	}
	c.obs = decide.Observation{State: decide.Idle}
	if res := runTick(t, w, t0.Add(2*time.Hour)); res.Action != "wake" || len(c.sent) != 1 || !strings.Contains(c.sent[0], "BB thread thr_created_1") {
		t.Fatalf("%+v %q", res, c.sent)
	}
}
