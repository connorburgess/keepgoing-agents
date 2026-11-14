// Package userdir says where the tool keeps the files that belong to a user
// and to no single loop: the loop registry, the standard route list and the
// default home of new loops under the configuration directory, and the
// session claims and the panel's runtime files under the state directory.
//
// Those files live in one directory under each. On a computer that has
// never run the tool it is named looptender. On a computer where the tool
// already keeps such files in a directory named keepgoing, its earlier name,
// under either place, that name is kept for both, and nothing is moved: a
// build from before the rename still finds its files there. What a person
// keeps in a directory of that name, such as a loop's own state, does not
// decide the name.
package userdir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// Name is the directory's name on a computer with no earlier files.
	Name = "looptender"
	// LegacyName is the directory's name from before the tool was renamed.
	LegacyName = "keepgoing"
)

// ConfigHome is the user's configuration directory: $XDG_CONFIG_HOME or
// ~/.config on Linux and other Unix systems, Application Support on macOS,
// and the roaming application data folder on Windows.
func ConfigHome() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("the user configuration directory is not an absolute path")
	}
	return base, nil
}

// StateHome is the user's state directory, for runtime files:
// $XDG_STATE_HOME or ~/.local/state on Linux and other Unix systems,
// Application Support on macOS, and the local application data folder on
// Windows.
func StateHome() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return os.UserConfigDir() // ~/Library/Application Support
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return os.UserConfigDir()
	}
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// Entries are the names of what the tool itself keeps in its directory: the
// loop registry, the standard route list and the default home of new loops
// under the configuration directory, and the session claims and the panel's
// runtime files under the state directory. On macOS the two are one place.
var Entries = []string{"registry", "routes.yaml", "loops", "sessions", "panel"}

// NameIn decides the directory's name from the places it could be in: the
// earlier name when one of the tool's own entries (Entries) is in a
// directory of that name under any of them, otherwise the current one.
//
// Only the tool's own entries count. A directory named keepgoing that holds
// something else, such as a loop's state_dir or configuration that a person
// put there, decides nothing: otherwise the first check of such a loop on a
// new install would change where every other command looks.
//
// An empty base is a place that could not be determined and is skipped. A
// place that cannot be inspected counts as holding the earlier name: the
// choice that leaves an existing setup where it is.
//
// Two exceptions, both for keepgoing migrate. In a place where the earlier
// name is only a link to the directory of the current name beside it, as a
// move leaves it, that place counts as holding the current name. And while
// a move is between its steps in any place, the current name is in use.
//
// The decision is made afresh by every command and never remembered. Two
// commands can still decide differently: around the moment an entry of the
// earlier name first appears (a build from before the rename ran), and when
// they are given different places (on Linux, a service started without the
// XDG_CONFIG_HOME its user's shell has). The session claims are kept safe
// through that by the claim itself, which looks under both names; see
// session.Take.
func NameIn(bases ...string) string {
	// A move that is between its steps in one place (see Place.Move) has
	// the files there under the current name only. The current name is in
	// use until it is finished, whatever the other place holds: what is
	// there is session claims and the panel's files, which are found under
	// either name.
	for _, base := range bases {
		if betweenMoveSteps(base) {
			return Name
		}
	}
	for _, base := range bases {
		// A link of the earlier name to the directory of the current name
		// beside it is what keepgoing migrate leaves: the files are under
		// the current name, and the link is for builds from before the
		// rename and for paths recorded with the earlier name.
		if linksToCurrent(base) {
			continue
		}
		if found, unknown := earlierEntry(base); found != "" || unknown {
			return LegacyName
		}
	}
	return Name
}

// betweenMoveSteps reports whether a move in either direction stopped in
// base between its steps: its marker is there, the directory of the current
// name is there, and nothing of the earlier name is.
func betweenMoveSteps(base string) bool {
	if base == "" {
		return false
	}
	if info, err := os.Lstat(filepath.Join(base, markerName)); err != nil || !info.Mode().IsRegular() {
		return false
	}
	if _, err := os.Lstat(filepath.Join(base, LegacyName)); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	info, err := os.Lstat(filepath.Join(base, Name))
	return err == nil && info.IsDir()
}

