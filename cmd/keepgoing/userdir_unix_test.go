//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// Doctor through the command, for this platform's real places: a new
// install, then a loop's state_dir under the earlier name (which changes
// nothing), then the tool's own files under both names.
func TestDoctorUserDirectoryThroughTheCommand(t *testing.T) {
	f := newFixture(t)
	config, state := userHome(t)
	found := func() doctorCheck {
		t.Helper()
		_, out, _ := f.run("doctor", "--json")
		var v struct {
			Checks []doctorCheck `json:"checks"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, c := range v.Checks {
			if c.ID == "user.directory" {
				return c
			}
		}
		t.Fatalf("no user.directory check: %s", out)
		return doctorCheck{}
	}
	if c := found(); c.Status != "ok" || !strings.Contains(c.Message, "named looptender") {
		t.Fatalf("%+v", c)
	}
	if err := os.MkdirAll(filepath.Join(state, "looptender", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "keepgoing", "example-loop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := found(); c.Status != "ok" || !strings.Contains(c.Message, "named looptender") {
		t.Fatalf("a loop's state_dir under the earlier name: %+v", c)
	}
	// The tool's own claims under the earlier name: that name is in use.
	// Claims are honoured under both names, so this alone is no warning.
	if err := os.MkdirAll(filepath.Join(state, "keepgoing", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := found(); c.Status != "ok" || !strings.Contains(c.Message, "named keepgoing") {
		t.Fatalf("claims under both names: %+v", c)
	}
	// A registry under the current name is not read: a warning naming it.
	if err := os.MkdirAll(filepath.Join(config, "looptender", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c := found(); c.Status != "warning" || !strings.Contains(c.Message, filepath.Join(state, "keepgoing", "sessions")) || !strings.Contains(c.Message, "not read while that is so: "+filepath.Join(config, "looptender", "registry")) || strings.Contains(c.Message+c.Fix, "emove it") {
		t.Fatalf("%+v", c)
	}
}

// One name linked to the other, in either direction: checks run as usual.
// (Taking a claim once looked "under the other name", found its own, and
// refused every check for good.)
func TestChecksRunWhenTheTwoNamesAreOneDirectory(t *testing.T) {
	for _, link := range []string{"looptender", "keepgoing"} {
		t.Run(link+" is the link", func(t *testing.T) {
			f := newFixture(t)
			config, state := userHome(t)
			defaultPlaces(t)
			real := map[string]string{"looptender": "keepgoing", "keepgoing": "looptender"}[link]
			places := []string{config}
			if state != config {
				places = append(places, state)
			}
			for _, base := range places {
				if err := os.MkdirAll(filepath.Join(base, real), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, filepath.Join(base, link)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(state, real, "sessions"), 0o700); err != nil {
				t.Fatal(err)
			}
			if code, _, errs := f.run("enable"); code != exitOK {
				t.Fatalf("enable: %s", errs)
			}
			for i := 0; i < 2; i++ {
				code, out, errs := f.run("tick")
				if code != exitOK || !strings.Contains(out, `"outcome":"succeeded"`) {
					t.Fatalf("check %d: %d %s %s", i+1, code, out, errs)
				}
			}
			if !strings.Contains(f.read("calls"), "thr_primary") {
				t.Fatalf("nothing reached the session: %q", f.read("calls"))
			}
			if c := func() string { _, out, _ := f.run("doctor"); return out }(); !strings.Contains(c, "the two names are one directory") {
				t.Fatalf("doctor does not say the names are linked: %s", c)
			}
		})
	}
}

// shortUserHome is userHome somewhere short enough for the panel's socket.
func shortUserHome(t *testing.T) (config, state string) {
	t.Helper()
	root := panelShortDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "c"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "s"))
	var err error
	if config, err = userdir.ConfigHome(); err != nil {
		t.Fatal(err)
	}
	if state, err = userdir.StateHome(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{config, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	defaultPlaces(t)
	return config, state
}

func panelRunHere(args ...string) (int, string, string) {
	var out, errs bytes.Buffer
	code := panelCommand(env{context.Background(), &out, &errs}, args)
	return code, out.String(), errs.String()
}

// The name in use changes while a panel is running. keepgoing panel still
// opens that panel: it serves the default registry, under the name that was
// in use when it started, which is not "another registry". No second server
// is started. A registry a person named is still another registry.
func TestPanelOpensTheRunningPanelAfterTheNameChanges(t *testing.T) {
	config, state := shortUserHome(t)
	runsClock(t, dockNow)
	started := filepath.Join(state, "looptender", "panel")
	if err := os.MkdirAll(started, 0o700); err != nil {
		t.Fatal(err)
	}
	done := panelServeAt(t, started, "")
	// A build from before the rename registers a loop.
	if err := os.MkdirAll(filepath.Join(config, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if userdir.DirName() != "keepgoing" {
		t.Fatal("the earlier name is not in use")
	}
	code, out, errs := panelRunHere("--no-open")
	if code != exitOK || !strings.Contains(out, "Open this address") || strings.Contains(errs, "another registry") {
		t.Fatalf("open: %d %s %s", code, out, errs)
	}
	if held, _ := lock.Held(filepath.Join(state, "keepgoing", "panel"), panelServerLock); held {
		t.Fatal("a second server was started under the name now in use")
	}
	if code, out, _ := panelRunHere("status"); code != exitOK || !strings.Contains(out, "is running") {
		t.Fatalf("status: %d %s", code, out)
	}
	// A registry chosen with --directory is another registry, as before.
	chosen := filepath.Join(filepath.Dir(config), "chosen")
	if err := os.MkdirAll(chosen, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := panelRunHere("--no-open", "--directory", chosen); code != exitFailed || !strings.Contains(errs, "another registry") {
		t.Fatalf("a chosen registry: %d %s", code, errs)
	}
	if code, out, errs := panelRunHere("stop"); code != exitOK || !strings.Contains(out, "Stopped the keepgoing panel") {
		t.Fatalf("stop: %d %s %s", code, out, errs)
	}
	panelStoppedWithin(t, done, "the server")
}

// Two servers, one under each name (a build from before the rename started
// the second). One keepgoing panel stop stops both.
func TestPanelStopStopsServersUnderBothNames(t *testing.T) {
	config, state := shortUserHome(t)
	runsClock(t, dockNow)
	if err := os.MkdirAll(filepath.Join(config, "looptender", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	var servers []chan int
	for _, name := range []string{"looptender", "keepgoing"} {
		dir := filepath.Join(state, name, "panel")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, panelServeAt(t, dir, filepath.Join(config, "looptender", "registry")))
	}
	code, out, errs := panelRunHere("stop")
	if code != exitOK || !strings.Contains(out, "Stopped the keepgoing panel") || !strings.Contains(out, "Stopped a second panel, running at "+filepath.Join(state, "looptender", "panel")) {
		t.Fatalf("stop: %d %s %s", code, out, errs)
	}
	for i, done := range servers {
		panelStoppedWithin(t, done, []string{"the server under looptender", "the server under keepgoing"}[i])
	}
	for _, name := range []string{"looptender", "keepgoing"} {
		if held, _ := lock.Held(filepath.Join(state, name, "panel"), panelServerLock); held {
			t.Fatalf("a server is still running under %s", name)
		}
	}
	if code, out, _ := panelRunHere("stop"); code != exitOK || !strings.Contains(out, "is not running") {
		t.Fatalf("a second stop: %d %s", code, out)
	}
}

// defaultPlaces undoes the test harness's own registry and claim
// directories, so a test uses the user's default ones.
func defaultPlaces(t *testing.T) {
	t.Helper()
	for _, v := range envvar.Locations {
		t.Setenv(v.Name(), "")
		t.Setenv(v.Legacy(), "")
	}
}

const journeyRoutes = `version: 1
redundancy: {min_healthy: 1, span: group}
routes:
  - id: codex-headless
    provider: codex
    model: example-codex
    group: codex-main
    enter: headless
`

// A new install whose loop keeps its state_dir in a directory named
// keepgoing, as the guides used to suggest. Its checks create that
// directory. The name in use must not change: the registry still lists the
// loop, the standard route list is still read, and the claims stay where
// they were.
func TestALoopsStateDirUnderTheEarlierNameChangesNothing(t *testing.T) {
	f := newFixture(t)
	config, state := userHome(t)
	defaultPlaces(t)
	b, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(state, "keepgoing", "example-loop")
	write(t, f.cfg, strings.Replace(string(b), "state_dir: state", "state_dir: "+stateDir, 1), 0o600)
	if code, out, errs := runWith("registry", "add", "-c", f.cfg); code != exitOK {
		t.Fatalf("registry add: %d %s %s", code, out, errs)
	}
	if err := os.MkdirAll(filepath.Join(config, "looptender"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(config, "looptender", "routes.yaml"), journeyRoutes, 0o600)
	listed := func(when string) {
		t.Helper()
		if userdir.DirName() != "looptender" {
			t.Fatalf("%s: the name in use changed to %s", when, userdir.DirName())
		}
		if code, out, errs := runWith("registry", "list"); code != exitOK || !strings.Contains(out, "example-loop") {
			t.Fatalf("%s: registry list: %d %s %s", when, code, out, errs)
		}
		code, out, errs := runWith("routes", "list", "--json")
		var v struct {
			Standard struct {
				State string `json:"state"`
			} `json:"standard"`
		}
		if err := json.Unmarshal([]byte(out), &v); code != exitOK || err != nil || v.Standard.State != "ok" {
			t.Fatalf("%s: routes list: %d %s %s", when, code, out, errs)
		}
	}
	listed("before any check")
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %s", errs)
	}
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("tick: %v", v)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "state.json")); err != nil {
		t.Fatalf("the loop's state is not under the earlier name: %v", err)
	}
	listed("after a check")
	if _, err := os.Lstat(filepath.Join(state, "looptender", "sessions")); err != nil {
		t.Fatalf("the claims are not under the current name: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state, "keepgoing", "sessions")); err == nil {
		t.Fatal("claims were taken under the earlier name")
	}
	f.tick()
	listed("after a second check")
}

// The name in use changes in the middle of another loop's check: loop A
// holds its claim on a session under the current name, a build from before
// the rename then leaves its panel directory, and loop C, on the same
// session, starts a check and resolves the earlier name. C must be refused
// and send nothing, and enabling a second loop on the session must still
// find the first. Once A's check ends, C's goes ahead.
func TestAClaimUnderOneNameStopsACheckUnderTheOther(t *testing.T) {
	f := newFixture(t)
	_, state := userHome(t)
	defaultPlaces(t)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatalf("enable: %s", errs)
	}
	under := func(name string) string { return filepath.Join(state, name, "sessions") }
	id := session.Identity{Kind: "bb", ID: "thr_primary"}
	// A is in the middle of its check.
	if err := os.MkdirAll(under("looptender"), 0o700); err != nil {
		t.Fatal(err)
	}
	// (The holder is named as another live process, as status only names a
	// holder that is not itself: the test's parent stands in for it.)
	a, err := session.Take(under("looptender"), id, session.Holder{Loop: "example-loop-a", PID: os.Getppid(), Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	// A build from before the rename ran `panel status`.
	if err := os.MkdirAll(filepath.Join(state, "keepgoing", "panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	if userdir.DirName() != "keepgoing" {
		t.Fatal("the earlier name is not in use")
	}
	// C's check.
	code, out, errs := f.run("tick")
	if code != exitSkipped || !strings.Contains(errs, "example-loop-a") || f.read("calls") != "" {
		t.Fatalf("the check went ahead: %d %s %s calls=%q", code, out, errs, f.read("calls"))
	}
	if !strings.Contains(out, "another loop is acting on this session") {
		t.Fatalf("%s", out)
	}
	// status says who holds the session, wherever the claim is.
	if code, out, _ := f.run("status"); code != exitOK || !strings.Contains(out, "example-loop-a") {
		t.Fatalf("status: %d %s", code, out)
	}
	// The second-loop guard still finds the enabled loop, whose record is
	// under the current name.
	other := secondLoop(t, f, "example-loop-b", nil)
	if code, _, errs := runWith("enable", "-c", other); code != exitFailed || !strings.Contains(errs, "loop example-loop (") {
		t.Fatalf("a second loop was enabled: %d %s", code, errs)
	}
	// A's check ends: C's goes ahead, with its claim under the name in use.
	a.Release()
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("after the claim ended: %v", v)
	}
	if _, err := os.Lstat(filepath.Join(under("keepgoing"), id.Key()+".lock")); err != nil {
		t.Fatalf("the claim was not taken under the name in use: %v", err)
	}
}

// The other way round: a claim held under the earlier name stops a check
// that resolved the current one (a service started without the variable
// that names its user's configuration directory can decide so).
func TestAClaimUnderTheEarlierNameStopsACheckUnderTheCurrentOne(t *testing.T) {
	_, state := userHome(t)
	defaultPlaces(t)
	id := session.Identity{Kind: "bb", ID: "thr_example"}
	earlier, current := filepath.Join(state, "keepgoing", "sessions"), filepath.Join(state, "looptender", "sessions")
	for _, dir := range []string{earlier, current} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	held, err := session.Take(earlier, id, session.Holder{Loop: "example-loop-a", PID: os.Getpid(), Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var claimed *session.ClaimedError
	if _, err := session.Take(current, id, session.Holder{Loop: "example-loop-c", PID: os.Getpid(), Since: time.Now()}); !errors.As(err, &claimed) || claimed.Holder == nil || claimed.Holder.Loop != "example-loop-a" {
		t.Fatalf("%v", err)
	}
	held.Release()
	c, err := session.Take(current, id, session.Holder{Loop: "example-loop-c", PID: os.Getpid(), Since: time.Now()})
	if err != nil {
		t.Fatalf("after the claim ended: %v", err)
	}
	c.Release()
}

// A panel server keeps the directory it was started in. After the name in
// use changes, the panel commands still address that server, and none
// starts a second one under the other name.
func TestPanelCommandsFindAServerUnderTheOtherName(t *testing.T) {
	configBase, stateBase := panelShortDir(t), panelShortDir(t)
	prevState, prevLegacy := panelStateBase, panelLegacyBase
	panelStateBase = func() (string, error) { return stateBase, nil }
	panelLegacyBase = func() (string, error) { return configBase, nil }
	t.Cleanup(func() { panelStateBase, panelLegacyBase = prevState, prevLegacy })
	started, err := panelDirectory("")
	if err != nil || started != filepath.Join(stateBase, "looptender", "panel") {
		t.Fatalf("%q %v", started, err)
	}
	server, err := lock.AcquireNamed(started, panelServerLock)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Release()
	// A registry from a build before the rename appears.
	if err := os.MkdirAll(filepath.Join(configBase, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if panelDirName() != "keepgoing" {
		t.Fatal("the earlier name is not in use")
	}
	if dir, err := panelDirectory(""); err != nil || dir != started {
		t.Fatalf("the running server's directory is not used: %q %v", dir, err)
	}
	if _, err := os.Lstat(filepath.Join(stateBase, "keepgoing")); err == nil {
		t.Fatal("a directory for a second server was made")
	}
	// Once that server has stopped, the name in use decides.
	server.Release()
	if dir, err := panelDirectory(""); err != nil || dir != filepath.Join(stateBase, "keepgoing", "panel") {
		t.Fatalf("after the server stopped: %q %v", dir, err)
	}
}

// The panel's default directory follows the same rule, decided from both
// places: an earlier directory under the configuration directory keeps the
// panel's files under the earlier name in the state directory.
func TestPanelDefaultDirectoryFollowsTheEarlierName(t *testing.T) {
	configBase, stateBase := panelShortDir(t), panelShortDir(t)
	prevState, prevLegacy := panelStateBase, panelLegacyBase
	panelStateBase = func() (string, error) { return stateBase, nil }
	panelLegacyBase = func() (string, error) { return configBase, nil }
	t.Cleanup(func() { panelStateBase, panelLegacyBase = prevState, prevLegacy })
	want := func(name string) string {
		t.Helper()
		p, err := canonicalFuturePath(filepath.Join(stateBase, name, "panel"))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if dir, err := panelDirectory(""); err != nil || dir != want("looptender") {
		t.Fatalf("a new install: %q %v", dir, err)
	}
	if _, found := panelLegacy(); found {
		t.Fatal("a new install has no earlier build's panel")
	}
	// The registry of an install from before the rename, and nothing else.
	if err := os.MkdirAll(filepath.Join(configBase, "keepgoing", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if dir, err := panelDirectory(""); err != nil || dir != want("keepgoing") {
		t.Fatalf("an earlier directory: %q %v", dir, err)
	}
}
