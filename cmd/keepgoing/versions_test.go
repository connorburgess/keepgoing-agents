package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
// dirty label say nothing; older only between plain releases.
func TestVersionWordsCompareWholeLabels(t *testing.T) {
	for _, c := range []struct{ label, ref, kind, want, text string }{
		{"", "v0.2.1", "newest_release", "not_recorded", "Not recorded."},
		{"v0.2.1", "v0.2.1", "newest_release", "current", "keepgoing v0.2.1, the newest build here."},
		{"v0.2.0", "v0.2.1", "newest_release", "older", "keepgoing v0.2.0, older than the newest build here (v0.2.1)."},
		{"v0.1.10", "v0.1.9", "newest_release", "different", "keepgoing v0.1.10, a different build from the newest build here (v0.1.9)."},
		{"v0.2.1-dev.26f91db", "v0.2.1", "newest_release", "different", "keepgoing v0.2.1-dev.26f91db, a different build from the newest build here (v0.2.1)."},
		{"v0.2.0-dev.26f91db", "v0.2.1", "newest_release", "different", "keepgoing v0.2.0-dev.26f91db, a different build from the newest build here (v0.2.1)."},
		{"dev", "v0.2.1", "newest_release", "unknown", "A keepgoing build with no version recorded (dev), so whether it is the newest build here (v0.2.1) is unknown."},
		{"26f91db-dirty", "26f91db-dirty", "this_panel", "unknown", "keepgoing 26f91db-dirty, built with uncommitted changes, so whether it is the same build as this panel's build (26f91db-dirty) is unknown."},
		{"v0.2.1-dev.ab565b1", "v0.2.1-dev.ab565b1", "this_panel", "current", "keepgoing v0.2.1-dev.ab565b1, the same build as this panel."},
		{"v0.2.1", "dev", "this_panel", "unknown", "keepgoing v0.2.1; this panel has no version recorded (dev), so there is nothing to compare it with."},
		{"v0.2.1\nInjected", "v0.2.1", "newest_release", "different", "keepgoing unreadable, a different build from the newest build here (v0.2.1)."},
	} {
		a, text := dockVersionAgainst(c.label, c.ref, c.kind)
		if a != c.want || text != c.text {
			t.Errorf("%q against %q: %s %q", c.label, c.ref, a, text)
		}
	}
	// The newest plain release, compared by number; dev and dirty labels never count.
	if n, k := dockNewest([]string{"v0.1.9", "v0.1.10", "v0.2.0-dev.1", "dev", "", "9.9.9-dirty"}, "dev"); n != "v0.1.10" || k != "newest_release" {
		t.Errorf("%s %s", n, k)
	}
	if n, k := dockNewest([]string{"dev", "v0.2.1-dev.ab565b1"}, "v0.2.1-dev.ab565b1"); n != "v0.2.1-dev.ab565b1" || k != "this_panel" {
		t.Errorf("%s %s", n, k)
	}
	for _, c := range []struct{ v, want, text string }{
		{bundledPluginVersion, "current", "BB plugin " + bundledPluginVersion + ", the version this panel's build was released with."},
		{"0.2.9", "older", "BB plugin 0.2.9, older than the " + bundledPluginVersion + " this panel's build was released with."},
		{"0.9.0", "different", "BB plugin 0.9.0, newer than the " + bundledPluginVersion + " this panel's build was released with."},
		{"0.3.1-local", "different", "BB plugin 0.3.1-local, a different version from the " + bundledPluginVersion + " this panel's build was released with."},
	} {
		if a, text := dockComponentAgainst("BB plugin", c.v, bundledPluginVersion); a != c.want || text != c.text {
			t.Errorf("%s: %s %q", c.v, a, text)
		}
	}
}

