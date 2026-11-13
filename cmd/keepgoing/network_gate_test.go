//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func networkConfig(t *testing.T, f *fixture, network string) {
	t.Helper()
	write(t, f.cfg, fmt.Sprintf(`version: 1
loop:
  name: network-example
  state_dir: state
  prompt: Continue the fictional loop.
  adapter: bb
  bb: {thread: thr_primary, cli: %q}
  errors: {max_reprompts: 2, backoff: 1s}
%s`, filepath.Join(f.dir, "bin", "bb"), network), 0600)
}

func networkTick(t *testing.T, f *fixture) (int, map[string]any) {
	t.Helper()
	code, out, _ := f.run("tick")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("tick output: %v\n%s", err, out)
	}
	return code, v
}

// Through the real CLI with a stand-in connectivity command: while it fails,
// an errored thread is not re-prompted, the check says why, exits 0 and
// spends nothing; when it passes, the next check re-prompts at once.
func TestNetworkDownGatesRepromptsEndToEnd(t *testing.T) {
	f := newFixture(t)
	networkConfig(t, f, "")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
	f.run("enable")
	t.Setenv("KEEPGOING_NET_PROBE", "exit 1")
	for i := 0; i < 4; i++ {
		code, r := networkTick(t, f)
		if code != exitOK || r["action"] != "none" || r["reason"] != "network down; waiting for connectivity before re-prompting or handing off" || r["network"] != "down" || r["outcome"] != "succeeded" || r["reprompts"] != nil {
			t.Fatalf("check %d: exit %d %v", i, code, r)
		}
	}
	if f.read("thr_primary.told") != "" {
		t.Fatal("a re-prompt was sent into the dead network")
	}
	_, status, _ := f.run("status")
	if !strings.Contains(status, "network:   down since ") || !strings.Contains(status, "decision reason: network down;") {
		t.Fatalf("status does not show the outage:\n%s", status)
	}
	code, timeline, _ := timelineCLI(t, filepath.Join(f.dir, "state"))
	if code != 0 || !strings.Contains(timeline, "cause network_down") {
		t.Fatalf("timeline does not name the wait:\n%s", timeline)
	}

	t.Setenv("KEEPGOING_NET_PROBE", "exit 0")
	if code, r := networkTick(t, f); code != exitOK || r["action"] != "reprompt" || r["network"] != nil || r["reprompts"] != float64(1) {
		t.Fatalf("first check after connectivity returned: exit %d %v", code, r)
	}
	if _, status, _ = f.run("status"); strings.Contains(status, "network:   down") {
		t.Fatalf("outage still shown:\n%s", status)
	}
}

// The check can be pointed at a command or turned off. With it off, recovery
// is attempted whatever the network is doing, as before this change.
func TestNetworkCheckConfiguration(t *testing.T) {
	t.Setenv("KEEPGOING_NET_PROBE", "exit 1") // the default check would say down
	for _, tc := range []struct {
		name, network, action string
	}{
		{"off", "  network: {check: \"off\"}\n", "reprompt"},
		{"command up", "  network: {command: [\"true\"]}\n", "reprompt"},
		{"command down", "  network: {command: [\"false\"]}\n", "none"},
		{"default uses the environment probe", "", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			networkConfig(t, f, tc.network)
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
			f.run("enable")
			if code, r := networkTick(t, f); code != exitOK || r["action"] != tc.action {
				t.Fatalf("exit %d %v", code, r)
			}
		})
	}
	for _, bad := range []string{
		"  network: {check: maybe}\n",
		"  network: {url: \"ftp://example.com\"}\n",
		"  network: {url: \"https://example.com\", command: [\"true\"]}\n",
		"  network: {timeout: 31s}\n",
		"  network: {timeout: 500ms}\n",
	} {
		f := newFixture(t)
		networkConfig(t, f, bad)
		if code, _, errs := f.run("validate"); code == exitOK || !strings.Contains(errs, "loop.network") {
			t.Fatalf("%q accepted: %s", bad, errs)
		}
	}
}
