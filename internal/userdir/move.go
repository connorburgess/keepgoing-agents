package userdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Status is what one place holds, as far as a move is concerned.
type Status string

const (
	// Fresh: neither name is there. Nothing to move.
	Fresh Status = "fresh"
	// Earlier: a directory under the earlier name and nothing under the
	// current one. This is what Move moves.
	Earlier Status = "earlier"
	// Moved: a directory under the current name, and the earlier name is a
	// link to it. This is what Move leaves and what MoveBack moves back.
	Moved Status = "moved"
	// Current: a directory under the current name and nothing under the
	// earlier one: a computer that started with the current name.
	Current Status = "current"
	// Interrupted: a move in either direction stopped between its steps.
	// The files are under the current name; Move or MoveBack finishes it.
	Interrupted Status = "interrupted"
	// Both: directories under both names. Nothing is moved.
	Both Status = "both"
	// Blocked: something a move will not touch; Problem says what.
	Blocked Status = "blocked"
)

// markerName is a file a move keeps beside the directories while it works,
// so a move that stops between its steps can be told from a computer that
// started with the current name.
const markerName = LegacyName + ".moving"

// Place is one of the user's directories that hold the tool's own.
type Place struct {
	Base    string // the user's configuration or state directory
	Earlier string // Base/keepgoing
	Current string // Base/looptender
	Status  Status
	Problem string // why, when Status is Blocked
}

// Places returns this user's configuration and state directories, once each,
// as they are now. It changes nothing.
func Places() ([]Place, error) {
	config, _ := ConfigHome()
	state, _ := StateHome()
	var out []Place
	seen := map[string]bool{}
	for _, base := range []string{config, state} {
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		out = append(out, PlaceAt(base))
	}
	if len(out) == 0 {
		return nil, errors.New("the user configuration and state directories could not be determined")
	}
	return out, nil
}

// PlaceAt describes base as it is now. It changes nothing.
func PlaceAt(base string) Place {
	p := Place{Base: base, Earlier: filepath.Join(base, LegacyName), Current: filepath.Join(base, Name)}
	blocked := func(why string) Place {
		p.Status, p.Problem = Blocked, why
		return p
	}
	earlier, eerr := os.Lstat(p.Earlier)
	current, cerr := os.Lstat(p.Current)
	if (eerr != nil && !errors.Is(eerr, os.ErrNotExist)) || (cerr != nil && !errors.Is(cerr, os.ErrNotExist)) {
		return blocked("it cannot be inspected")
	}
	if cerr == nil && !current.IsDir() {
		return blocked(p.Current + " is not a directory")
	}
	switch {
	case eerr != nil && cerr != nil:
		p.Status = Fresh
	case eerr != nil:
		p.Status = Current
		if p.marked() {
			p.Status = Interrupted
		}
	case linksToCurrent(base):
		if cerr != nil {
			return blocked(p.Earlier + " is a link to " + p.Current + ", which does not exist")
		}
		p.Status = Moved
	case earlier.Mode()&os.ModeSymlink != 0:
		return blocked(p.Earlier + " is a link to somewhere else; move what it points to yourself")
	case !earlier.IsDir():
		return blocked(p.Earlier + " is not a directory")
	case cerr == nil:
		p.Status = Both
	default:
		p.Status = Earlier
	}
	return p
}

func (p Place) marker() string { return filepath.Join(p.Base, markerName) }

// marked reports whether a move's marker is there: an empty regular file.
func (p Place) marked() bool {
	info, err := os.Lstat(p.marker())
	return err == nil && info.Mode().IsRegular()
}

