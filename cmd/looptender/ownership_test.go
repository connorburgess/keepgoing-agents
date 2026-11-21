//go:build unix

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/session"
	"github.com/looptender/looptender/internal/testexec"
)

func ownerControl(t *testing.T, f *fixture) control.File {
	t.Helper()
	c, err := control.Load(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOwnerConflictsAndExplicitTransfer(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	f.run("pause")
	f.run("stop")
	f.run("complete")
	f.run("await-user", "--note", "Fictional hold")
	before, _ := os.ReadFile(filepath.Join(f.dir, "state", control.FileName))
	calls := f.read("calls")
	t.Setenv("BB_THREAD_ID", "thr_second")
	for _, args := range [][]string{{"validate"}, {"enable"}, {"daemon"}, {"tick"}, {"retarget", "primary"}, {"fallbacks", "check"}} {
		code, _, errs := f.run(args...)
		if code != exitFailed || !strings.Contains(errs, "thr_first") || !strings.Contains(errs, "--take-over") {
			t.Fatalf("%v: code=%d error=%s", args, code, errs)
		}
		after, _ := os.ReadFile(filepath.Join(f.dir, "state", control.FileName))
		if string(after) != string(before) {
			t.Fatalf("%v changed control", args)
		}
	}
	if f.read("calls") != calls || f.read("probes") != "" {
		t.Fatal("ownership check queried provider or probed")
	}
	if code, _, errs := f.run("validate", "--take-over"); code != 0 {
		t.Fatal(errs)
	}
	after, _ := os.ReadFile(filepath.Join(f.dir, "state", control.FileName))
	if string(after) != string(before) {
		t.Fatal("validation claimed owner")
	}
	if code, _, errs := f.run("enable", "--take-over"); code != 0 {
		t.Fatal(errs)
	}
	c := ownerControl(t, f)
	if c.Ownership.ThreadID != "thr_second" || c.Ownership.Generation != 2 || c.Ownership.Previous.ThreadID != "thr_first" || c.Ownership.Previous.Status != "handed_off" || !c.Paused || !c.Stop || !c.Completed || !c.AwaitingUser || c.Note != "Fictional hold" {
		t.Fatalf("%+v", c)
	}
	if code, out, _ := f.run("status"); code != 0 || !strings.Contains(out, "thr_second") || !strings.Contains(out, "handed_off") {
		t.Fatal(out)
	}
	// Restrictive controls remain available from any thread.
	t.Setenv("BB_THREAD_ID", "thr_first")
	for _, cmd := range []string{"pause", "stop", "complete", "await-user", "disable"} {
		if code, _, e := f.run(cmd); code != 0 {
			t.Fatalf("%s: %s", cmd, e)
		}
	}
}

func TestOwnerSchedulersLegacyAndFlagValidation(t *testing.T) {
	f := newFixture(t)
	if code, _, e := f.run("validate"); code != 0 {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state")); !os.IsNotExist(err) {
		t.Fatal("validation created runtime state")
	}
	f.run("enable")
	if ownerControl(t, f).Ownership != nil {
		t.Fatal("anonymous enable invented owner")
	}
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable")
	f.run("complete")
	t.Setenv("BB_THREAD_ID", "")
	for _, cmd := range []string{"validate", "enable", "tick"} {
		if code, _, e := f.run(cmd); code != 0 {
			t.Fatalf("scheduler %s: %s", cmd, e)
		}
	}
	if ownerControl(t, f).Ownership.ThreadID != "thr_first" {
		t.Fatal("scheduler replaced owner")
	}
	if code, _, _ := f.run("enable", "--take-over"); code == 0 {
		t.Fatal("anonymous takeover allowed")
	}
	for _, args := range [][]string{{"tick", "--take-over"}, {"resume", "--take-over"}, {"daemon", "--dry-run", "--take-over"}} {
		if code, _, _ := f.run(args...); code != exitUsage {
			t.Fatalf("invalid flags: %v %d", args, code)
		}
	}
	t.Setenv("BB_THREAD_ID", "private fixture value")
	if code, _, e := f.run("enable"); code != exitFailed || strings.Contains(e, "private fixture value") {
		t.Fatalf("invalid identity: %d %s", code, e)
	}
}

func TestOwnerSharedStateAliasesAndLocks(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable")
	cfg := loadFixture(t, f.cfg)
	lease := leaseOf(ownerControl(t, f))
	t.Setenv("BB_THREAD_ID", "thr_second")
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.run("enable", "--take-over"); code != exitSkipped {
		t.Fatal("takeover bypassed tick lock")
	}
	l.Release()
	// A second config path sharing state has the same owner.
	copyPath := filepath.Join(f.dir, "second.yaml")
	b, _ := os.ReadFile(f.cfg)
	write(t, copyPath, string(b), 0600)
	var errs bytes.Buffer
	if code := run(context.Background(), []string{"enable", "-c", copyPath}, io.Discard, &errs); code != exitFailed {
		t.Fatalf("alias accepted: %s", errs.String())
	}
	// Directory symlinks share the OS locks and the ownership record.
	alias := filepath.Join(f.dir, "alias-state")
	if err := os.Symlink(cfg.Loop.StateDir, alias); err != nil {
		t.Fatal(err)
	}
	write(t, copyPath, strings.Replace(string(b), "state_dir: state", "state_dir: alias-state", 1), 0600)
	if code := run(context.Background(), []string{"enable", "-c", copyPath}, io.Discard, io.Discard); code != exitFailed {
		t.Fatal("symlink directory bypassed owner")
	}
	if code, _, e := f.run("enable", "--take-over"); code != 0 {
		t.Fatal(e)
	}
	calls := f.read("calls")
	if tickOnce(env{context.Background(), io.Discard, io.Discard}, cfg, time.Now(), false, false, lease) != exitFailed || pollWake(context.Background(), cfg, lease) != true {
		t.Fatal("stale daemon lease accepted")
	}
	if f.read("calls") != calls {
		t.Fatal("stale daemon queried provider")
	}
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable", "--take-over")
	if checkOwnership(cfg.Loop.StateDir, lease) == nil {
		t.Fatal("A to B to A revived stale daemon")
	}
}

func TestOwnerControlFileAliasRefused(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable")
	dir := filepath.Join(f.dir, "aliased")
	os.Mkdir(dir, 0700)
	if err := os.Symlink(filepath.Join(f.dir, "state", control.FileName), filepath.Join(dir, control.FileName)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(b), "state_dir: state", "state_dir: aliased", 1), 0600)
	for _, cmd := range []string{"validate", "enable", "daemon", "tick"} {
		if code, _, e := f.run(cmd); code != exitFailed || !strings.Contains(e, "symlink") {
			t.Fatalf("%s: %d %s", cmd, code, e)
		}
	}
}

func TestRunningDaemonExitsAfterOwnershipTransfer(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		t.Run(map[bool]string{false: "BB_owner", true: "scheduler"}[anonymous], func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("BB_THREAD_ID", "thr_first")
			f.run("enable")
			f.run("complete")
			if anonymous {
				t.Setenv("BB_THREAD_ID", "")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, io.Discard, io.Discard) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
				}
			})
			cfg := loadFixture(t, f.cfg)
			deadline := time.Now().Add(3 * time.Second)
			for inspectDaemon(context.Background(), cfg.Loop.StateDir, version, false).Phase != "waiting" {
				if time.Now().After(deadline) {
					t.Fatal("daemon did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Setenv("BB_THREAD_ID", "thr_second")
			// A daemon takeover cannot steal the existing process lock or change owner.
			if code, _, _ := f.run("daemon", "--take-over"); code != exitSkipped {
				t.Fatal("second daemon bypassed process lock")
			}
			if ownerControl(t, f).Ownership.ThreadID != "thr_first" {
				t.Fatal("refused daemon changed owner")
			}
			for {
				code, _, e := f.run("enable", "--take-over")
				if code == 0 {
					break
				}
				if code != exitSkipped || time.Now().After(deadline) {
					t.Fatal(e)
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case code := <-done:
				if code != exitFailed {
					t.Fatalf("old daemon exit=%d", code)
				}
				done <- code
			case <-time.After(3 * time.Second):
				t.Fatal("old daemon survived takeover")
			}
			if f.read("calls") != "" || !ownerControl(t, f).Completed {
				t.Fatal("takeover disturbed held target")
			}
		})
	}
}

