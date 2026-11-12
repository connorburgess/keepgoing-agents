package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// helperCLI returns argv that re-runs this test binary as a fake agent CLI.
// It reads the prompt from stdin, echoes a fixed result, and exits with code.
func helperCLI(t *testing.T, stdout string, code int, sleep time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "fake.bat")
		body := "@echo off\r\nset HELPER_STDOUT_FILE=" + filepath.Join(dir, "out.txt") + "\r\n"
		os.WriteFile(filepath.Join(dir, "out.txt"), []byte(stdout), 0o600)
		body += "more > \"" + filepath.Join(dir, "prompt.txt") + "\"\r\n"
		if sleep > 0 {
			body += "ping -n " + itoa(int(sleep.Seconds())+1) + " 127.0.0.1 > nul\r\n"
		}
		body += "type \"%HELPER_STDOUT_FILE%\"\r\nexit /b " + itoa(code) + "\r\n"
		os.WriteFile(p, []byte(body), 0o755)
		return p
	}
	p := filepath.Join(dir, "fake.sh")
	os.WriteFile(filepath.Join(dir, "out.txt"), []byte(stdout), 0o600)
	body := "#!/bin/sh\ncat > \"" + filepath.Join(dir, "prompt.txt") + "\"\n"
	if sleep > 0 {
		body += "sleep " + itoa(int(sleep.Seconds())) + "\n"
	}
	body += "cat \"" + filepath.Join(dir, "out.txt") + "\"\nexit " + itoa(code) + "\n"
	testexec.Script(t, p, body)
	return p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func waitFor(t *testing.T, d Dir, want Phase) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := d.Status(); st.Phase == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := d.Status()
	t.Fatalf("phase %s never reached; at %s", want, st.Phase)
}

// These run on every platform, so the Windows wrapper path is covered.
func TestTurnLifecycleOnThisPlatform(t *testing.T) {
	dir := t.TempDir()
	out := `{"type":"result","subtype":"success","is_error":false,"session_id":"00000000-0000-4000-8000-0000000000aa","result":"done"}`
	c := NewClaude(config.Claude{CLI: helperCLI(t, out, 0, 0), Workdir: dir}, filepath.Join(dir, "target"))
	if err := c.Send(context.Background(), "go", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c.dir, Finished)
	obs, _ := c.Observe(context.Background())
	if obs.State != decide.Idle || c.dir.Session() != "00000000-0000-4000-8000-0000000000aa" {
		t.Errorf("obs=%+v session=%q", obs, c.dir.Session())
	}
}

func TestTurnErrorAndStopOnThisPlatform(t *testing.T) {
	dir := t.TempDir()
	limited := `{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429}`
	c := NewClaude(config.Claude{CLI: helperCLI(t, limited, 1, 0), Workdir: dir}, filepath.Join(dir, "target"))
	c.Send(context.Background(), "go", adapter.ModeAuto)
	waitFor(t, c.dir, Finished)
	if obs, _ := c.Observe(context.Background()); obs.ErrorClass != decide.ErrRateLimit {
		t.Errorf("%+v", obs)
	}
	dir2 := t.TempDir()
	slow := NewClaude(config.Claude{CLI: helperCLI(t, "{}", 0, 30*time.Second), Workdir: dir2}, filepath.Join(dir2, "target"))
	slow.Send(context.Background(), "go", adapter.ModeAuto)
	waitFor(t, slow.dir, Running)
	if err := slow.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		st, _ := slow.dir.Status()
		if st.Phase != Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn still running after stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWrapperDoesNotLeakItsVariables(t *testing.T) {
	cmd, err := wrapperCommand([]string{"x"}, ".", "/fictional/dir", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, envTurnDir+"=") || strings.HasPrefix(kv, envTurnArgv+"=") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("wrapper selection variables missing: %d", found)
	}
}
