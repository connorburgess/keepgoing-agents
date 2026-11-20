package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/control"
)

func TestSessionClearUnconfirmedAntigravity(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "looptender.yaml")
	cfgYAML := "version: 1\nloop:\n  name: example-antigravity\n  state_dir: " + stateDir + "\n  adapter: antigravity\n  antigravity:\n    workdir: " + project + "\n  prompt: Fictional task.\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Set a hold so we verify holds are preserved.
	ctl := control.File{Enabled: false, AwaitingUser: true}
	if err := control.Save(stateDir, ctl); err != nil {
		t.Fatal(err)
	}

	targetDir := filepath.Join(stateDir, "targets", "primary")
	turnID := strings.Repeat("0123456789abcdef", 4)
	stopKey := strings.Repeat("fedcba9876543210", 4)
	turnDir := filepath.Join(targetDir, "acp-turns", turnID)
	if err := os.MkdirAll(turnDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionBytes := []byte(`{"version":1,"sessionId":"fictional-session-1"}` + "\n")
	if err := os.WriteFile(filepath.Join(targetDir, "acp-session.json"), sessionBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "acp-turn.json"), []byte(`{"version":1,"turn":"`+turnID+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(turnDir, "acp-pending.json"), []byte(`{"version":1,"turn":"`+turnID+`","stopKey":"`+stopKey+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// --dry-run is refused without writing acp-cleared.json.
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"session", "clear-unconfirmed", "--dry-run", "-c", cfgPath}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "does not support --dry-run") {
		t.Fatalf("dry-run: code=%d err=%q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(turnDir, "acp-cleared.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote acp-cleared.json")
	}

	// Supervised process cannot clear its own unconfirmed turn.
	t.Setenv(control.SupervisedLoopVar, stateDir)
	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"session", "clear-unconfirmed", "-c", cfgPath}, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("supervised caller: code=%d err=%q", code, errOut.String())
	}
	t.Setenv(control.SupervisedLoopVar, "")

	// Successful JSON clear writes acp-cleared.json and preserves session and holds.
	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"session", "clear-unconfirmed", "--json", "-c", cfgPath}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("clear-unconfirmed: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	var report sessionClearUnconfirmedReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out.String())
	}
	if report.Type != "session_clear_unconfirmed" || report.Loop != "example-antigravity" || report.Target != "primary" || report.By != "terminal" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.Holds) != 2 || report.Holds[0] != "disabled" || report.Holds[1] != "awaitingUser" {
		t.Fatalf("unexpected holds: %+v", report.Holds)
	}
	if _, err := os.Stat(filepath.Join(turnDir, "acp-cleared.json")); err != nil {
		t.Fatalf("expected acp-cleared.json: %v", err)
	}
	gotSession, err := os.ReadFile(filepath.Join(targetDir, "acp-session.json"))
	if err != nil || !bytes.Equal(gotSession, sessionBytes) {
		t.Fatalf("acp-session.json was modified or lost: %q %v", gotSession, err)
	}
	gotCtl, err := control.Load(stateDir)
	if err != nil || gotCtl.Enabled || !gotCtl.AwaitingUser {
		t.Fatalf("holds were not preserved: %+v %v", gotCtl, err)
	}

	// Idempotent second call succeeds in text mode.
	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"session", "clear-unconfirmed", "-c", cfgPath}, &out, &errOut)
	if code != exitOK || !strings.Contains(out.String(), "session clear-unconfirmed") {
		t.Fatalf("second clear-unconfirmed: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestSessionClearUnconfirmedRejectsNonAntigravityAndFreshTarget(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	codexCfg := filepath.Join(dir, "codex.yaml")
	if err := os.WriteFile(codexCfg, []byte("version: 1\nloop:\n  name: example-codex\n  state_dir: "+stateDir+"\n  adapter: codex\n  codex:\n    workdir: "+project+"\n  prompt: Fictional task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"session", "clear-unconfirmed", "-c", codexCfg}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "applies to an Antigravity target") {
		t.Fatalf("non-antigravity: code=%d err=%q", code, errOut.String())
	}

	freshState := filepath.Join(dir, "fresh-state")
	if err := os.MkdirAll(freshState, 0o700); err != nil {
		t.Fatal(err)
	}
	agyCfg := filepath.Join(dir, "agy.yaml")
	if err := os.WriteFile(agyCfg, []byte("version: 1\nloop:\n  name: example-fresh\n  state_dir: "+freshState+"\n  adapter: antigravity\n  antigravity:\n    workdir: "+project+"\n  prompt: Fictional task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"session", "clear-unconfirmed", "-c", agyCfg}, &out, &errOut)
	if code != exitFailed {
		t.Fatalf("fresh target: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}
