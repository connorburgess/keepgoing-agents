package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The plugin's and the menu's versions this build was released with are the
// ones in their own sources.
func TestBundledComponentVersions(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "integrations", "bb-plugin-keepgoing", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil || pkg.Version != bundledPluginVersion {
		t.Errorf("package.json %q, bundledPluginVersion %q", pkg.Version, bundledPluginVersion)
	}
	m, err := os.ReadFile(filepath.Join("..", "..", "integrations", "macos", "VERSION"))
	if err != nil || strings.TrimSpace(string(m)) != bundledMenuVersion {
		t.Errorf("VERSION %q, bundledMenuVersion %q (%v)", m, bundledMenuVersion, err)
	}
}

// Each label against the reference, as words. Whole labels; dev and a shared
// dirty label say nothing; older only between plain releases; a dev build
// against a release is a different build whose order is not known.
func TestVersionWordsCompareWholeLabels(t *testing.T) {
	for _, c := range []struct{ label, ref, kind, want, text string }{
		{"", "v0.2.1", "newest_release", "not_recorded", "Not recorded."},
		{"v0.2.1", "v0.2.1", "newest_release", "current", "keepgoing v0.2.1, the newest release here."},
		{"v0.2.0", "v0.2.1", "newest_release", "older", "keepgoing v0.2.0, older than the newest release here (v0.2.1)."},
		{"v0.1.9", "v0.1.10", "newest_release", "older", "keepgoing v0.1.9, older than the newest release here (v0.1.10)."},
		{"v0.2.1-dev.26f91db", "v0.2.1", "newest_release", "different", "keepgoing v0.2.1-dev.26f91db, a different build from the newest release here (v0.2.1); which of them is newer is not known."},
		{"v0.2.1-dev.abc1234", "v0.2.0", "newest_release", "different", "keepgoing v0.2.1-dev.abc1234, a different build from the newest release here (v0.2.0); which of them is newer is not known."},
		{"dev", "v0.2.1", "newest_release", "unknown", "A keepgoing build with no version recorded (dev), so whether it is the newest release here (v0.2.1) is unknown."},
		{"26f91db-dirty", "26f91db-dirty", "this_panel", "unknown", "keepgoing 26f91db-dirty, built with uncommitted changes, so whether it is the same build as this panel's build (26f91db-dirty) is unknown."},
		{"v0.2.1-dev.ab565b1", "v0.2.1-dev.ab565b1", "this_panel", "current", "keepgoing v0.2.1-dev.ab565b1, the same build as this panel."},
		{"v0.2.1", "dev", "this_panel", "unknown", "keepgoing v0.2.1; this panel has no version recorded (dev), so there is nothing to compare it with."},
		{"v0.2.1\nInjected", "v0.2.1", "newest_release", "different", "keepgoing unreadable, a different build from the newest release here (v0.2.1); which of them is newer is not known."},
	} {
		a, text := dockVersionAgainst(c.label, c.ref, c.kind)
		if a != c.want || text != c.text {
			t.Errorf("%q against %q: %s %q", c.label, c.ref, a, text)
		}
	}
	if n, k := dockNewest([]string{"v0.1.9", "v0.1.10", "v0.2.0-dev.1", "dev", "", "9.9.9-dirty"}, "dev"); n != "v0.1.10" || k != "newest_release" {
		t.Errorf("%s %s", n, k)
	}
	if n, k := dockNewest([]string{"dev", "v0.2.1-dev.ab565b1"}, "v0.2.1-dev.ab565b1"); n != "v0.2.1-dev.ab565b1" || k != "this_panel" {
		t.Errorf("%s %s", n, k)
	}
	for _, c := range []struct{ v, want, text string }{
		{bundledPluginVersion, "current", "BB plugin " + bundledPluginVersion + ", the version this build expects."},
		{"0.2.9", "older", "BB plugin 0.2.9, older than the " + bundledPluginVersion + " this build expects."},
		{"0.9.0", "newer", "BB plugin 0.9.0, newer than this build expects (" + bundledPluginVersion + ")."},
		{"0.3.1-local", "different", "BB plugin 0.3.1-local, a different version from the " + bundledPluginVersion + " this build expects; which is newer is not known."},
	} {
		if a, text := dockComponentAgainst("BB plugin", c.v, bundledPluginVersion); a != c.want || text != c.text {
			t.Errorf("%s: %s %q", c.v, a, text)
		}
	}
}

