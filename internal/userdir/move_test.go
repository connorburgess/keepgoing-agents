package userdir

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// layoutsOf gives the configuration and state directories of each
// platform's shape under a temporary home: one place on macOS, two on Linux
// and on Windows.
func layoutsOf(t *testing.T) map[string][2]string {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	mac := mkdir(t, root, "mac", "Library", "Application Support")
	return map[string][2]string{
		"macOS":   {mac, mac},
		"Linux":   {mkdir(t, root, "linux", ".config"), mkdir(t, root, "linux", ".local", "state")},
		"Windows": {mkdir(t, root, "windows", "AppData", "Roaming"), mkdir(t, root, "windows", "AppData", "Local")},
	}
}

func needLinks(t *testing.T, base string) {
	t.Helper()
	if err := (Place{Base: base}).CanLink(); err != nil {
		t.Skip("links cannot be made here")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func status(t *testing.T, base string, want Status) Place {
	t.Helper()
	p := PlaceAt(base)
	if p.Status != want {
		t.Fatalf("%s is %s (%s), want %s", base, p.Status, p.Problem, want)
	}
	return p
}

// A move and its undo, in each platform's layout: the files keep their
// contents and modes, the earlier name is left as a link the files are
// still reached through, the name in use follows, and moving back leaves
// the place exactly as it was.
func TestMoveAndMoveBackInEachLayout(t *testing.T) {
	for platform, bases := range layoutsOf(t) {
		t.Run(platform, func(t *testing.T) {
			config, state := bases[0], bases[1]
			needLinks(t, config)
			write(t, filepath.Join(config, LegacyName, "registry", "loops.json"), "registry")
			write(t, filepath.Join(config, LegacyName, "loops", "example-loop", "keepgoing.yaml"), "config")
			write(t, filepath.Join(state, LegacyName, "sessions", "claim.holder"), "claim")
			before := entries(t, config, state)
			places := []string{config}
			if state != config {
				places = append(places, state)
			}
			if got := NameIn(config, state); got != LegacyName {
				t.Fatalf("before: %q", got)
			}

			for _, base := range places {
				if err := status(t, base, Earlier).Move(); err != nil {
					t.Fatal(err)
				}
				p := status(t, base, Moved)
				if target, err := os.Readlink(p.Earlier); err != nil || target != Name {
					t.Fatalf("link: %q %v", target, err)
				}
				if p.marked() {
					t.Fatal("the marker was left")
				}
			}
			if got := NameIn(config, state); got != Name {
				t.Fatalf("after the move: %q", got)
			}
			// The files, under the current name and through the link.
			for _, name := range []string{Name, LegacyName} {
				if read(t, filepath.Join(config, name, "registry", "loops.json")) != "registry" || read(t, filepath.Join(state, name, "sessions", "claim.holder")) != "claim" {
					t.Fatalf("files not reached under %s", name)
				}
			}
			if info, err := os.Stat(filepath.Join(config, Name, "registry", "loops.json")); err != nil || (info.Mode().Perm() != 0o600 && os.PathSeparator == '/') {
				t.Fatalf("mode changed: %v %v", info.Mode(), err)
			}
			// A second move has nothing to do and changes nothing.
			after := entries(t, config, state)
			for _, base := range places {
				if err := status(t, base, Moved).Move(); err != nil {
					t.Fatal(err)
				}
			}
			if again := entries(t, config, state); strings.Join(again, "\n") != strings.Join(after, "\n") {
				t.Fatalf("a second move changed the places: %v then %v", after, again)
			}

			for _, base := range places {
				if err := status(t, base, Moved).MoveBack(); err != nil {
					t.Fatal(err)
				}
				status(t, base, Earlier)
			}
			if got := NameIn(config, state); got != LegacyName {
				t.Fatalf("after moving back: %q", got)
			}
			if back := entries(t, config, state); strings.Join(back, "\n") != strings.Join(before, "\n") {
				t.Fatalf("moving back left the places different: %v then %v", before, back)
			}
			if read(t, filepath.Join(config, LegacyName, "loops", "example-loop", "keepgoing.yaml")) != "config" {
				t.Fatal("a file was lost")
			}
		})
	}
}

// One place moved and the other not, as when a move stops between the two:
// the earlier name is still the one in use, and the moved place is reached
// through its link, so every file is still found.
func TestOnePlaceMovedIsReachedThroughItsLink(t *testing.T) {
	bases := layoutsOf(t)["Linux"]
	config, state := bases[0], bases[1]
	needLinks(t, config)
	write(t, filepath.Join(config, LegacyName, "registry", "loops.json"), "registry")
	write(t, filepath.Join(state, LegacyName, "sessions", "claim.holder"), "claim")
	if err := status(t, config, Earlier).Move(); err != nil {
		t.Fatal(err)
	}
	if got := NameIn(config, state); got != LegacyName {
		t.Fatalf("NameIn = %q", got)
	}
	if read(t, filepath.Join(config, NameIn(config, state), "registry", "loops.json")) != "registry" {
		t.Fatal("the moved place is not reached under the name in use")
	}
}

// What a move will and will not touch.
func TestPlaceAt(t *testing.T) {
	needLinks(t, t.TempDir())
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, base string)
		want  Status
	}{
		{"nothing there", func(*testing.T, string) {}, Fresh},
		{"the earlier directory", func(t *testing.T, base string) { mkdir(t, base, LegacyName) }, Earlier},
		{"the earlier directory and a stale marker", func(t *testing.T, base string) {
			mkdir(t, base, LegacyName)
			write(t, filepath.Join(base, markerName), "")
		}, Earlier},
		{"started under the current name", func(t *testing.T, base string) { mkdir(t, base, Name) }, Current},
		{"moved", func(t *testing.T, base string) {
			mkdir(t, base, Name)
			os.Symlink(Name, filepath.Join(base, LegacyName))
		}, Moved},
		{"moved, the link written in full", func(t *testing.T, base string) {
			mkdir(t, base, Name)
			os.Symlink(filepath.Join(base, Name), filepath.Join(base, LegacyName))
		}, Moved},
		{"a move stopped between its steps", func(t *testing.T, base string) {
			mkdir(t, base, Name)
			write(t, filepath.Join(base, markerName), "")
		}, Interrupted},
		{"directories of both names", func(t *testing.T, base string) {
			mkdir(t, base, Name)
			mkdir(t, base, LegacyName)
		}, Both},
		{"the earlier name is a file", func(t *testing.T, base string) { write(t, filepath.Join(base, LegacyName), "") }, Blocked},
		{"the earlier name links somewhere else", func(t *testing.T, base string) {
			os.Symlink(mkdir(t, base, "elsewhere"), filepath.Join(base, LegacyName))
		}, Blocked},
		{"a link to a current directory that is not there", func(t *testing.T, base string) {
			os.Symlink(Name, filepath.Join(base, LegacyName))
		}, Blocked},
		{"the current name is a file", func(t *testing.T, base string) {
			mkdir(t, base, LegacyName)
			write(t, filepath.Join(base, Name), "")
		}, Blocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			tc.setup(t, base)
			before := entries(t, base)
			p := status(t, base, tc.want)
			if tc.want == Blocked && p.Problem == "" {
				t.Fatal("blocked without saying why")
			}
			// Both and Blocked are refused in either direction, and nothing changes.
			if tc.want == Both || tc.want == Blocked {
				if p.Move() == nil || p.MoveBack() == nil {
					t.Fatal("moved a place it must not touch")
				}
				if after := entries(t, base); strings.Join(after, "\n") != strings.Join(before, "\n") {
					t.Fatalf("a refused move changed the place: %v then %v", before, after)
				}
			}
		})
	}
}

