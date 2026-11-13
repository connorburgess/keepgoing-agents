//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
		if code != exitOK || r["action"] != "none" || r["reason"] != "network down; waiting for connectivity" || r["network"] != "down" || r["outcome"] != "succeeded" || r["reprompts"] != nil {
			t.Fatalf("check %d: exit %d %v", i, code, r)
		}
	}
	if f.read("thr_primary.told") != "" {
		t.Fatal("a re-prompt was sent into the dead network")
	}
	_, status, _ := f.run("status")
	if !strings.Contains(status, "network:   down at the last connectivity check, failing since ") || !strings.Contains(status, "not reported yet") || !strings.Contains(status, "decision reason: network down;") {
		t.Fatalf("status does not show the outage:\n%s", status)
	}
	code, timeline, _ := timelineCLI(t, filepath.Join(f.dir, "state"))
	if code != 0 || !strings.Contains(timeline, "cause network_down") {
		t.Fatalf("timeline does not name the wait:\n%s", timeline)
	}

	t.Setenv("KEEPGOING_NET_PROBE", "exit 0")
	if code, r := networkTick(t, f); code != exitOK || r["action"] != "reprompt" || r["network"] != "restored" || r["networkDownFor"] == nil || r["reprompts"] != float64(1) {
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

// doctor runs the connectivity check once, so a target this machine cannot
// reach is found before it makes a loop wait. --offline does not run it.
func TestDoctorTestsTheConnectivityCheck(t *testing.T) {
	check := func(t *testing.T, f *fixture, args ...string) string {
		t.Helper()
		_, out, _ := f.run(append([]string{"doctor", "--json"}, args...)...)
		i := strings.Index(out, `"network.check"`)
		if i < 0 {
			t.Fatalf("doctor has no network.check:\n%s", out)
		}
		return out[i:min(len(out), i+900)]
	}
	marker := filepath.Join(t.TempDir(), "probed")
	probe := "touch " + marker + "; exit "
	for _, tc := range []struct {
		name, network, env, want string
		args                     []string
		probed                   bool
	}{
		{"reachable", "", probe + "0", `"status": "ok"`, nil, true},
		{"unreachable", "", probe + "1", "could not reach the default target", nil, true},
		{"configured command", "  network: {command: [\"false\"]}\n", probe + "0", "could not reach the configured command", nil, false},
		{"off", "  network: {check: \"off\"}\n", probe + "1", "The connectivity check is off", nil, false},
		{"no room", "  tick_timeout: 2m40s\n", probe + "1", "leaves no room for the connectivity check", nil, false},
		{"offline flag", "", probe + "1", "not tested because --offline was given", []string{"--offline"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(marker)
			t.Setenv("KEEPGOING_NET_PROBE", tc.env)
			f := newFixture(t)
			networkConfig(t, f, tc.network)
			if got := check(t, f, tc.args...); !strings.Contains(got, tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, got)
			}
			if _, err := os.Stat(marker); (err == nil) != tc.probed {
				t.Fatalf("default probe ran=%t, want %t", err == nil, tc.probed)
			}
		})
	}
}

// doctor names what a target that redirects, or asks for a network sign-in,
// returned, since that reads as offline and a redirect is never followed.
func TestDoctorNamesARedirect(t *testing.T) {
	for want, status := range map[string]int{"got a redirect from the configured URL": http.StatusFound, "got 511 Network Authentication Required from the configured URL": http.StatusNetworkAuthenticationRequired} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://portal.example.com/sign-in")
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		f := newFixture(t)
		networkConfig(t, f, "  network: {url: \""+srv.URL+"\"}\n")
		_, out, _ := f.run("doctor", "--json")
		if !strings.Contains(out, want) || !strings.Contains(out, "redirects are never followed") {
			t.Fatalf("want %q in:\n%s", want, out)
		}
	}
}

// A dry run previews the decision without making the connectivity request,
// and an explicit tick_timeout of 2m40s, the earlier minimum, still loads:
// the check has no room, is not made, and validate and status say so.
func TestDryRunAndSmallTickTimeoutMakeNoConnectivityRequest(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	t.Setenv("KEEPGOING_NET_PROBE", "touch "+marker+"; exit 1")
	f := newFixture(t)
	networkConfig(t, f, "")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"error"}}`, 0600)
	f.run("enable")
	code, out, _ := f.run("tick", "--dry-run")
	if _, err := os.Stat(marker); err == nil || code != exitOK || !strings.Contains(out, `"action":"reprompt"`) {
		t.Fatalf("dry run made the request or changed its preview: exit %d %s", code, out)
	}

	networkConfig(t, f, "  tick_timeout: 2m40s\n")
	code, _, errs := f.run("validate")
	if code != exitOK || !strings.Contains(errs, "leaves no room for the connectivity check") {
		t.Fatalf("validate: exit %d %s", code, errs)
	}
	if code, r := networkTick(t, f); code != exitOK || r["action"] != "reprompt" || r["network"] != nil {
		t.Fatalf("exit %d %v", code, r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the check was made although it has no room")
	}
	if _, status, _ := f.run("status"); !strings.Contains(status, "network:   warning: loop.tick_timeout (2m40s) leaves no room") {
		t.Fatalf("status:\n%s", status)
	}
}
