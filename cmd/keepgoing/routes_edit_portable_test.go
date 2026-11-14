package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// These run on every platform, Windows included, where a reader must never
// block the rename that replaces the list.

const routeEditPortableList = "version: 1\r\nroutes:\r\n  # First.\r\n  - {id: first, provider: codex, model: example-codex, group: codex-a, enter: headless}\r\n  - {id: second, provider: codex, model: example-codex, group: codex-b, enter: headless}\r\n"

func routeEditPortable(t *testing.T) (string, func(args ...string) (int, routeChangeOut)) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	dir := t.TempDir()
	list := filepath.Join(dir, "user", config.RoutesFileName)
	if err := os.MkdirAll(filepath.Dir(list), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list, []byte(routeEditPortableList), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.RoutesFileEnv, list)
	registry := filepath.Join(dir, "registry")
	return list, func(args ...string) (int, routeChangeOut) {
		t.Helper()
		var out, errb bytes.Buffer
		code := run(context.Background(), append(append([]string{"routes"}, args...), "--directory", registry, "--json"), &out, &errb)
		var v routeChangeOut
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatalf("JSON: %v %s %s", err, &out, &errb)
		}
		return code, v
	}
}

func TestRouteEditChangesTheListLineByLineOnEveryPlatform(t *testing.T) {
	list, change := routeEditPortable(t)
	if code, v := change("move", "second", "--to", "1", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("move: %d %+v", code, v)
	}
	if code, v := change("off", "first", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("off: %d %+v", code, v)
	}
	if code, v := change("add", "--id", "third", "--provider", "codex", "--model", "example-codex", "--group", "codex-c", "--enter", "headless", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("add: %d %+v", code, v)
	}
	got, _ := os.ReadFile(list)
	want := "version: 1\r\nroutes:\r\n  - {id: second, provider: codex, model: example-codex, group: codex-b, enter: headless}\r\n  # First.\r\n  - {id: first, provider: codex, model: example-codex, group: codex-a, enter: headless, disabled: true}\r\n  - {id: third, provider: codex, model: example-codex, group: codex-c, enter: headless}\r\n"
	if string(got) != want {
		t.Fatalf("list:\n%q", got)
	}
	if code, v := change("remove", "third", "--yes"); code != 0 || v.Outcome != "changed" {
		t.Fatalf("remove: %d %+v", code, v)
	}
	if code, v := change("add", "--id", "third", "--provider", "codex", "--model", "example-codex", "--group", "codex-c", "--enter", "headless", "--yes"); code == 0 || v.Refusal == nil || v.Refusal.Code != "id_used_before" {
		t.Fatalf("reuse: %d %+v", code, v)
	}
	hist, err := os.ReadFile(filepath.Join(filepath.Dir(list), routesHistoryName))
	if err != nil || strings.Count(string(hist), "\n") != 4 {
		t.Fatalf("history: %v %s", err, hist)
	}
}

func TestRouteEditReplacesTheListWhileItIsRead(t *testing.T) {
	list, change := routeEditPortable(t)
	var stop atomic.Bool
	var failures atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := config.LoadRoutes(list); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	for i := range 20 {
		to := 1 + i%2
		if code, v := change("move", "first", "--to", map[int]string{1: "2", 2: "1"}[to], "--yes"); code != 0 || (v.Outcome != "changed" && v.Outcome != "unchanged") {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("move %d while read: %d %+v", i, code, v)
		}
	}
	stop.Store(true)
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("%d reads failed while the list was replaced", n)
	}
}

// timelineOps lists the route_change events in a loop's timeline, as
// operation/caller.
func timelineOps(t *testing.T, cfg string) []string {
	t.Helper()
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"timeline", "--json", "-c", cfg}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var tl timelineReport
	if err := json.Unmarshal(out.Bytes(), &tl); err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, ev := range tl.Events {
		if ev.Incident != nil && ev.Incident.RouteChange != nil {
			ops = append(ops, ev.Incident.RouteChange.Operation+"/"+ev.Incident.RouteChange.Caller)
		}
	}
	return ops
}
