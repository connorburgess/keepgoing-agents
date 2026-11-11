package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// script writes a test command: a shell script on Unix, a batch file on
// Windows. Each test passes both bodies.
func script(t *testing.T, sh, bat string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "cmd.bat")
		if err := os.WriteFile(p, []byte("@echo off\r\n"+bat+"\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := filepath.Join(dir, "cmd.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+sh+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestObserveJSON(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo '{"state":"active","background":2}'`, `echo {"state":"active","background":2}`)}})
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Active || obs.Background != 2 {
		t.Errorf("%+v %v", obs, err)
	}
}

func TestObserveAwaitingInput(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo '{"state":"idle","awaitingInput":true}'`, `echo {"state":"idle","awaitingInput":true}`)}})
	if obs, err := a.Observe(context.Background()); err != nil || !obs.AwaitingInput {
		t.Errorf("%+v %v", obs, err)
	}
}

func TestObserveExitCodeMap(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo "quota exhausted" >&2; exit 75`, "echo quota exhausted 1>&2\r\nexit /b 75")}, ExitCodes: map[int]string{75: "rate_limit"}})
	obs, err := a.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrRateLimit {
		t.Errorf("%+v %v", obs, err)
	}
	a = New(config.Process{StatusCommand: []string{script(t, `exit 9`, "exit /b 9")}})
	obs, _ = a.Observe(context.Background())
	if obs.ErrorClass != decide.ErrUnknown {
		t.Errorf("unmapped exit should be unknown, got %s", obs.ErrorClass)
	}
}

func TestObserveBadJSONIsUnknown(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, `echo "all good, rate limit fine"`, "echo all good, rate limit fine")}})
	obs, err := a.Observe(context.Background())
	if err == nil || obs.State != decide.Unknown {
		t.Errorf("%+v %v", obs, err)
	}
}

func TestSendPassesPromptAndMode(t *testing.T) {
	out := filepath.Join(t.TempDir(), "got.txt")
	a := New(config.Process{ResumeCommand: []string{script(t, `cat > "$OUT"; echo "$KEEPGOING_MODE" >> "$OUT"`, "findstr \"^\" > \"%OUT%\"\r\necho %KEEPGOING_MODE%>> \"%OUT%\"")}, Env: map[string]string{"OUT": out}})
	if err := a.Send(context.Background(), "keep going", adapter.ModeQueue); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	got := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r", ""), "\n", "")
	if got != "keep goingqueue" {
		t.Errorf("got %q", b)
	}
}

func TestStopUnsupported(t *testing.T) {
	a := New(config.Process{})
	if _, ok := a.Stop(context.Background()).(adapter.ErrUnsupported); !ok {
		t.Error("expected ErrUnsupported")
	}
}

func TestEvidenceDistinguishesExitAndJSON(t *testing.T) {
	a := New(config.Process{StatusCommand: []string{script(t, "exit 9", "exit /b 9")}})
	o, err := a.Observe(context.Background())
	if err != nil || o.Evidence.Source != "process_exit" || o.Evidence.ExitCode == nil || *o.Evidence.ExitCode != 9 || o.ErrorClass != decide.ErrUnknown {
		t.Fatal(o, err)
	}
	a = New(config.Process{StatusCommand: []string{script(t, `echo '{"state":"error","errorClass":"connection_error","text":"SECRET_BODY"}'`, `echo {"state":"error","errorClass":"connection_error","text":"SECRET_BODY"}`)}})
	o, err = a.Observe(context.Background())
	if err != nil || o.Evidence.Source != "process_json" || o.Evidence.Code != "connection_error" || o.Evidence.ExitCode == nil || *o.Evidence.ExitCode != 0 {
		t.Fatal(o, err)
	}
}