// A computer that started under the current name was never moved: neither
// direction touches it.
func TestACurrentPlaceIsLeftAlone(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, Name, "registry", "loops.json"), "registry")
	before := entries(t, base)
	p := status(t, base, Current)
	if err := p.Move(); err != nil {
		t.Fatal(err)
	}
	if err := p.MoveBack(); err != nil {
		t.Fatal(err)
	}
	if after := entries(t, base); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("%v then %v", before, after)
	}
}

// A move that stopped between its steps, in either direction, leaves the
// files under the current name with the marker beside them. The tool still
// finds them, and either direction finishes from there.
func TestAnInterruptedMoveIsFinishedEitherWay(t *testing.T) {
	needLinks(t, t.TempDir())
	interrupted := func(t *testing.T) string {
		base := t.TempDir()
		write(t, filepath.Join(base, Name, "registry", "loops.json"), "registry")
		write(t, filepath.Join(base, markerName), "")
		if got := NameIn(base); got != Name {
			t.Fatalf("NameIn while interrupted = %q", got)
		}
		return base
	}
	t.Run("forward", func(t *testing.T) {
		base := interrupted(t)
		if err := status(t, base, Interrupted).Move(); err != nil {
			t.Fatal(err)
		}
		p := status(t, base, Moved)
		if p.marked() || read(t, filepath.Join(p.Earlier, "registry", "loops.json")) != "registry" {
			t.Fatal("not finished")
		}
	})
	t.Run("back", func(t *testing.T) {
		base := interrupted(t)
		if err := status(t, base, Interrupted).MoveBack(); err != nil {
			t.Fatal(err)
		}
		p := status(t, base, Earlier)
		if p.marked() || read(t, filepath.Join(p.Earlier, "registry", "loops.json")) != "registry" {
			t.Fatal("not finished")
		}
	})
}

