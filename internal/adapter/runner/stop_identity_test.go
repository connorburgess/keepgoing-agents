package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The live PID belongs to a disposable fixture, never an arbitrary host process.
func TestStopRefusesLegacyLivePID(t *testing.T) {
	root := t.TempDir()
	owned := Dir(filepath.Join(root, "owned"))
	if err := owned.Start([]string{helperCLI(t, "{}", 0, 30*time.Second)}, root, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	defer owned.Stop()
	st, err := owned.Status()
	if err != nil {
		t.Fatal(err)
	}
	stale := Dir(filepath.Join(root, "stale"))
	if err := os.MkdirAll(string(stale), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": st.Record.PID, "startedAt": time.Now().UTC(), "argv0": "fixture"})
	if err := os.WriteFile(stale.path("run.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := stale.Stop(); err == nil {
		t.Fatal("legacy saved PID authorized a stop")
	}
	if st, err := owned.Status(); err != nil || st.Phase != Running {
		t.Fatalf("unrelated fixture changed: %s, %v", st.Phase, err)
	}
}

func startStopFixture(t *testing.T) Dir {
	t.Helper()
	root := t.TempDir()
	d := Dir(filepath.Join(root, "turn"))
	if err := d.Start([]string{helperCLI(t, "{}", 0, 30*time.Second)}, root, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	return d
}

func saveStopRecord(t *testing.T, d Dir, rec Record) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.path("run.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStopUsesOwnedChannelDespiteChangedSavedPID(t *testing.T) {
	owned, unrelated := startStopFixture(t), startStopFixture(t)
	original, _ := owned.Status()
	victim, _ := unrelated.Status()
	changed := original.Record
	changed.PID = victim.Record.PID
	saveStopRecord(t, owned, changed)
	if err := owned.Stop(); err != nil {
		t.Fatal(err)
	}
	// Restore the advisory PID only so the existing status API can observe exit.
	saveStopRecord(t, owned, original.Record)
	waitFor(t, owned, Crashed)
	if st, err := unrelated.Status(); err != nil || st.Phase != Running {
		t.Fatalf("unrelated process affected: %s %v", st.Phase, err)
	}
}

func TestStopRejectsMismatchedKeyAndStaleAcknowledgement(t *testing.T) {
	d := startStopFixture(t)
	original, _ := d.Status()
	defer saveStopRecord(t, d, original.Record)
	wrong := original.Record
	wrong.StopKey, _ = randomStopID()
	saveStopRecord(t, d, wrong)
	oldRequest, _ := randomStopID()
	if err := writeStopMessage(d.path("stop-ack.json"), stopMessage{Key: wrong.StopKey, Request: oldRequest}); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(); err == nil {
		t.Fatal("mismatched wrapper/stale acknowledgement accepted")
	}
	if st, err := d.Status(); err != nil || st.Phase != Running {
		t.Fatalf("wrong key affected live fixture: %s %v", st.Phase, err)
	}
}

func TestStopAfterWrapperCrashNeverTargetsReusedPID(t *testing.T) {
	// Retain the process returned by Start, and do not reap it until Kill. The
	// crash injection never obtains a process handle from a persisted PID.
	root := t.TempDir()
	key, _ := randomStopID()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(root, "crash-child")
	if err := os.WriteFile(filepath.Join(root, "prompt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd, err := wrapperCommand([]string{self}, root, root, []string{envTurnKey + "=" + key, "KEEPGOING_TEST_HELPER=leaf", "KEEPGOING_TEST_RESULT=" + result}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(result + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("wrapper did not launch disposable child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The disposable child self-exits after two seconds if wrapper death leaves
	// it behind. No arbitrary PID or persistent provider is involved.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	unrelated := startStopFixture(t)
	victim, _ := unrelated.Status()
	stale := Dir(root)
	saveStopRecord(t, stale, Record{PID: victim.Record.PID, StopKey: key})
	if err := stale.Stop(); err == nil {
		t.Fatal("dead wrapper unexpectedly confirmed stop")
	}
	if st, err := unrelated.Status(); err != nil || st.Phase != Running {
		t.Fatalf("reused PID target affected: %s %v", st.Phase, err)
	}
}

func TestStopOldRequestCannotStopNextTurn(t *testing.T) {
	d := startStopFixture(t)
	old, _ := d.Status()
	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Crashed)
	if err := d.Start([]string{helperCLI(t, "{}", 0, 30*time.Second)}, filepath.Dir(string(d)), "next", nil); err != nil {
		t.Fatal(err)
	}
	next, _ := d.Status()
	if old.Record.StopKey == next.Record.StopKey {
		t.Fatal("stop identity reused")
	}
	nonce, _ := randomStopID()
	if err := writeStopMessage(d.path("stop-request.json"), stopMessage{Key: old.Record.StopKey, Request: nonce}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if st, err := d.Status(); err != nil || st.Phase != Running {
		t.Fatalf("old request stopped next turn: %s %v", st.Phase, err)
	}
	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Crashed)
}

func TestStopWatcherRejectsMalformedAndWrongRequests(t *testing.T) {
	d := Dir(t.TempDir())
	key, _ := randomStopID()
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	defer close(done)
	go watchStop(d, key, func() error { called <- struct{}{}; return nil }, done)
	for _, body := range []string{"{}", "not json", `{"key":"../escape","request":"bad"}`} {
		if err := os.WriteFile(d.path("stop-request.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		select {
		case <-called:
			t.Fatal("malformed request invoked termination")
		case <-time.After(50 * time.Millisecond):
		}
	}
	nonce, _ := randomStopID()
	m := stopMessage{Key: key, Request: nonce}
	if err := writeStopMessage(d.path("stop-request.json"), m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("owned request not delivered")
	}
	if ack, err := readStopMessage(d.path("stop-ack.json")); err != nil || ack != m {
		t.Fatal("missing matching receipt")
	}
}

func TestOwnedWrapperUnicodePathsAndPrivateEnvironment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "café 项目 with spaces")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	d := Dir(filepath.Join(root, "turn"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(root, "result")
	if err := d.Start([]string{self}, root, "fixture", []string{"KEEPGOING_TEST_HELPER=environment", "KEEPGOING_TEST_RESULT=" + result}); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	waitFor(t, d, Finished)
	value, err := os.ReadFile(result)
	if err != nil || string(value) != "|||fixture" {
		t.Fatal("wrapper variables leaked or Unicode path/prompt round trip failed")
	}
}

func TestOwnedStopTerminatesDescendants(t *testing.T) {
	root := t.TempDir()
	d := Dir(filepath.Join(root, "turn"))
	self, _ := os.Executable()
	result := filepath.Join(root, "escaped")
	if err := d.Start([]string{self}, root, "fixture", []string{"KEEPGOING_TEST_HELPER=tree", "KEEPGOING_TEST_RESULT=" + result}); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(result + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, d, Crashed)
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatal("descendant survived owned cancellation")
	}
}
