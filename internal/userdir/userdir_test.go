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
func TestNameInEachLayout(t *testing.T) {
	layouts := []struct {
		platform      string
		config, state []string // under a temporary home
	}{
		{"macOS", []string{"Library", "Application Support"}, []string{"Library", "Application Support"}},
		{"Linux", []string{".config"}, []string{".local", "state"}},
		{"Windows", []string{"AppData", "Roaming"}, []string{"AppData", "Local"}},
	}
	cases := []struct {
		name                      string
		inConfig, inState         []string // directory names to create
		fileInConfig, linkInState bool     // the earlier name as a file, or as a link
		want                      string
	}{
		{name: "nothing there: a new install", want: Name},
		{name: "the earlier name under the configuration directory", inConfig: []string{LegacyName}, want: LegacyName},
		{name: "the earlier name under the state directory only", inState: []string{LegacyName}, want: LegacyName},
		{name: "the current name only", inConfig: []string{Name}, inState: []string{Name}, want: Name},
		{name: "both names: the earlier one is kept", inConfig: []string{LegacyName, Name}, want: LegacyName},
		{name: "the current name in one place, the earlier in the other", inConfig: []string{Name}, inState: []string{LegacyName}, want: LegacyName},
		{name: "the earlier name as a file", fileInConfig: true, want: LegacyName},
		{name: "the earlier name as a link", linkInState: true, want: LegacyName},
	}
	for _, l := range layouts {
		for _, tc := range cases {
			t.Run(l.platform+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				config := mkdir(t, append([]string{root}, l.config...)...)
				state := mkdir(t, append([]string{root}, l.state...)...)
				for _, n := range tc.inConfig {
					mkdir(t, config, n)
				}
				for _, n := range tc.inState {
					mkdir(t, state, n)
				}
				if tc.fileInConfig {
					if err := os.WriteFile(filepath.Join(config, LegacyName), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.linkInState {
					if err := os.Symlink(mkdir(t, root, "elsewhere"), filepath.Join(state, LegacyName)); err != nil {
						t.Skip("links are not available here")
					}
				}
				before := entries(t, config, state)
				if got := NameIn(config, state); got != tc.want {
					t.Fatalf("NameIn = %q, want %q", got, tc.want)
				}
				// The order the places are given in does not matter, and a
				// place that could not be determined is skipped.
				if got := NameIn(state, "", config); got != tc.want {
					t.Fatalf("NameIn, other order = %q, want %q", got, tc.want)
				}
				// Deciding creates and removes nothing.
				if after := entries(t, config, state); !reflect.DeepEqual(before, after) {
					t.Fatalf("deciding changed the places: %v, then %v", before, after)
				}
			})
		}
	}
	if got := NameIn("", ""); got != Name {
		t.Fatalf("no place at all = %q", got)
	}
}

// A place that cannot be inspected counts as holding the earlier name.
func TestNameInAPlaceThatCannotBeInspected(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot search")
	}
	base := mkdir(t, t.TempDir(), "closed")
	if err := os.Chmod(base, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(base, 0o700) })
	if got := NameIn(base); got != LegacyName {
		t.Fatalf("NameIn = %q, want %q", got, LegacyName)
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
	if f := Look(); f.Name != Name || len(f.Used) != 0 || len(f.Ignored) != 0 {
		t.Fatalf("Look = %+v", f)
	}
	for _, base := range []string{config, state} {
		if _, err := os.Lstat(filepath.Join(base, Name)); err == nil {
			t.Fatalf("asking created %s", filepath.Join(base, Name))
		}
	}

	// Its own directory, once made, is the one in use.
	current := mkdir(t, state, Name, "sessions")
	if f := Look(); f.Name != Name || !reflect.DeepEqual(f.Used, []string{filepath.Dir(current)}) || len(f.Ignored) != 0 {
		t.Fatalf("Look = %+v", f)
	}

	// A directory under the earlier name, in the state directory alone,
	// moves both places to the earlier name, and the current-name directory
	// is reported as not read.
	earlier := mkdir(t, state, LegacyName)
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
	if f.Name != LegacyName || !reflect.DeepEqual(f.Used, []string{earlier}) || !reflect.DeepEqual(f.Ignored, []string{filepath.Dir(current)}) {
		t.Fatalf("Look = %+v", f)
	}
}
