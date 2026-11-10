//go:build unix

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func subscribeCompletion(t *testing.T, f *fixture) {
	t.Helper()
	b, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, strings.Replace(string(b), "stop, await_user]", "stop, await_user, complete]", 1), 0600)
}

func TestCompletionReportsOncePerDeclaration(t *testing.T) {
	f := newFixture(t)
	subscribeCompletion(t, f)
	for _, args := range [][]string{{"complete", "--note", "fictional private evidence"}, {"complete", "--note", "updated private evidence"}, {"enable"}, {"pause"}, {"complete"}} {
		if code, _, errs := f.run(args...); code != 0 {
			t.Fatal(errs)
		}
	}
	f.tick()
	if f.hooks.types() != "keepgoing.complete" {
		t.Fatal(f.hooks.types())
	}
	f.hooks.mu.Lock()
	payload, err := json.Marshal(f.hooks.events[0])
	f.hooks.mu.Unlock()
	if err != nil || strings.Contains(string(payload), "private evidence") || !strings.Contains(string(payload), "explicit_declaration") || !strings.Contains(string(payload), "not_recorded") || !strings.Contains(string(payload), "has not independently verified") {
		t.Fatalf("%s %v", payload, err)
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("completion contacted providers")
	}
	// A fresh CLI call uses persisted control, not an in-memory delivery flag.
	if code, _, errs := f.run("resume"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs := f.run("complete"); code != 0 {
		t.Fatal(errs)
	}
	if f.hooks.types() != "keepgoing.complete,keepgoing.complete" {
		t.Fatal(f.hooks.types())
	}
}

func TestCompletionReportFailureIsNotReplayed(t *testing.T) {
	f := newFixture(t)
	subscribeCompletion(t, f)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ctl, err := control.Load(cfg.Loop.StateDir)
		if err != nil || !ctl.Completed {
			t.Error("delivery preceded persisted hold")
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	code, _, errs := f.run("complete")
	if code != 0 || !strings.Contains(errs, "delivery unconfirmed") || calls.Load() != 1 {
		t.Fatalf("code=%d calls=%d err=%s", code, calls.Load(), errs)
	}
	if code, _, errs = f.run("complete"); code != 0 {
		t.Fatal(errs)
	}
	f.tick()
	if calls.Load() != 1 {
		t.Fatal("failed delivery replayed")
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil || !ctl.Completed {
		t.Fatal("failed delivery cleared hold")
	}
}

func TestCompletionReportConcurrentCommandAndStop(t *testing.T) {
	f := newFixture(t)
	subscribeCompletion(t, f)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	defer close(release)
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	done := make(chan int, 1)
	go func() { code, _, _ := f.run("complete"); done <- code }()
	select {
	case <-entered:
	case code := <-done:
		t.Fatalf("command returned without attempting report: %d", code)
	}
	// Delivery does not hold control.lock: a stop can win while the webhook waits.
	if code, _, errs := f.run("stop"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs := f.run("complete"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil || !ctl.Stop || !ctl.Completed {
		t.Fatal("stop/completion lost")
	}
	// The server is released by defer; the command must finish without a second request.
	if calls.Load() != 1 {
		t.Fatal("concurrent duplicate")
	}
	// Wait for completion before returning from the test and closing its temp directory.
	release <- struct{}{}
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
}

func TestCompletionReportSkippedOrLegacyIsNotBackfilled(t *testing.T) {
	for _, kind := range []string{"unsubscribed", "legacy", "control-failure", "log-failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			if kind != "unsubscribed" {
				subscribeCompletion(t, f)
			}
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "legacy":
				_, err = control.Update(cfg.Loop.StateDir, func(c *control.File) { c.Completed = true })
			case "control-failure":
				os.MkdirAll(cfg.Loop.StateDir, 0700)
				err = os.Mkdir(filepath.Join(cfg.Loop.StateDir, control.FileName), 0700)
			case "log-failure":
				os.MkdirAll(cfg.Loop.StateDir, 0700)
				err = os.Mkdir(filepath.Join(cfg.Loop.StateDir, "runs.jsonl"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			code, _, errs := f.run("complete")
			if (code != 0) != (kind == "control-failure" || kind == "log-failure") {
				t.Fatalf("code=%d %s", code, errs)
			}
			if kind == "log-failure" {
				os.Remove(filepath.Join(cfg.Loop.StateDir, "runs.jsonl"))
				if code, _, errs = f.run("complete"); code != 0 {
					t.Fatal(errs)
				}
			}
			if kind == "unsubscribed" {
				subscribeCompletion(t, f)
				if code, _, errs := f.run("complete"); code != 0 {
					t.Fatal(errs)
				}
			}
			if f.hooks.types() != "" {
				t.Fatal("unexpected report")
			}
		})
	}
}

func TestCompletionTextPayloadOmitsPrivateNote(t *testing.T) {
	f := newFixture(t)
	subscribeCompletion(t, f)
	b, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, strings.Replace(string(b), "report: {events:", "report: {format: text, scheme: bearer, events:", 1), 0600)
	if code, _, errs := f.run("complete", "--note", "fictional private acceptance evidence"); code != 0 {
		t.Fatal(errs)
	}
	f.hooks.mu.Lock()
	defer f.hooks.mu.Unlock()
	if len(f.hooks.events) != 1 {
		t.Fatal("missing report")
	}
	payload, err := json.Marshal(f.hooks.events[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "has not independently verified success") || strings.Contains(string(payload), "private acceptance evidence") {
		t.Fatalf("%s", payload)
	}
}
