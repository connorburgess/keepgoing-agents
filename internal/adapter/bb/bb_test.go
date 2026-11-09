package bb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func fake(responses map[string]string, fail map[string]bool) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		key := strings.Join(args[:2], " ")
		if fail[key] {
			return []byte(responses[key]), errors.New("exit 1")
		}
		return []byte(responses[key]), nil
	}
}

func TestObserveIdle(t *testing.T) {
	a := New("bb", "thr_example123")
	a.run = fake(map[string]string{
		"thread show": `{"thread":{"status":"idle","queuedMessageCount":1,"activeBackgroundAgentCount":0,"archivedAt":null,"deletedAt":null}}`,
	}, nil)
	obs, err := a.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != decide.Idle || obs.Queued != 1 || obs.Gone {
		t.Errorf("obs = %+v", obs)
	}
}

func TestObserveErrorClassifiesCodeNotProse(t *testing.T) {
	a := New("bb", "thr_example123")
	a.run = fake(map[string]string{
		"thread show": `{"thread":{"status":"error"}}`,
		"thread log":  `[{"type":"message","data":{"text":"usage limit reached"}},{"type":"turn_failed","data":{"error":{"code":"rate_limit","message":"Usage limit reached"}}}]`,
	}, nil)
	obs, _ := a.Observe(context.Background())
	if obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit {
		t.Errorf("obs = %+v", obs)
	}
	// Same words in prose only, no code: must be unknown.
	a.run = fake(map[string]string{
		"thread show": `{"thread":{"status":"error"}}`,
		"thread log":  `[{"type":"turn_failed","data":{"message":"usage limit reached, 429"}}]`,
	}, nil)
	obs, _ = a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("prose classified as %s", obs.ErrorClass)
	}
}

func TestObserveGoneAndArchived(t *testing.T) {
	a := New("bb", "thr_example123")
	a.run = fake(map[string]string{"thread show": `{"ok":false,"error":{"code":"thread_not_found","message":"no"}}`}, map[string]bool{"thread show": true})
	obs, err := a.Observe(context.Background())
	if err != nil || !obs.Gone {
		t.Errorf("not found: %+v %v", obs, err)
	}
	a.run = fake(map[string]string{"thread show": `{"thread":{"status":"idle","archivedAt":1794000000000}}`}, nil)
	obs, _ = a.Observe(context.Background())
	if !obs.Gone {
		t.Error("archived thread should be gone")
	}
}

func TestObserveCLIFailureIsUnknown(t *testing.T) {
	a := New("bb", "thr_example123")
	a.run = fake(map[string]string{"thread show": ``}, map[string]bool{"thread show": true})
	obs, err := a.Observe(context.Background())
	if err == nil || obs.State != decide.Unknown {
		t.Errorf("cli failure: %+v %v", obs, err)
	}
}

func TestSendUsesStdinAndMode(t *testing.T) {
	a := New("bb", "thr_example123")
	var gotStdin string
	var gotArgs []string
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		gotStdin, gotArgs = stdin, args
		return []byte(`{"ok":true}`), nil
	}
	if err := a.Send(context.Background(), "keep going", adapter.ModeQueue); err != nil {
		t.Fatal(err)
	}
	want := "thread tell thr_example123 --message-file - --mode queue --json"
	if gotStdin != "keep going" || strings.Join(gotArgs, " ") != want {
		t.Errorf("stdin=%q args=%q", gotStdin, gotArgs)
	}
	a.run = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"thread_not_found","message":"x"}}`), errors.New("exit 1")
	}
	if err := a.Send(context.Background(), "x", adapter.ModeAuto); err == nil || !strings.Contains(err.Error(), "thread_not_found") {
		t.Errorf("err = %v", err)
	}
}