func TestOwnerCommandHelper(t *testing.T) {
	if os.Getenv("KEEPGOING_OWNER_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	os.Exit(run(context.Background(), []string{"enable", "-c", os.Getenv("KEEPGOING_OWNER_CONFIG")}, os.Stdout, os.Stderr))
}

func TestCompetingOwnerProcessesHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	alias := filepath.Join(f.dir, "second.yaml")
	b, _ := os.ReadFile(f.cfg)
	write(t, alias, string(b), 0600)
	cmds := []*exec.Cmd{}
	for n, id := range []string{"thr_first", "thr_second"} {
		c := exec.Command(os.Args[0], "-test.run=^TestOwnerCommandHelper$")
		cfg := f.cfg
		if n == 1 {
			cfg = alias
		}
		c.Env = append(os.Environ(), "KEEPGOING_OWNER_HELPER=1", "KEEPGOING_OWNER_CONFIG="+cfg, "BB_THREAD_ID="+id)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	successes := 0
	for _, c := range cmds {
		if err := c.Wait(); err == nil {
			successes++
		} else if c.ProcessState.ExitCode() != exitSkipped && c.ProcessState.ExitCode() != exitFailed {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful claims=%d", successes)
	}
	o := ownerControl(t, f).Ownership
	if o == nil || o.Generation != 1 || o.Previous != nil {
		t.Fatalf("%+v", o)
	}
	if f.read("calls") != "" {
		t.Fatal("claim queried provider")
	}
}

func TestDaemonTakeoverPreservesDisabledAndCompleted(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable")
	f.run("disable")
	f.run("complete")
	t.Setenv("BB_THREAD_ID", "thr_second")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if code := run(ctx, []string{"daemon", "--take-over", "-c", f.cfg}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("daemon takeover exit=%d", code)
	}
	c := ownerControl(t, f)
	if c.Enabled || !c.Completed || c.Ownership.ThreadID != "thr_second" || c.Ownership.Previous.ThreadID != "thr_first" {
		t.Fatalf("%+v", c)
	}
	if f.read("calls") != "" {
		t.Fatal("takeover queried held provider")
	}
}

func TestTickControlContentionIsRetryable(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.interactions"), `[{"threadId":"thr_child","status":"pending"}]`, 0600)
	l, err := lock.AcquireNamed(filepath.Join(f.dir, "state"), lock.Control)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.run("tick"); code != exitSkipped {
		t.Fatalf("control contention exit=%d", code)
	}
	l.Release()
	if ownerControl(t, f).AwaitingUser || f.read("thr_primary.told") != "" {
		t.Fatal("failed hold changed provider or controls")
	}
	if r := f.tick(); r["action"] != "await_user" || !ownerControl(t, f).AwaitingUser {
		t.Fatal("retry did not save hold")
	}
}

func TestEnableTakeoverPreservesDisabled(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	f.run("enable")
	f.run("disable")
	f.run("pause")
	t.Setenv("BB_THREAD_ID", "thr_second")
	if code, _, e := f.run("enable", "--take-over"); code != 0 {
		t.Fatal(e)
	}
	c := ownerControl(t, f)
	if c.Enabled || !c.Paused || c.Ownership.ThreadID != "thr_second" {
		t.Fatal("takeover reopened held loop")
	}
	if code, _, e := f.run("enable"); code != 0 {
		t.Fatal(e)
	}
	if c = ownerControl(t, f); !c.Enabled || !c.Paused || c.Ownership.Generation != 2 {
		t.Fatal("same owner enable did not preserve pause and generation")
	}
}

// takeoverFixture is a loop owned by thr_first, with the check lock held
// by this test as the old owner's supervisor holds it during a check, and
// the caller switched to thr_second.
func takeoverFixture(t *testing.T) (*fixture, *lock.Lock) {
	t.Helper()
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	held, err := lock.Acquire(filepath.Join(f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Release() })
	t.Setenv("BB_THREAD_ID", "thr_second")
	t.Cleanup(func() { lockRetried = nil })
	return f, held
}

// enable --take-over waits for the check lock instead of being skipped:
// the lock is released while it waits, and the takeover happens.
func TestTakeoverWaitsForTheCheckLock(t *testing.T) {
	f, held := takeoverFixture(t)
	waits := 0
	lockRetried = func() {
		if waits++; waits == 1 {
			held.Release()
		}
	}
	if code, _, errs := f.run("enable", "--take-over"); code != 0 || waits != 1 {
		t.Fatalf("exit %d after %d waits: %s", code, waits, errs)
	}
	if c := ownerControl(t, f); c.Ownership == nil || c.Ownership.ThreadID != "thr_second" {
		t.Fatalf("%+v", c.Ownership)
	}
}

// A lock held for the whole wait ends it: exit 75, nothing taken over, and
// the message says the old supervisor is busy and to run it again.
func TestTakeoverGivesUpOnALockHeldForTheWholeWait(t *testing.T) {
	f, _ := takeoverFixture(t)
	old := takeoverLockWait
	takeoverLockWait = 300 * time.Millisecond
	t.Cleanup(func() { takeoverLockWait = old })
	before, _ := os.ReadFile(filepath.Join(f.dir, "state", control.FileName))
	start := time.Now()
	code, _, errs := f.run("enable", "--take-over")
	if took := time.Since(start); code != exitSkipped || took < takeoverLockWait ||
		!strings.Contains(errs, "held this loop's check lock for all of 300ms, so nothing was taken over") || !strings.Contains(errs, "run enable --take-over again") {
		t.Fatalf("exit %d after %s: %s", code, took, errs)
	}
	if after, _ := os.ReadFile(filepath.Join(f.dir, "state", control.FileName)); string(after) != string(before) {
		t.Fatal("a takeover that gave up changed control")
	}
}

// Only a takeover waits: an ordinary enable or resume is still skipped at
// once while a check holds the lock.
func TestOnlyATakeoverWaitsForTheCheckLock(t *testing.T) {
	f, _ := takeoverFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_first")
	lockRetried = func() { t.Error("waited for the lock") }
	for _, cmd := range []string{"enable", "resume"} {
		if code, _, errs := f.run(cmd); code != exitSkipped || !strings.Contains(errs, "lock held by another process") {
			t.Fatalf("%s: exit %d: %s", cmd, code, errs)
		}
	}
}

func TestSupervisedSessionCannotClearItsOwnHold(t *testing.T) {
	f := newFixture(t)
	state := filepath.Join(f.dir, "state")
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	if c, _, e := f.run("await-user", "--note", "Fictional question"); c != 0 {
		t.Fatal(e)
	}
	held, _ := os.ReadFile(filepath.Join(state, control.FileName))
	refused := func(why string, args ...string) {
		t.Helper()
		code, _, errs := f.run(args...)
		after, _ := os.ReadFile(filepath.Join(state, control.FileName))
		if code != exitFailed || !strings.Contains(errs, "supervised session") || string(after) != string(held) {
			t.Fatalf("%s %v: code=%d error=%s", why, args, code, errs)
		}
	}
	// The target's own BB thread, with no owner recorded.
	t.Setenv("BB_THREAD_ID", "thr_primary")
	refused("target thread", "resume")
	refused("target thread", "retarget", "primary")
	// A process this loop's supervisor started.
	t.Setenv("BB_THREAD_ID", "")
	t.Setenv(control.SupervisedLoopVar, state)
	refused("supervised process", "resume")
	refused("supervised process", "retarget", "primary")
	// Another loop's marker is no reason to refuse; a person at a terminal resumes.
	t.Setenv(control.SupervisedLoopVar, t.TempDir())
	if code, _, e := f.run("resume"); code != 0 {
		t.Fatal(e)
	}
	t.Setenv(control.SupervisedLoopVar, "")

	// The target thread also owns the loop: still refused, and a person
	// at a terminal still resumes.
	t.Setenv("BB_THREAD_ID", "thr_primary")
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	f.run("pause")
	held, _ = os.ReadFile(filepath.Join(state, control.FileName))
	refused("owning target thread", "resume")
	t.Setenv("BB_THREAD_ID", "")
	if code, _, e := f.run("resume"); code != 0 || ownerControl(t, f).Paused {
		t.Fatal(e)
	}

	// An operator thread that is not the target resumes as before.
	t.Setenv("BB_THREAD_ID", "thr_operator")
	if c, _, e := f.run("enable", "--take-over"); c != 0 {
		t.Fatal(e)
	}
	f.run("pause")
	if code, _, e := f.run("resume"); code != 0 || ownerControl(t, f).Paused {
		t.Fatal(e)
	}
}

func TestCheckMarksWhatItStartsAsSupervised(t *testing.T) {
	f := newFixture(t)
	t.Setenv(control.SupervisedLoopVar, "")
	testexec.Shared(t, filepath.Join(f.dir, "bin", "bb"), strings.Replace(fakeBB, "\n", "\necho \"$"+control.SupervisedLoopVar+"\" >> \"$FAKE_BB/marked\"\n", 1))
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	if c, _, e := f.run("tick"); c != 0 {
		t.Fatal(e)
	}
	want := session.CleanPath(filepath.Join(f.dir, "state"))
	lines := strings.Fields(f.read("marked"))
	if len(lines) == 0 || !slices.Contains(lines, want) || slices.ContainsFunc(lines, func(l string) bool { return l != want }) {
		t.Fatalf("bb started by the check saw %q, want %q", lines, want)
	}
	if os.Getenv(control.SupervisedLoopVar) != "" {
		t.Fatal("the check left its marker in the caller's environment")
	}
}

// Between checks too, a daemon carries its loop's marker, so what it starts
// outside a check (control polls, a stop from the last configuration) is
// marked; it puts the caller's environment back when it ends.
func TestDaemonMarksItselfForItsLifetime(t *testing.T) {
	f := newFixture(t)
	t.Setenv(control.SupervisedLoopVar, "")
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, io.Discard, io.Discard) }()
	cfg := loadFixture(t, f.cfg)
	want := session.CleanPath(cfg.Loop.StateDir)
	deadline := time.Now().Add(5 * time.Second)
	for inspectDaemon(context.Background(), cfg.Loop.StateDir, version, false).Phase != "waiting" || os.Getenv(control.SupervisedLoopVar) != want {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("waiting daemon is not marked: %q", os.Getenv(control.SupervisedLoopVar))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if os.Getenv(control.SupervisedLoopVar) != "" {
		t.Fatal("the daemon left its marker behind")
	}
}

func TestResumeOwnerConflictGivesPersonRecoveryWithoutTransfer(t *testing.T) {
	f := newFixture(t)
	t.Setenv(control.SupervisedLoopVar, "")
	t.Setenv("BB_THREAD_ID", "thr_primary")
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	if code, _, errs := f.run("await-user", "--note", "Fictional permission request"); code != exitOK {
		t.Fatal(errs)
	}
	path := filepath.Join(f.dir, "state", control.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BB_THREAD_ID", "thr_other")
	code, _, errs := f.run("resume")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitFailed || !strings.Contains(errs, "BB_THREAD_ID") || !strings.Contains(errs, "review Resume") || strings.Contains(errs, "--take-over") || !bytes.Equal(before, after) {
		t.Fatalf("conflicting resume: code=%d error=%s changed=%t", code, errs, !bytes.Equal(before, after))
	}
	// An ordinary person terminal can resume without transferring the claim.
	t.Setenv("BB_THREAD_ID", "")
	if code, _, errs := f.run("resume"); code != exitOK {
		t.Fatal(errs)
	}
	ctl := ownerControl(t, f)
	if ctl.AwaitingUser || ctl.Note != "" || ctl.Ownership.ThreadID != "thr_primary" || ctl.Ownership.Generation != 1 {
		t.Fatalf("resume changed ownership or retained hold: %+v", ctl)
	}
}
