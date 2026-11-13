package runner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

func TestCursorRecoversInitializedSessionAfterWrapperCrash(t *testing.T) {
	root := t.TempDir()
	dir := Dir(filepath.Join(root, "target"))
	if err := os.Mkdir(string(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.path("prompt"), []byte("fictional interrupted task"), 0600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	key, err := randomStopID()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "child-ready")
	cmd, err := wrapperCommand([]string{self, "-p", "--output-format", "stream-json"}, root, string(dir), []string{envTurnKey + "=" + key, "KEEPGOING_TEST_HELPER=cursor-crash", "KEEPGOING_TEST_RESULT=" + ready}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	childPID := 0
	cleanup := func() bool {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			reaped = true
		}
		_ = os.WriteFile(ready+".release", nil, 0600)
		// This is only an existence check; no saved PID is ever signalled.
		deadline := time.Now().Add(15 * time.Second)
		for childPID != 0 && alive(childPID) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if childPID != 0 && alive(childPID) {
			return false
		}
		childPID = 0
		return true
	}
	t.Cleanup(func() {
		if !cleanup() {
			t.Error("disposable crash child failed to exit during cleanup")
		}
	})
	saveStopRecord(t, dir, Record{PID: cmd.Process.Pid, StopKey: key, StartedAt: time.Now().UTC(), Argv0: filepath.Base(self)})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(ready)
		if err == nil {
			childPID, _ = strconv.Atoi(string(b))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("fixture never initialized")
	}
	// Retain the handle from Start until Kill/Wait; do not look up run.json's PID.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reaped = true
	if !cleanup() {
		t.Fatal("disposable crash child failed to exit; refusing resume")
	}
	st, err := dir.Status()
	if err != nil || st.Phase != Crashed || st.ExitCodeKnown {
		t.Fatalf("crash status %+v: %v", st, err)
	}
	if _, err := os.Stat(dir.path("exit")); !os.IsNotExist(err) {
		t.Fatalf("wrapper crash created terminal exit: %v", err)
	}
	capture := filepath.Join(root, "resumed.json")
	cfg := config.Cursor{CLI: self, Workdir: root, Env: map[string]string{"KEEPGOING_TEST_HELPER": "cursor", "KEEPGOING_TEST_RESULT": capture, "KEEPGOING_CURSOR_FIXTURE_STDOUT": cursorSuccess}}
	restarted := NewCursor(cfg, string(dir))
	obs, err := restarted.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown || dir.Session() != cursorSession {
		t.Fatalf("reconstructed observation %+v %v, session %q", obs, err, dir.Session())
	}
	t.Cleanup(func() { _ = restarted.Stop(context.Background()) })
	// Reconstruct once more after observation persisted the verified identity.
	restarted = NewCursor(cfg, string(dir))
	if err := restarted.Send(context.Background(), "fictional retry", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	got := readCursorCapture(t, capture)
	if !reflect.DeepEqual(got.Args, []string{"-p", "--output-format", "stream-json", "--resume=" + cursorSession}) {
		t.Fatalf("crash recovery argv: %q", got.Args)
	}
	waitFor(t, dir, Finished)
	obs, err = restarted.Observe(context.Background())
	if err != nil || obs.State != decide.Idle {
		t.Fatalf("recovery %+v %v", obs, err)
	}
}