// Another command that makes the earlier directory again in the moment it
// is gone (a build from before the rename would): no link can be left, the
// move says so and where the files are, loses nothing, and the place then
// reads as holding both names, which no later move touches.
func TestMoveWhenTheEarlierDirectoryComesBack(t *testing.T) {
	base := t.TempDir()
	needLinks(t, base)
	write(t, filepath.Join(base, LegacyName, "registry", "loops.json"), "registry")
	betweenSteps = func() { mkdir(t, base, LegacyName, "sessions") }
	t.Cleanup(func() { betweenSteps = nil })
	err := status(t, base, Earlier).Move()
	if err == nil || !strings.Contains(err.Error(), "created again") || !strings.Contains(err.Error(), filepath.Join(base, Name)) {
		t.Fatalf("%v", err)
	}
	betweenSteps = nil
	if read(t, filepath.Join(base, Name, "registry", "loops.json")) != "registry" {
		t.Fatal("the files were lost")
	}
	p := status(t, base, Both)
	if p.Move() == nil {
		t.Fatal("moved over the directory that came back")
	}
	if _, err := os.Lstat(filepath.Join(base, LegacyName, "sessions")); err != nil {
		t.Fatal("the other command's directory was removed")
	}
}

// Where no link can be made, nothing is moved.
func TestMoveWithoutLinksMovesNothing(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, LegacyName, "registry", "loops.json"), "registry")
	// A directory where the probe link goes makes the probe fail everywhere.
	mkdir(t, base, LegacyName+".linktest", "kept")
	before := entries(t, base)
	err := status(t, base, Earlier).Move()
	if !errors.Is(err, ErrNoLink) {
		t.Fatalf("%v", err)
	}
	if after := entries(t, base); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("%v then %v", before, after)
	}
	status(t, base, Earlier)
}

