//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestTransferProcessHelper(t *testing.T) {
	if os.Getenv("KEEPGOING_TRANSFER_HELPER") != "1" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(os.Getenv("KEEPGOING_TRANSFER_ARGS")), &args) != nil {
		os.Exit(90)
	}
	if len(args) > 1 && args[0] == "transfer" && args[1] == "apply" {
		code := transferApply(env{ctx: context.Background(), stdout: os.Stdout, stderr: os.Stderr}, args[1:], doctorExec, func(step string) error {
			if step == os.Getenv("KEEPGOING_TRANSFER_CRASH") {
				os.Exit(93)
			}
			return nil
		})
		os.Exit(code)
	}
	os.Exit(run(context.Background(), args, os.Stdout, os.Stderr))
}

func transferProcess(t *testing.T, owner, crash string, args ...string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTransferProcessHelper$")
	cmd.Env = append(os.Environ(), "KEEPGOING_TRANSFER_HELPER=1", "KEEPGOING_TRANSFER_ARGS="+string(b), "KEEPGOING_TRANSFER_CRASH="+crash, "BB_THREAD_ID="+owner)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode(), string(out)
	}
	t.Fatal(err)
	return -1, ""
}

func TestTransferProcessCrashStopRecoveryAndNewTargetOnly(t *testing.T) {
	path, dir := transferApplyFixture(t)
	ctl, _ := control.Load(dir)
	ctl.Paused, ctl.Stop, ctl.Completed, ctl.AwaitingUser = false, false, false, false
	control.Save(dir, ctl)
	fixtureDir := filepath.Join(t.TempDir(), "bb")
	os.Mkdir(fixtureDir, 0700)
	t.Setenv("FAKE_BB", fixtureDir)
	cli := filepath.Join(t.TempDir(), "bb")
	write(t, cli, fakeBB, 0700)
	raw, _ := os.ReadFile(path)
	quoted, _ := json.Marshal(cli)
	raw = []byte(strings.Replace(string(raw), "  bb:\n", "  bb:\n    cli: "+string(quoted)+"\n", 1))
	os.WriteFile(path, raw, 0600)
	for _, id := range []string{"thr_source", "thr_destination"} {
		v := transferResponse(id)
		if id == "thr_destination" {
			v["thread"].(map[string]any)["status"] = "active"
		}
		b, _ := json.Marshal(v)
		os.WriteFile(filepath.Join(fixtureDir, id+".show"), b, 0600)
	}
	code, out := transferProcess(t, "thr_source", "config", "transfer", "apply", "-c", path, "--from", "thr_source", "--thread", "thr_destination")
	if code != 93 {
		t.Fatal(code, out)
	}
	ctl, _ = control.Load(dir)
	if ctl.PendingTransfer == nil || !ctl.AwaitingUser {
		t.Fatal("crash lost recovery fence")
	}
	id := ctl.PendingTransfer.ID
	if code, out := transferProcess(t, "thr_source", "", "resume", "-c", path); code == 0 {
		t.Fatal("crashed transfer resumed", out)
	}
	before, _ := os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if code, out := transferProcess(t, "", "", "tick", "-c", path); code == 0 {
		t.Fatal("pending transfer tick was not refused", out)
	}
	after, _ := os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if string(before) != string(after) {
		t.Fatal("pending tick queried provider")
	}
	if code, out := transferProcess(t, "", "", "stop", "-c", path); code != 0 {
		t.Fatal(code, out)
	}
	if code, out := transferProcess(t, "", "", "tick", "-c", path); code != 0 {
		t.Fatal(code, out)
	}
	calls, _ := os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if !strings.Contains(string(calls), "thread stop thr_destination") || strings.Contains(string(calls), "thread tell") {
		t.Fatal("pending stop targeted incorrectly", string(calls))
	}
	if code, out := transferProcess(t, "thr_source", "", "transfer", "recover", "-c", path, "--id", id); code != 0 {
		t.Fatal(code, out)
	}
	ctl, _ = control.Load(dir)
	if ctl.PendingTransfer != nil || ctl.BBTarget != "thr_destination" || !ctl.Stop || !ctl.AwaitingUser {
		t.Fatal("recovery lost stop/hold")
	}
	if code, out := transferProcess(t, "thr_destination", "", "resume", "-c", path); code != 0 {
		t.Fatal(code, out)
	}
	b, _ := json.Marshal(transferResponse("thr_destination"))
	os.WriteFile(filepath.Join(fixtureDir, "thr_destination.show"), b, 0600)
	if code, out := transferProcess(t, "", "", "tick", "-c", path); code != 0 {
		t.Fatal(code, out)
	}
	calls, _ = os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if !strings.Contains(string(calls), "thread tell thr_destination") || strings.Contains(string(calls), "thread tell thr_source") {
		t.Fatal("wrong wake target", string(calls))
	}
	copyPath := filepath.Join(filepath.Dir(path), "old.yaml")
	os.WriteFile(copyPath, raw, 0600)
	if code, out := transferProcess(t, "", "", "tick", "-c", copyPath); code == 0 {
		t.Fatal("old config copy tick succeeded", out)
	}
	final, _ := os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if string(calls) != string(final) {
		t.Fatal("old config copy touched provider")
	}
}