func (p Place) mark() error {
	f, err := os.OpenFile(p.marker(), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (p Place) unmark() {
	if p.marked() {
		_ = os.Remove(p.marker())
	}
}

// betweenSteps, when set by a test, runs after Move's rename and before its
// link, and after MoveBack's removal of the link and before its rename: the
// moments in which the earlier name is absent.
var betweenSteps func()

// ErrNoLink means this user cannot make a symbolic link here. On Windows
// that needs Developer Mode or an elevated prompt.
var ErrNoLink = errors.New("a symbolic link cannot be made here")

// CanLink reports whether a link can be left in this place, by making one
// and removing it. Move asks before it moves anything.
func (p Place) CanLink() error {
	probe := filepath.Join(p.Base, LegacyName+".linktest")
	_ = os.Remove(probe)
	if err := os.Symlink(".", probe); err != nil {
		return ErrNoLink
	}
	return os.Remove(probe)
}

// Move renames the directory under the earlier name to the current name and
// leaves the earlier name as a link to it, so a build from before the
// rename, and every path recorded with the earlier name, still reaches the
// files. It moves only a place that is Earlier, and finishes one that is
// Interrupted; a place that is Fresh, Current or Moved needs nothing. The
// caller makes sure nothing is using the directory.
//
// The steps are: a marker file, the rename, the link, the marker removed.
// Stopped after any of them, the place is still usable (the files are under
// one name or the other and the tool finds them), and Move finishes the
// rest when it is run again.
func (p Place) Move() error {
	switch p.Status {
	case Fresh, Current, Moved:
		p.unmark()
		return nil
	case Earlier:
		if err := p.CanLink(); err != nil {
			return err
		}
		if err := p.mark(); err != nil {
			return fmt.Errorf("could not write beside %s; nothing was moved", p.Earlier)
		}
		if err := os.Rename(p.Earlier, p.Current); err != nil {
			p.unmark()
			return fmt.Errorf("%s could not be renamed (something may have a file in it open); nothing was moved", p.Earlier)
		}
	case Interrupted:
	default:
		return fmt.Errorf("%s is not moved: %s", p.Earlier, p.describe())
	}
	if betweenSteps != nil {
		betweenSteps()
	}
	if err := os.Symlink(Name, p.Earlier); err != nil {
		if _, lerr := os.Lstat(p.Earlier); lerr == nil {
			// Another command made the earlier directory again in the
			// moment it was gone. The marker stays, so the next run says so.
			return fmt.Errorf("%s was created again by another command during the move, so no link could be left there. Your files are in %s. Remove %s if it holds nothing you need, then run the move again", p.Earlier, p.Current, p.Earlier)
		}
		// No link can be made after all: put the directory back.
		if rerr := os.Rename(p.Current, p.Earlier); rerr != nil {
			return fmt.Errorf("the files are now in %s, but no link could be left at %s, and they could not be put back", p.Current, p.Earlier)
		}
		p.unmark()
		return ErrNoLink
	}
	p.unmark()
	return nil
}

// InMoveOrder returns the places in the order a move goes through them:
// the configuration directory, which holds the registry and the route list,
// first; and the reverse for a move back. Stopped between two places, or
// between the steps of one, either way, the registry and the route list are
// then always under the name in use (see NameIn), and what is in the other
// place, the session claims and the panel's files, is found under either
// name. Places lists the configuration directory first.
func InMoveOrder(places []Place, back bool) []Place {
	out := append([]Place(nil), places...)
	if back {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// MoveBack undoes Move: it removes the link and renames the directory back
// to the earlier name. It moves only a place that is Moved, and finishes one
// that is Interrupted; a computer that started with the current name
// (Current) was never moved, and is left as it is.
func (p Place) MoveBack() error {
	switch p.Status {
	case Fresh, Current, Earlier:
		p.unmark()
		return nil
	case Moved:
		if err := p.mark(); err != nil {
			return fmt.Errorf("could not write beside %s; nothing was moved", p.Current)
		}
		if err := os.Remove(p.Earlier); err != nil {
			p.unmark()
			return fmt.Errorf("the link %s could not be removed; nothing was moved", p.Earlier)
		}
	case Interrupted:
	default:
		return fmt.Errorf("%s is not moved back: %s", p.Current, p.describe())
	}
	if betweenSteps != nil {
		betweenSteps()
	}
	if err := os.Rename(p.Current, p.Earlier); err != nil {
		if p.Status == Moved {
			// Put the link back: the place is as it was.
			if os.Symlink(Name, p.Earlier) == nil {
				p.unmark()
			}
		}
		return fmt.Errorf("%s could not be renamed (something may have a file in it open); the files are still there", p.Current)
	}
	p.unmark()
	return nil
}

func (p Place) describe() string {
	switch p.Status {
	case Both:
		return "directories of both names exist (" + p.Earlier + " and " + p.Current + ")"
	case Blocked:
		return p.Problem
	}
	return string(p.Status)
}

// Describe says what the place holds, for a preview.
func (p Place) Describe() string { return p.describe() }