// A report is recorded only for a known name and a plain version, one per
// client name, without a path, in a private file in a panel directory that
// already exists; an unchanged report under an hour old is not rewritten;
// a damaged file is replaced.
func TestClientReportsAreBoundedAndPrivate(t *testing.T) {
	// A panel directory short enough for the panel's socket, as the panel requires.
	dir := filepath.Join(panelShortDir(t), "panel")
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	// The directory does not exist yet: nothing is recorded, and it is not created.
	recordClient(dir, "bb-plugin", "0.3.1", now)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a report created the panel directory: %v", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "bb-plugin", "@0.3.1", "other@0.3.1", "bb-plugin@", "bb-plugin@0.3.1 rm", "bb-plugin@" + strings.Repeat("1", 40), "macos-menu@-1"} {
		if _, _, ok := parseClientFlag(bad); ok {
			t.Errorf("%q accepted", bad)
		}
		noteClient(bad, dir)
	}
	if f, err := loadClients(dir); f != nil || err != nil {
		t.Fatalf("an unusable value recorded something: %+v %v", f, err)
	}
	recordClient(dir, "bb-plugin", "0.3.1", now)
	recordClient(dir, "macos-menu", "0.1.0", now.Add(time.Minute))
	recordClient(dir, "bb-plugin", "0.3.2", now.Add(2*time.Minute)) // one entry per name: replaces the plugin's
	f, err := loadClients(dir)
	if err != nil || f == nil || len(f.Clients) != 2 || f.Clients[0].Name != "bb-plugin" || f.Clients[0].ClientVersion != "0.3.2" || f.Clients[1].Name != "macos-menu" {
		t.Fatalf("%+v %v", f, err)
	}
	if c := f.Clients[0]; c.CLIVersion != dockBuildLabel(version) {
		t.Errorf("%+v", c)
	}
	b, _ := os.ReadFile(filepath.Join(dir, clientsName))
	if strings.Contains(string(b), string(filepath.Separator)+"keepgoing") || strings.Contains(string(b), "Path") {
		t.Errorf("a path recorded: %s", b)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dir, clientsName)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%v %v", info.Mode(), err)
		}
	}
	// The same report within the hour writes nothing; after it, the file is rewritten.
	recordClient(dir, "macos-menu", "0.1.0", now.Add(30*time.Minute))
	if again, _ := loadClients(dir); !again.Clients[1].SeenAt.Equal(now.Add(time.Minute)) {
		t.Errorf("an unchanged report was rewritten within the hour: %+v", again)
	}
	recordClient(dir, "macos-menu", "0.1.0", now.Add(2*time.Hour))
	if again, _ := loadClients(dir); again.Clients[0].Name != "macos-menu" || !again.Clients[0].SeenAt.Equal(now.Add(2*time.Hour)) {
		t.Errorf("an hour-old report was not refreshed: %+v", again)
	}
	// Two reports for one name are refused.
	dup := clientsFile{Version: 1, Clients: []clientReport{
		{Name: "bb-plugin", ClientVersion: "0.3.1", CLIVersion: "v0.2.1", SeenAt: now},
		{Name: "bb-plugin", ClientVersion: "0.3.0", CLIVersion: "v0.2.0", SeenAt: now},
	}}
	b, _ = json.Marshal(dup)
	if err := os.WriteFile(filepath.Join(dir, clientsName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClients(dir); err == nil {
		t.Fatal("two reports for one client loaded")
	}
	// A file from before this change, with a path, reads as unavailable and is replaced.
	if err := os.WriteFile(filepath.Join(dir, clientsName), []byte(`{"version":1,"clients":[{"name":"bb-plugin","clientVersion":"0.3.1","cliPath":"/x","cliVersion":"v0.2.1","seenAt":"2030-01-01T12:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClients(dir); err == nil {
		t.Fatal("an unknown field loaded")
	}
	recordClient(dir, "macos-menu", "0.1.0", now)
	if f, err := loadClients(dir); err != nil || len(f.Clients) != 1 {
		t.Fatalf("%+v %v", f, err)
	}
}

// The view against the newest build, with what to do for each that is
// older; nothing for a build whose order is unknown.
func TestVersionsViewAgainstTheNewestBuild(t *testing.T) {
	previous := version
	version = "v0.2.1"
	t.Cleanup(func() { version = previous })
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	loops := []dockLoop{
		{Key: "l1", Name: "example-docs", Build: dockBuildOf("v0.2.1", version), Versions: dockLoopVersions{Daemon: "v0.2.1", Service: "same_file"}},
		{Key: "l2", Name: "example-api", Build: dockBuildOf("v0.2.0", version), Versions: dockLoopVersions{Daemon: "v0.2.0", Service: "different_file"}},
	}
	clients := &clientsFile{Version: 1, Clients: []clientReport{
		{Name: "bb-plugin", ClientVersion: "0.3.0", CLIVersion: "v0.2.2", SeenAt: now.Add(-5 * time.Minute)},
	}}
	v := dockVersionsOf(loops, clients, nil, now)
	// The plugin's CLI is the newest release here, so the panel is behind too.
	if v.Newest != "v0.2.2" || v.Reference != "newest_release" || v.Clients != "available" {
		t.Fatalf("%+v", v)
	}
	got, update := map[string]string{}, map[string]bool{}
	for _, r := range v.Rows {
		got[r.Title], update[r.Title] = r.Availability, r.Update != ""
	}
	want := map[string]string{
		"This panel": "older", "example-docs: last check": "older", "example-docs: daemon": "older", "example-docs: service": "same_file",
		"example-api: last check": "older", "example-api: daemon": "older", "example-api: service": "different_file",
		"BB plugin": "older", "BB plugin: the keepgoing it runs": "current",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %q, want %q", k, got[k], w)
		}
		if update[k] != (w == "older") {
			t.Errorf("%s: update %t", k, update[k])
		}
	}
	if len(v.Rows) != len(want) || v.Behind != 7 || v.Summary != "7 places need a look, against v0.2.2" {
		t.Errorf("%d rows, behind %d, %q", len(v.Rows), v.Behind, v.Summary)
	}
	// A loop with nothing recorded is named once, not given a row.
	v = dockVersionsOf(append(loops, dockLoop{Key: "l3", Name: "example-quiet", Build: dockBuildOf("", version)}), nil, nil, now)
	if len(v.Unrecorded) != 1 || v.Unrecorded[0].Name != "example-quiet" {
		t.Errorf("%+v", v.Unrecorded)
	}
	// With this panel the newest release, a service on another file is told
	// to restart, or to take this panel's build only if its file is older.
	v = dockVersionsOf(loops, nil, nil, now)
	for _, r := range v.Rows {
		if r.Title == "example-api: service" && r.Update != "Restart the service, or install it again from this panel's build if its file is older." {
			t.Errorf("%+v", r)
		}
	}
	if v.Clients != "none" || !strings.Contains(v.Lede, "--client") {
		t.Errorf("%+v", v)
	}
}

// The review's case: everything here runs one dev build and an old client
// reported from a release. The old report does not become the reference, no
// dev row is told to update, nothing is advised toward an older build, and
// a plugin newer than this build expects is not told to change.
func TestVersionsWithDevBuildsAndOldReports(t *testing.T) {
	previous := version
	version = "v0.2.1-dev.abc1234"
	t.Cleanup(func() { version = previous })
	now := time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC)
	loops := []dockLoop{{Key: "l1", Name: "example-docs", Build: dockBuildOf(version, version), Versions: dockLoopVersions{Daemon: version, Service: "different_file"}}}
	stale := &clientsFile{Version: 1, Clients: []clientReport{
		{Name: "bb-plugin", ClientVersion: "0.3.0", CLIVersion: "v0.2.0", SeenAt: now.Add(-8 * 24 * time.Hour)},
		{Name: "macos-menu", ClientVersion: "0.9.0", CLIVersion: version, SeenAt: now.Add(-time.Hour)},
	}}
	v := dockVersionsOf(loops, stale, nil, now)
	if v.Reference != "this_panel" || v.Newest != version {
		t.Fatalf("an old report set the reference: %+v", v)
	}
	for _, r := range v.Rows {
		if r.Update != "" {
			t.Errorf("advice with no newer build known: %+v", r)
		}
		switch r.Title {
		case "This panel", "example-docs: last check", "example-docs: daemon", "macOS menu: the keepgoing it runs":
			if r.Availability != "current" {
				t.Errorf("%+v", r)
			}
		case "BB plugin":
			if r.Availability != "stale" || r.Text != "BB plugin 0.3.0, not seen in the last seven days." || r.SeenAt == nil || !r.SeenAt.Equal(now.Add(-8*24*time.Hour)) {
				t.Errorf("%+v", r)
			}
		case "macOS menu":
			if r.Availability != "newer" || r.Text != "macOS menu 0.9.0, newer than this build expects ("+bundledMenuVersion+")." {
				t.Errorf("%+v", r)
			}
		}
	}
	// Only the service on another file counts; the stale report and the
	// newer menu count for nothing, and with no newer build known the service
	// gets no advice.
	if v.Behind != 1 || v.Summary != "1 place needs a look, against "+version {
		t.Errorf("%d %q", v.Behind, v.Summary)
	}
	// A fresh report from a plain release, with dev builds here: a different
	// build each way, order unknown, and still no advice.
	fresh := &clientsFile{Version: 1, Clients: []clientReport{{Name: "bb-plugin", ClientVersion: bundledPluginVersion, CLIVersion: "v0.2.0", SeenAt: now.Add(-time.Hour)}}}
	// Every place seen now on one dev build, and the only release a
	// client's report: rows say which is newer is not known, the picker does
	// not count them, and the release is "the newest release here".
	same := []dockLoop{{Key: "l1", Name: "example-docs", Build: dockBuildOf(version, version), Versions: dockLoopVersions{Daemon: version}}}
	v = dockVersionsOf(same, fresh, nil, now)
	if v.Newest != "v0.2.0" || v.Behind != 0 || v.Summary != "Nothing known to be older" {
		t.Fatalf("%+v", v)
	}
	for _, r := range v.Rows {
		if r.Title == "example-docs: last check" && r.Text != "keepgoing "+version+", a different build from the newest release here (v0.2.0); which of them is newer is not known." {
			t.Errorf("%q", r.Text)
		}
	}
	v = dockVersionsOf(loops, fresh, nil, now)
	for _, r := range v.Rows {
		if r.Update != "" {
			t.Errorf("advice toward an older or unordered build: %+v", r)
		}
		if r.Title == "example-docs: last check" && (r.Availability != "different" || !strings.Contains(r.Text, "which of them is newer is not known")) {
			t.Errorf("%+v", r)
		}
	}
}

// The panel's own row is always this panel, even for a dirty or dev build.
func TestVersionsPanelRowIsThisPanel(t *testing.T) {
	previous := version
	t.Cleanup(func() { version = previous })
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	for label, want := range map[string]string{
		"26f91db-dirty":      "keepgoing 26f91db-dirty, this panel's own build.",
		"v0.2.1-dev.abc1234": "keepgoing v0.2.1-dev.abc1234, this panel's own build.",
		"dev":                "A keepgoing build with no version recorded (dev).",
	} {
		version = label
		if r := dockVersionsOf(nil, nil, nil, now).Rows[0]; r.Kind != "panel" || r.Text != want {
			t.Errorf("%s: %q", label, r.Text)
		}
	}
}

// A loop whose daemon runs another build, or whose service runs another
// file, is listed under "Worth knowing"; never as a need.
func TestVersionsInWorthKnowingNeverANeed(t *testing.T) {
	previous := version
	version = "v0.2.1"
	t.Cleanup(func() { version = previous })
	in := dockEvidence{controls: &clientControls{Enabled: true}, status: func(s *statusOut) { s.State.LastCheck.Version = "v0.2.1" }}.input()
	in.Entry.ID, in.Entry.Loop = "loop_00000000000000000000000000000001", "example-docs"
	in.Daemon = daemonStatus{State: "responsive", version: "v0.2.0"}
	in.ServiceFile = "different_file"
	r := buildDockReport([]dockInput{in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if !r.AllClear || len(r.NeedsYou) != 0 || len(r.Worth) != 1 {
		t.Fatalf("allClear %t needs %d worth %+v", r.AllClear, len(r.NeedsYou), r.Worth)
	}
	text := r.Worth[0].Text.render(dockNow)
	if !strings.Contains(text, "Its daemon runs keepgoing v0.2.0; this panel is keepgoing v0.2.1.") || !strings.Contains(text, "Its service runs a different file from this panel's binary.") {
		t.Errorf("%q", text)
	}
	// The view is not built unless asked for.
	if r.Versions != nil {
		t.Errorf("%+v", r.Versions)
	}
	if v := dockVersionsOf(r.Loops, nil, nil, dockNow); v.Behind != 2 || v.Newest != "v0.2.1" {
		t.Errorf("%+v", v)
	}
	// The daemon's label stays out of status JSON.
	b, _ := json.Marshal(in.Daemon)
	if strings.Contains(string(b), "v0.2.0") {
		t.Errorf("%s", b)
	}
}
