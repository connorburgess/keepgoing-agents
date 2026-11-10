//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

func TestEndToEndChildInputHoldsAndReportsWithoutParentMutation(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"active"}}`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.interactions"), `[{"threadId":"thr_child","status":"pending","payload":{"text":"fictional-private-question"}}]`, 0600)
	r := f.tick()
	if r["action"] != "await_user" || !strings.Contains(r["reason"].(string), "thr_child") {
		t.Fatalf("%v", r)
	}
	if f.hooks.types() != "keepgoing.await_user" {
		t.Fatalf("%s", f.hooks.types())
	}
	f.hooks.mu.Lock()
	body, _ := json.Marshal(f.hooks.events)
	f.hooks.mu.Unlock()
	if !strings.Contains(string(body), `"children":["thr_child"]`) || strings.Contains(string(body), "fictional-private-question") {
		t.Fatalf("report did not identify only child metadata: %s", body)
	}
	if f.read("probes") != "" {
		t.Fatal("child escalation spent probe quota")
	}
	calls := f.read("calls")
	for _, s := range []string{"thread tell ", "thread stop ", "thread update ", "interactions respond"} {
		if strings.Contains(calls, s) {
			t.Fatal("provider mutated")
		}
	}
	// Pending input disappearing does not reopen the task without explicit resume.
	write(t, filepath.Join(f.bbDir, "thr_child.interactions"), `[]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	if r = f.tick(); r["reason"] != "awaiting user" || f.read("calls") != calls || f.hooks.types() != "keepgoing.await_user" {
		t.Fatalf("hold not persistent: %v", r)
	}
	for _, name := range []string{"control.json", "state.json", "runs.jsonl"} {
		b, err := os.ReadFile(filepath.Join(f.dir, "state", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "fictional-private-question") {
			t.Fatalf("private input in %s", name)
		}
	}
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	if r = f.tick(); r["action"] != "wake" {
		t.Fatalf("explicit resume: %v", r)
	}
}

func TestChildInputTakesPrecedenceOverEarlyCompletionWake(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_child"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.show"), `{"thread":{"id":"thr_child","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_child.interactions"), `[{"threadId":"thr_child","status":"pending"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_primary.log"), `[{"seq":1,"type":"client/turn/requested","data":{"initiator":"system","systemMessageKind":"child-completed","systemMessageSubject":{"kind":"thread","threadId":"thr_child","outcomes":[{"threadId":"thr_child","status":"completed"}]}}}]`, 0600)
	cfg := loadFixture(t, f.cfg)
	// Test precedence through the real fake-CLI adapter without requiring its
	// subprocess chain to fit within a production control-poll budget. Command
	// tests separately cover timer advancement and bounded/cancelled polling.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	early := supervise.PollWake(ctx, cfg, nil)
	l.Release()
	if !early {
		t.Fatal("completion did not request an escalation check")
	}
	if f.read("thr_primary.told") != "" || f.read("thr_child.told") != "" || f.hooks.types() != "" {
		t.Fatal("notification poll performed an action before the tick")
	}
	if r := f.tick(); r["action"] != "await_user" {
		t.Fatalf("%v", r)
	}
	if f.read("thr_primary.told") != "" || f.read("thr_child.told") != "" {
		t.Fatal("pending input caused a prompt")
	}
}
