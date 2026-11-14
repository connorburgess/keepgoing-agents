package userdir

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// home gives the test a user of its own on every platform: the variables
// each platform's configuration and state directories are read from.
func home(t *testing.T) (config, state string) {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, ".local", "state"))
	t.Setenv("AppData", filepath.Join(root, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "AppData", "Local"))
	config, err := ConfigHome()
	if err != nil {
		t.Fatal(err)
	}
	state, err = StateHome()
	if err != nil {
		t.Fatal(err)
	}
	return config, state
}

// entries lists what is directly in each place.
func entries(t *testing.T, places ...string) []string {
	t.Helper()
	var found []string
	for _, p := range places {
		list, err := os.ReadDir(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list {
			found = append(found, filepath.Join(p, e.Name()))
		}
	}
	return found
}

func mkdir(t *testing.T, elem ...string) string {
	t.Helper()
	p := filepath.Join(elem...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// The three platforms lay the two places out differently: one place for
// both on macOS, two on Linux and on Windows. The decision is the same
// function of what is in them. The places here are stand-ins with each
// platform's shape; TestThisPlatform reads the real ones.
//
// Only the tool's own entries decide. A directory named keepgoing that
// holds a loop's state or configuration, as the guides used to suggest,
// decides nothing.
func TestNameInEachLayout(t *testing.T) {
	layouts := []struct {
		platform      string
		config, state []string // under a temporary home
	}{
		{"macOS", []string{"Library", "Application Support"}, []string{"Library", "Application Support"}},
		{"Linux", []string{".config"}, []string{".local", "state"}},
		{"Windows", []string{"AppData", "Roaming"}, []string{"AppData", "Local"}},
	}
	type place struct{ config, state string }
	cases := []struct {
		name  string
		setup func(t *testing.T, p place)
		want  string
	}{
		{"nothing there: a new install", func(*testing.T, place) {}, Name},
		{"the earlier registry under the configuration directory", func(t *testing.T, p place) { mkdir(t, p.config, LegacyName, "registry") }, LegacyName},
		{"earlier session claims under the state directory only", func(t *testing.T, p place) { mkdir(t, p.state, LegacyName, "sessions") }, LegacyName},
		{"the earlier panel directory only", func(t *testing.T, p place) { mkdir(t, p.state, LegacyName, "panel") }, LegacyName},
		{"an earlier route list only", func(t *testing.T, p place) {
			mkdir(t, p.config, LegacyName)
			if err := os.WriteFile(filepath.Join(p.config, LegacyName, "routes.yaml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, LegacyName},
		{"a loop that init put in the earlier directory", func(t *testing.T, p place) { mkdir(t, p.config, LegacyName, "loops", "example-loop") }, LegacyName},
		{"only a loop's state_dir in a directory of the earlier name", func(t *testing.T, p place) {
			mkdir(t, p.state, LegacyName, "example-loop")
		}, Name},
		{"only a loop's configuration in a directory of the earlier name", func(t *testing.T, p place) {
			mkdir(t, p.config, LegacyName, "example-loop")
			if err := os.WriteFile(filepath.Join(p.config, LegacyName, "example.env"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, Name},
		{"an empty directory of the earlier name", func(t *testing.T, p place) { mkdir(t, p.config, LegacyName) }, Name},
		{"a file of the earlier name", func(t *testing.T, p place) {
			if err := os.WriteFile(filepath.Join(p.config, LegacyName), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, Name},
		{"the current name only", func(t *testing.T, p place) {
			mkdir(t, p.config, Name, "registry")
			mkdir(t, p.state, Name, "sessions")
		}, Name},
		{"the current name, and a loop's state_dir under the earlier one", func(t *testing.T, p place) {
			mkdir(t, p.state, Name, "sessions")
			mkdir(t, p.state, LegacyName, "example-loop")
		}, Name},
		{"the tool's entries under both names: the earlier one is kept", func(t *testing.T, p place) {
			mkdir(t, p.config, Name, "registry")
			mkdir(t, p.config, LegacyName, "registry")
		}, LegacyName},
		{"the current name in one place, the earlier in the other", func(t *testing.T, p place) {
			mkdir(t, p.config, Name, "registry")
			mkdir(t, p.state, LegacyName, "sessions")
		}, LegacyName},
		{"the earlier name as a link to a directory with the tool's entries", func(t *testing.T, p place) {
			if err := os.Symlink(filepath.Dir(mkdir(t, filepath.Dir(p.config), "elsewhere", "registry")), filepath.Join(p.state, LegacyName)); err != nil {
				t.Skip("links are not available here")
			}
		}, LegacyName},
		{"the earlier name as a link to a directory without them", func(t *testing.T, p place) {
			if err := os.Symlink(mkdir(t, filepath.Dir(p.config), "elsewhere"), filepath.Join(p.state, LegacyName)); err != nil {
				t.Skip("links are not available here")
			}
		}, Name},
	}
	for _, l := range layouts {
		for _, tc := range cases {
			t.Run(l.platform+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				p := place{mkdir(t, append([]string{root, "home"}, l.config...)...), mkdir(t, append([]string{root, "home"}, l.state...)...)}
				tc.setup(t, p)
				before := entries(t, p.config, p.state)
				if got := NameIn(p.config, p.state); got != tc.want {
					t.Fatalf("NameIn = %q, want %q", got, tc.want)
				}
				// The order the places are given in does not matter, and a
				// place that could not be determined is skipped.
				if got := NameIn(p.state, "", p.config); got != tc.want {
					t.Fatalf("NameIn, other order = %q, want %q", got, tc.want)
				}
				// Deciding creates and removes nothing.
				if after := entries(t, p.config, p.state); !reflect.DeepEqual(before, after) {
					t.Fatalf("deciding changed the places: %v, then %v", before, after)
				}
			})
		}
	}
	if got := NameIn("", ""); got != Name {
		t.Fatalf("no place at all = %q", got)
	}
	// Every entry the tool keeps decides, each on its own.
	for _, entry := range Entries {
		base := t.TempDir()
		mkdir(t, base, LegacyName, entry)
		if got := NameIn(base); got != LegacyName {
			t.Fatalf("%s alone: NameIn = %q", entry, got)
		}
	}
}

// A place that cannot be inspected counts as holding the earlier name,
// whether it is the place itself or the directory of the earlier name in it.
func TestNameInAPlaceThatCannotBeInspected(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot search")
	}
	for _, closed := range []string{"the place", "the earlier directory"} {
		t.Run(closed, func(t *testing.T) {
			base := mkdir(t, t.TempDir(), "place")
			shut := base
			if closed == "the earlier directory" {
				shut = mkdir(t, base, LegacyName)
			}
			if err := os.Chmod(shut, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(shut, 0o700) })
			if got := NameIn(base); got != LegacyName {
				t.Fatalf("NameIn = %q, want %q", got, LegacyName)
			}
		})
	}
}

// This platform's real places, read from the environment a user has.
func TestThisPlatform(t *testing.T) {
	config, state := home(t)
	root := os.Getenv("HOME")
	var wantConfig, wantState string
	switch runtime.GOOS {
	case "darwin":
		wantConfig = filepath.Join(root, "Library", "Application Support")
		wantState = wantConfig
	case "windows":
		wantConfig, wantState = filepath.Join(root, "AppData", "Roaming"), filepath.Join(root, "AppData", "Local")
	default:
		wantConfig, wantState = filepath.Join(root, ".config"), filepath.Join(root, ".local", "state")
	}
	if config != wantConfig || state != wantState {
		t.Fatalf("config %q, state %q; want %q, %q", config, state, wantConfig, wantState)
	}

	// A new install: everything under the current name, and nothing made.
	if got := DirName(); got != Name {
		t.Fatalf("DirName = %q", got)
	}
	p, err := Config("registry")
	if err != nil || p != filepath.Join(config, Name, "registry") {
		t.Fatalf("Config = %q %v", p, err)
	}
	p, err = State("sessions")
	if err != nil || p != filepath.Join(state, Name, "sessions") {
		t.Fatalf("State = %q %v", p, err)
	}
	if f := Look(); f.Name != Name || len(f.Used) != 0 || len(f.Unread) != 0 || len(f.Unknown) != 0 {
		t.Fatalf("Look = %+v", f)
	}
	for _, base := range []string{config, state} {
		if _, err := os.Lstat(filepath.Join(base, Name)); err == nil {
			t.Fatalf("asking created %s", filepath.Join(base, Name))
		}
	}

	// Its own claims, once made, are the ones in use. A loop's state_dir in
	// a directory of the earlier name beside them changes nothing, and is
	// not reported as anything of the tool's.
	current := mkdir(t, state, Name, "sessions")
	mkdir(t, state, LegacyName, "example-loop")
	if f := Look(); f.Name != Name || !reflect.DeepEqual(f.Used, []string{current}) || len(f.Unread) != 0 {
		t.Fatalf("Look = %+v", f)
	}

	// The tool's own claims under the earlier name, in the state directory
	// alone, move both places to the earlier name. A registry under the
	// current name is then reported as not read; the claims under it are
	// not, as claims are honoured under both names.
	registry := mkdir(t, config, Name, "registry")
	earlier := mkdir(t, state, LegacyName, "sessions")
	if got := DirName(); got != LegacyName {
		t.Fatalf("DirName = %q", got)
	}
	if p, _ := Config("registry"); p != filepath.Join(config, LegacyName, "registry") {
		t.Fatalf("Config = %q", p)
	}
	if p, _ := State("sessions"); p != filepath.Join(state, LegacyName, "sessions") {
		t.Fatalf("State = %q", p)
	}
	f := Look()
	if f.Name != LegacyName || !reflect.DeepEqual(f.Used, []string{earlier}) || !reflect.DeepEqual(f.Unread, []string{registry}) || len(f.Unknown) != 0 || len(f.Linked) != 0 {
		t.Fatalf("Look = %+v", f)
	}
}

// Where one name is a link to the other, in either direction, the two are
// one directory: nothing is "under the other name", and Look says so.
func TestLookWhenTheTwoNamesAreOneDirectory(t *testing.T) {
	for _, link := range []string{Name, LegacyName} {
		t.Run(link+" is the link", func(t *testing.T) {
			config, state := home(t)
			real := LegacyName
			if link == LegacyName {
				real = Name
			}
			mkdir(t, config, real, "registry")
			mkdir(t, state, real, "sessions")
			places := []string{config}
			if state != config {
				places = append(places, state)
			}
			for _, base := range places {
				if err := os.Symlink(real, filepath.Join(base, link)); err != nil {
					t.Skip("links are not available here")
				}
			}
			// Where the earlier name is a link to the current one, as
			// keepgoing migrate leaves it, the current name is in use; the
			// other way round, the tool's entries are under the earlier name.
			// Either way both lead to the same files.
			want := LegacyName
			if link == LegacyName {
				want = Name
			}
			f := Look()
			if f.Name != want || len(f.Unread) != 0 || !reflect.DeepEqual(f.Linked, places) || len(f.Unknown) != 0 {
				t.Fatalf("Look = %+v", f)
			}
			p, err := Config("registry")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("the registry is not reached at %s: %v", p, err)
			}
		})
	}
}

// A directory of the earlier name that cannot be looked into is named as
// such, not the place it is in.
func TestLookNamesTheDirectoryThatCannotBeInspected(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot search")
	}
	config, _ := home(t)
	shut := mkdir(t, config, LegacyName)
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o700) })
	if f := Look(); f.Name != LegacyName || !reflect.DeepEqual(f.Unknown, []string{shut}) {
		t.Fatalf("Look = %+v", f)
	}
}
