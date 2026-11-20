//go:build windows

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Fixture cleanup must join exit separately from Finished: an owned
// deliberately lingering wrapper retains its cwd
// after logical completion. Its process handle, rather than its saved PID,
// authorizes the bounded wait. This does not identify a prior failure's holder.
func TestFinishedWrapperCleanupJoinsOwnedProcess(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	d := Dir(filepath.Join(root, "turn"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"KEEPGOING_TEST_WRAPPER=linger", "KEEPGOING_TEST_HELPER=environment", "KEEPGOING_TEST_RESULT=" + filepath.Join(root, "capture")}
	var h syscall.Handle
	var captureErr error
	defer func() {
		if h != 0 {
			syscall.WaitForSingleObject(h, 30000)
			syscall.CloseHandle(h)
		}
	}()
	remove := ObserveSpawnsForTest(func(p *os.Process, workdir string) {
		if workdir == project {
			h, captureErr = syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(p.Pid))
		}
	})
	defer remove()
	if err := d.Start([]string{self}, project, "fictional prompt", env); err != nil {
		t.Fatal(err)
	}
	remove()
	if captureErr != nil || h == 0 {
		t.Fatalf("owned capture: %v", captureErr)
	}
	rec, err := d.record()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Finished)
	wait, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || wait != syscall.WAIT_TIMEOUT {
		t.Fatalf("owned linger not alive: wait=%d err=%v", wait, err)
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	err = os.Remove(project)
	var errno syscall.Errno
	errors.As(err, &errno)
	t.Logf("Finished wrapper pid=%d wait=%d exit=%d remove=%v errno=%d", rec.PID, wait, code, err, errno)
	if errno != syscall.Errno(32) {
		t.Fatalf("cwd removal did not establish sharing failure: %v", err)
	}
	wait, err = syscall.WaitForSingleObject(h, 5000)
	if err != nil || wait != syscall.WAIT_OBJECT_0 {
		t.Fatalf("owned join: wait=%d err=%v", wait, err)
	}
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	t.Logf("joined same owned wrapper pid=%d wait=%d exit=%d", rec.PID, wait, code)
	if err := os.Remove(project); err != nil {
		t.Fatalf("removal after concrete owned join: %v", err)
	}
}

// A failed save restores the prior record, so its PID cannot identify the
// spawned wrapper. Capture must precede that failure and its delayed decline.
func TestSpawnObserverRetainsWrapperAfterFailedRecordSave(t *testing.T) {
	root := t.TempDir()
	d := Dir(filepath.Join(root, "turn"))
	cli := helperCLI(t, "{}", 0, 0)
	declined := filepath.Join(root, "declined")
	var h syscall.Handle
	var captureErr, errorSetup error
	defer func() {
		if h != 0 {
			syscall.WaitForSingleObject(h, 30000)
			syscall.CloseHandle(h)
		}
	}()
	remove := ObserveSpawnsForTest(func(p *os.Process, workdir string) {
		if workdir != root {
			return
		}
		h, captureErr = syscall.OpenProcess(0x1000|syscall.SYNCHRONIZE, false, uint32(p.Pid))
		errorSetup = os.Mkdir(d.path("run.json.tmp"), 0700)
	})
	defer remove()
	err := d.Start([]string{cli}, root, "fictional prompt", []string{"KEEPGOING_TEST_WRAPPER=late", "KEEPGOING_TEST_DECLINED=" + declined})
	remove()
	if captureErr != nil || errorSetup != nil || h == 0 {
		t.Fatalf("capture=%v injection=%v handle=%d", captureErr, errorSetup, h)
	}
	if err == nil {
		t.Fatal("record save unexpectedly succeeded")
	}
	if _, err := d.record(); !os.IsNotExist(err) {
		t.Fatalf("failed save left a new record: %v", err)
	}
	wait, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || wait != syscall.WAIT_TIMEOUT {
		t.Fatalf("delayed wrapper not retained: %d %v", wait, err)
	}
	wait, err = syscall.WaitForSingleObject(h, 30000)
	if err != nil || wait != syscall.WAIT_OBJECT_0 {
		t.Fatalf("declined wrapper did not exit: %d %v", wait, err)
	}
	if _, err := os.Stat(declined); err != nil {
		t.Fatalf("wrapper did not decline: %v", err)
	}
	if ranAgent(cli) {
		t.Fatal("declined wrapper ran the agent")
	}
}