// After a move the current name is in use, Look says each place's two names
// are one directory, and reports nothing under the earlier name as unread.
func TestLookAfterAMove(t *testing.T) {
	config, state := home(t)
	needLinks(t, mkdir(t, config))
	write(t, filepath.Join(config, LegacyName, "registry", "loops.json"), "registry")
	write(t, filepath.Join(state, LegacyName, "sessions", "claim.holder"), "claim")
	places, err := Places()
	if err != nil {
		t.Fatal(err)
	}
	if want := 2 - b2i(config == state); len(places) != want {
		t.Fatalf("%d places, want %d", len(places), want)
	}
	for _, p := range places {
		if err := p.Move(); err != nil {
			t.Fatal(err)
		}
	}
	f := Look()
	var bases []string
	for _, p := range places {
		bases = append(bases, p.Base)
	}
	if f.Name != Name || !reflect.DeepEqual(f.Linked, bases) || len(f.Unread) != 0 || len(f.Unknown) != 0 || len(f.Used) != 2 {
		t.Fatalf("%+v", f)
	}
	if p, _ := Config("registry", "loops.json"); read(t, p) != "registry" {
		t.Fatal("the registry is not found after the move")
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// In two places (Linux, Windows), a move goes through the configuration
// directory first and a move back through the state directory first. At
// every moment in between, including the instant inside a place between its
// steps, the registry and the route list are under the name in use, and the
// session claims are under one of the two names. The wrong order would leave
// a moment in which the registry is under the name not in use.
func TestEveryMomentOfAMoveFindsTheRegistry(t *testing.T) {
	bases := layoutsOf(t)["Linux"]
	config, state := bases[0], bases[1]
	needLinks(t, config)
	write(t, filepath.Join(config, LegacyName, "registry", "loops.json"), "registry")
	write(t, filepath.Join(config, LegacyName, "routes.yaml"), "routes")
	write(t, filepath.Join(state, LegacyName, "sessions", "claim.holder"), "claim")
	moments := 0
	check := func() {
		t.Helper()
		moments++
		name := NameIn(config, state)
		for _, f := range []string{filepath.Join(config, name, "registry", "loops.json"), filepath.Join(config, name, "routes.yaml")} {
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("moment %d: name %s, and %s is not there", moments, name, f)
			}
		}
		found := false
		for _, n := range []string{Name, LegacyName} {
			_, err := os.Stat(filepath.Join(state, n, "sessions", "claim.holder"))
			found = found || err == nil
		}
		if !found {
			t.Fatalf("moment %d: the claims are under neither name", moments)
		}
	}
	betweenSteps = check
	t.Cleanup(func() { betweenSteps = nil })
	places := []Place{PlaceAt(config), PlaceAt(state)}
	order := InMoveOrder(places, false)
	if order[0].Base != config || order[1].Base != state {
		t.Fatalf("the move does not start with the configuration directory: %v", order)
	}
	check()
	for _, p := range order {
		if err := PlaceAt(p.Base).Move(); err != nil {
			t.Fatal(err)
		}
		check()
	}
	if NameIn(config, state) != Name {
		t.Fatal("the current name is not in use after the move")
	}
	order = InMoveOrder([]Place{PlaceAt(config), PlaceAt(state)}, true)
	if order[0].Base != state || order[1].Base != config {
		t.Fatalf("the move back does not start with the state directory: %v", order)
	}
	for _, p := range order {
		if err := PlaceAt(p.Base).MoveBack(); err != nil {
			t.Fatal(err)
		}
		check()
	}
	if NameIn(config, state) != LegacyName {
		t.Fatal("the earlier name is not in use after the move back")
	}
	if moments != 9 { // the start, four in between, four after each step
		t.Fatalf("%d moments checked", moments)
	}
}

// A stale marker beside an earlier directory does not make the current name
// the one in use; only a move actually between its steps does.
func TestOnlyAMoveBetweenItsStepsKeepsTheCurrentName(t *testing.T) {
	bases := layoutsOf(t)["Linux"]
	config, state := bases[0], bases[1]
	mkdir(t, config, LegacyName, "registry")
	write(t, filepath.Join(config, markerName), "")
	if got := NameIn(config, state); got != LegacyName {
		t.Fatalf("a stale marker: %q", got)
	}
	// Between the steps: the current directory and the marker, no earlier one.
	if err := os.Rename(filepath.Join(config, LegacyName), filepath.Join(config, Name)); err != nil {
		t.Fatal(err)
	}
	mkdir(t, state, LegacyName, "sessions")
	if got := NameIn(config, state); got != Name {
		t.Fatalf("between the steps: %q", got)
	}
}