// linksToCurrent reports whether base holds the earlier name as a symbolic
// link to the directory of the current name beside it, written either as
// that name or as its full path.
func linksToCurrent(base string) bool {
	if base == "" {
		return false
	}
	link := filepath.Join(base, LegacyName)
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	return target == Name || filepath.Clean(target) == filepath.Join(base, Name)
}

// earlierEntry returns one of the tool's own entries under the earlier name
// in base, or unknown when the place cannot be inspected.
func earlierEntry(base string) (found string, unknown bool) {
	if base == "" {
		return "", false
	}
	dir := filepath.Join(base, LegacyName)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false
	case err != nil:
		return "", true
	case !info.IsDir() && info.Mode()&os.ModeSymlink == 0:
		return "", false // a file of that name is not the tool's directory
	}
	for _, name := range Entries {
		_, err := os.Lstat(filepath.Join(dir, name))
		switch {
		case err == nil:
			return filepath.Join(dir, name), false
		case !errors.Is(err, os.ErrNotExist):
			return "", true
		}
	}
	return "", false
}

// DirName is NameIn for this user's configuration and state directories.
func DirName() string {
	config, _ := ConfigHome()
	state, _ := StateHome()
	return NameIn(config, state)
}

// Config returns the tool's directory under the user's configuration
// directory, joined with elem. It creates nothing.
func Config(elem ...string) (string, error) {
	base, err := ConfigHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{base, DirName()}, elem...)...), nil
}

// State returns the tool's directory under the user's state directory,
// joined with elem. It creates nothing.
func State(elem ...string) (string, error) {
	base, err := StateHome()
	if err != nil || base == "" {
		return "", errors.New("no user state directory")
	}
	return filepath.Join(append([]string{base, DirName()}, elem...)...), nil
}

// Found is what is on disk under the user's directories, for doctor.
type Found struct {
	Name    string   // the name in use: DirName
	Used    []string // the tool's entries under that name that exist
	Unread  []string // the registry and route list under the other name, which are not read while Name is in use
	Unknown []string // directories that could not be inspected, which is why the earlier name is in use
	Linked  []string // places where the two names are one directory, one being a link to the other
}

// unread are the entries that are read under the name in use only. Session
// claims and their records are honoured under both names (see session), a
// panel server is found under either, and loops/ is only where init puts a
// new loop.
var unread = []string{"registry", "routes.yaml"}

// Look reports the name in use and which of the tool's own entries exist
// under each name. It reads directory entries only and changes nothing.
func Look() Found {
	config, _ := ConfigHome()
	state, _ := StateHome()
	f := Found{Name: NameIn(config, state)}
	other := LegacyName
	if f.Name == LegacyName {
		other = Name
	}
	seen := map[string]bool{}
	for _, base := range []string{config, state} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		if _, unknown := earlierEntry(base); unknown {
			// Say which directory, not the place it is in: the place itself
			// when even that cannot be looked into.
			blocked := filepath.Join(base, LegacyName)
			if _, err := os.Lstat(blocked); err != nil {
				blocked = base
			}
			f.Unknown = append(f.Unknown, blocked)
		}
		for _, name := range Entries {
			if p := filepath.Join(base, f.Name, name); exists(p) {
				f.Used = append(f.Used, p)
			}
		}
		// The two names can be one directory, one a link to the other: then
		// nothing is under "the other name" that is not also under this one.
		if sameDirectory(filepath.Join(base, f.Name), filepath.Join(base, other)) {
			f.Linked = append(f.Linked, base)
			continue
		}
		for _, name := range unread {
			if p := filepath.Join(base, other, name); exists(p) {
				f.Unread = append(f.Unread, p)
			}
		}
	}
	return f
}

// sameDirectory reports whether two paths lead to one directory.
func sameDirectory(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil || !ia.IsDir() {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