// A report is recorded only for a known name and a plain version, at most
// eight, newest first, in a private file; a damaged file is replaced.
func TestClientReportsAreBoundedAndPrivate(t *testing.T) {
	// A panel directory short enough for the panel's socket, as panelDirectory requires.
	dir := filepath.Join(panelShortDir(t), "panel")
	for _, bad := range []string{"", "bb-plugin", "@0.3.1", "other@0.3.1", "bb-plugin@", "bb-plugin@0.3.1 rm", "bb-plugin@" + strings.Repeat("1", 40), "macos-menu@-1"} {
		if _, _, ok := parseClientFlag(bad); ok {
			t.Errorf("%q accepted", bad)
		}
		noteClient(bad, dir)
	}
	if f, err := loadClients(dir); f != nil || err != nil {
		t.Fatalf("an unusable value recorded something: %+v %v", f, err)
	}
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	recordClient(dir, "bb-plugin", "0.3.1", now)
	recordClient(dir, "macos-menu", "0.1.0", now.Add(time.Minute))
	recordClient(dir, "bb-plugin", "0.3.2", now.Add(2*time.Minute)) // replaces the plugin's report from this executable
	f, err := loadClients(dir)
	if err != nil || f == nil || len(f.Clients) != 2 || f.Clients[0].Name != "bb-plugin" || f.Clients[0].ClientVersion != "0.3.2" || f.Clients[1].Name != "macos-menu" {
		t.Fatalf("%+v %v", f, err)
	}
	if c := f.Clients[0]; !filepath.IsAbs(c.CLIPath) || c.CLIVersion != dockBuildLabel(version) {
		t.Errorf("%+v", c)
	}
	info, err := os.Stat(filepath.Join(dir, clientsName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info.Mode(), err)
	}
	// Bounded: reports from nine executables keep the newest eight.
	many := clientsFile{Version: 1}
	for i := 0; i < 9; i++ {
		many.Clients = append(many.Clients, clientReport{Name: "bb-plugin", ClientVersion: "0.3.1", CLIPath: filepath.Join(dir, "bin", string(rune('a'+i))), CLIVersion: "v0.2.1", SeenAt: now.Add(time.Duration(i) * time.Second)})
	}
	b, _ := json.Marshal(many)
	if err := os.WriteFile(filepath.Join(dir, clientsName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClients(dir); err == nil {
		t.Fatal("nine reports loaded")
	}
	// A damaged file reads as unavailable and is replaced by the next report.
	if err := os.WriteFile(filepath.Join(dir, clientsName), []byte(`{"version":1,"clients":[],"extra":1}`), 0o600); err != nil {
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

// The view: every place a build runs, against the newest release here, with
// what to do for each that is behind. Paths only with identifiers.
func TestVersionsViewAgainstTheNewestBuild(t *testing.T) {
	previous := version
	version = "v0.2.1"
	t.Cleanup(func() { version = previous })
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	loops := []dockLoop{
		{Key: "l1", Name: "example-docs", Build: dockBuildOf("v0.2.1", version), Versions: dockLoopVersions{Daemon: "v0.2.1", Service: "same_file", ServicePath: "/fictional/bin/keepgoing"}},
		{Key: "l2", Name: "example-api", Build: dockBuildOf("v0.2.0", version), Versions: dockLoopVersions{Daemon: "v0.2.0", Service: "different_file", ServicePath: "/fictional/old/keepgoing"}},
	}
	clients := &clientsFile{Version: 1, Clients: []clientReport{
		{Name: "bb-plugin", ClientVersion: "0.3.0", CLIPath: "/fictional/bb/keepgoing", CLIVersion: "v0.2.2", SeenAt: now.Add(-5 * time.Minute)},
	}}
	v := dockVersionsOf(loops, clients, nil, "/fictional/panel/keepgoing", false, now)
	// The plugin's CLI is the newest release here, so the panel is behind too.
	if v.Newest != "v0.2.2" || v.Reference != "newest_release" || v.Clients != "available" {
		t.Fatalf("%+v", v)
	}
	got := map[string]string{}
	for _, r := range v.Rows {
		got[r.Title] = r.Availability
		if r.Path != "" {
			t.Errorf("a path without identifiers: %+v", r)
		}
		if (r.Availability == "current" || r.Availability == "same_file") && r.Update != "" {
			t.Errorf("an update for a current row: %+v", r)
		}
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
	}
	if len(v.Rows) != len(want) || v.Behind != 7 || v.Summary != "7 places differ from v0.2.2" {
		t.Errorf("%d rows, behind %d, %q", len(v.Rows), v.Behind, v.Summary)
	}
	// With identifiers, the files are shown.
	v = dockVersionsOf(loops, clients, nil, "/fictional/panel/keepgoing", true, now)
	paths := 0
	for _, r := range v.Rows {
		if r.Path != "" {
			paths++
		}
	}
	if paths != 4 { // the panel, two services, the plugin's CLI
		t.Errorf("%d paths", paths)
	}
	// A loop with nothing recorded is named once, not given a row.
	v = dockVersionsOf(append(loops, dockLoop{Key: "l3", Name: "example-quiet", Build: dockBuildOf("", version)}), nil, nil, "", false, now)
	if len(v.Unrecorded) != 1 || v.Unrecorded[0].Name != "example-quiet" {
		t.Errorf("%+v", v.Unrecorded)
	}
	for _, r := range v.Rows {
		if strings.HasPrefix(r.Title, "example-quiet") {
			t.Errorf("a row for a loop with nothing recorded: %+v", r)
		}
	}
	// Nothing reported and everything on one release.
	v = dockVersionsOf(loops[:1], nil, nil, "", false, now)
	if v.Summary != "All on v0.2.1" || v.Behind != 0 || v.Clients != "none" || !strings.Contains(v.Lede, "--client") {
		t.Errorf("%+v", v)
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
	if r.Versions.Behind != 2 || r.Versions.Newest != "v0.2.1" {
		t.Errorf("%+v", r.Versions)
	}
	// The daemon's label stays out of status JSON.
	b, _ := json.Marshal(in.Daemon)
	if strings.Contains(string(b), "v0.2.0") {
		t.Errorf("%s", b)
	}
}
