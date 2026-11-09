package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cmd.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestObserveJSON(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo '{"state":"active","background":2}'`)}})
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Active || obs.Background != 2 {
		t.Errorf("%+v %v", obs, err)
	}
}

func TestObserveExitCodeMap(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo "quota exhausted" >&2; exit 75`)}, ExitCodes: map[int]string{75: "rate_limit"}})
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit {
		t.Errorf("%+v %v", obs, err)
	}
	a = New(config.Process{StatusCommand: []string{script(t, `exit 9`)}})
	obs, _ = a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("unmapped exit should be unknown, got %s", obs.ErrorClass)
	}
}

func TestObserveBadJSONIsUnknown(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo "all good, rate limit fine"`)}})
	obs, err := a.Observe(context.Background())
	if err == nil || obs.State != decide.Unknown {
		t.Errorf("%+v %v", obs, err)
	}
}

func TestSendPassesPromptAndMode(t *testing.T) {
	out := filepath.Join(t.TempDir(), "got.txt")
	a := New(config.Process{ResumeCommand: []string{script(t, `cat > "$OUT"; echo "$KEEPGOING_MODE" >> "$OUT"`)}, Env: map[string]string{"OUT": out}})
	if err := a.Send(context.Background(), "keep going", adapter.ModeQueue); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "keep goingqueue\n" {
		t.Errorf("got %q", b)
	}
}

func TestStopUnsupported(t *testing.T) {
	a := New(config.Process{})
	if _, ok := a.Stop(context.Background()).(adapter.ErrUnsupported); !ok {
		t.Error("expected ErrUnsupported")
	}
}
