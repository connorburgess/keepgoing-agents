package userdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	Problem string // why, when Status is Blocked or Both
	stray   bool   // the current name is a link to itself, left by a swap that stopped
}

// Stopped reports whether a move in either direction stopped here partway:
// the marker is there, the move's own leftover link is, or the move is
// between its steps.
func (p Place) Stopped() bool { return p.marked() || p.stray || p.Status == Interrupted }

// isSelfLink reports whether path is a symbolic link whose target is its own
// name, as a swap makes and leaves for a moment.
func isSelfLink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(path)
	return err == nil && target == filepath.Base(path)
}

// errNoExchange means the operating system or file system cannot swap two
// names in one step; a move then renames and links in two.
var errNoExchange = errors.New("no atomic exchange here")

// swap is exchange, replaced by tests.
var swap = exchange

// clearStray removes the leftover self-link, if there is one.
func (p Place) clearStray() {
	if isSelfLink(p.Current) {
		_ = os.Remove(p.Current)
	}
}

// Finish clears what a finished move can leave behind: its marker and a
// leftover link. It changes nothing in a place a move has not finished.
func (p Place) Finish() bool {
	switch p.Status {
	case Fresh, Current, Moved, Earlier:
	default:
		return false
	}
	cleared := p.marked() || p.stray
	p.clearStray()
	p.unmark()
	return cleared
}

// Places returns this user's configuration and state directories, once each,
// as they are now. It changes nothing.
func Places() ([]Place, error) {
	config, _ := ConfigHome()
	state, _ := StateHome()
	out := placesAt(config, state)
	if len(out) == 0 {
		return nil, errors.New("the user configuration and state directories could not be determined")
	}
	return out, nil
}

// Rewrite returns path with this place's directory of one name replaced by
// the other: from the earlier name to the current one, or back. The
// directory is recognised by its name and by its parent being this place,
// whatever spelling the parent has (through a link, /var against
// /private/var, a dotfiles directory). A path elsewhere is returned as it is.
func (p Place) Rewrite(path string, back bool) string {
	from, to := LegacyName, Name
	if back {
		from, to = Name, LegacyName
	}
	if !filepath.IsAbs(path) {
		return path
	}
	base, err := os.Stat(p.Base)
	if err != nil {
		return path
	}
	clean := filepath.Clean(path)
	for a := clean; ; a = filepath.Dir(a) {
		parent := filepath.Dir(a)
		if parent == a {
			return path
		}
		if filepath.Base(a) != from {
			continue
		}
		if info, err := os.Stat(parent); err == nil && os.SameFile(info, base) {
			rel, err := filepath.Rel(a, clean)
			if err != nil {
				return path
			}
			return filepath.Join(parent, to, rel)
		}
	}
}

// Under reports whether path is inside this place's directory of the given
// name, by the same test as Rewrite.
func (p Place) Under(path, name string) bool {
	back := name == Name
	if name != Name && name != LegacyName {
		return false
	}
	return p.Rewrite(path, back) != path
}

// placesAt describes each base once: two spellings of one directory, as
// through a link, are one place.
func placesAt(bases ...string) []Place {
	var out []Place
	var seen []os.FileInfo
	for _, base := range bases {
		if base == "" {
			continue
		}
		info, err := os.Stat(base)
		dup := false
		for _, s := range seen {
			dup = dup || (err == nil && os.SameFile(s, info))
		}
		if dup {
			continue
		}
		if err == nil {
			seen = append(seen, info)
		}
		out = append(out, PlaceAt(base))
	}
	return out
}

// renameProblem names why a rename failed, from the operating system's own
// error: another file system or a mount point, permission, a file in use.
func renameProblem(err error) string {
	var le *os.LinkError
	if errors.As(err, &le) {
		err = le.Err
	}
	switch {
	case errors.Is(err, syscall.EXDEV):
		return "it is on another file system or is a mount point"
	case errors.Is(err, syscall.EBUSY):
		return "it is a mount point, or the system is using it"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	case isSharingViolation(err):
		return "a file in it is open in another program"
	}
	return "the system said: " + err.Error()
}

// PlaceAt describes base as it is now. It changes nothing.
func PlaceAt(base string) Place {
	p := Place{Base: base, Earlier: filepath.Join(base, LegacyName), Current: filepath.Join(base, Name)}
	blocked := func(why string) Place {
		p.Status, p.Problem = Blocked, why
		return p
	}
	// A swap (see Move) first makes the current name a link to itself, and
	// a move back leaves one for a moment after its swap. Stopped there, the
	// link is all that is under the current name: it is left over, not a
	// directory of that name, and the move or the move back removes it.
	p.stray = isSelfLink(p.Current)
	earlier, eerr := os.Lstat(p.Earlier)
	current, cerr := os.Lstat(p.Current)
	if p.stray {
		current, cerr = nil, os.ErrNotExist
	}
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
		if p.marked() {
			p.Problem = "a move stopped between its steps, and " + p.Earlier + " was created again since. The files are in " + p.Current + "; " + p.Earlier + " holds only what was made there since. To finish: stop every loop's checks and the panel, move what is in " + p.Earlier + " into " + p.Current + ", remove the empty " + p.Earlier + ", then run keepgoing migrate --apply"
		}
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
		p.Finish()
		return nil
	case Earlier:
		p.clearStray()
		if err := p.CanLink(); err != nil {
			return err
		}
		if err := p.mark(); err != nil {
			return fmt.Errorf("could not write beside %s; nothing was moved", p.Earlier)
		}
		// Where the system can swap two names in one step, the current name
		// is first made a link to itself, then swapped with the directory:
		// the directory arrives under the current name and the earlier name
		// becomes the link in the same instant. Nothing ever finds either
		// name missing.
		if err := os.Symlink(Name, p.Current); err == nil {
			if err := swap(p.Earlier, p.Current); err == nil {
				p.unmark()
				return nil
			}
			_ = os.Remove(p.Current)
		}
		if err := os.Rename(p.Earlier, p.Current); err != nil {
			p.unmark()
			return fmt.Errorf("%s could not be renamed (%s); nothing was moved", p.Earlier, renameProblem(err))
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
		p.Finish()
		return nil
	case Moved:
		if err := p.mark(); err != nil {
			return fmt.Errorf("could not write beside %s; nothing was moved", p.Current)
		}
		// Swapped in one step where the system can: the directory is back
		// under the earlier name, and the link, now under the current name,
		// points to itself and is removed.
		if err := swap(p.Earlier, p.Current); err == nil {
			if isSelfLink(p.Current) {
				_ = os.Remove(p.Current)
			}
			p.unmark()
			return nil
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
		return fmt.Errorf("%s could not be renamed (%s); the files are still there", p.Current, renameProblem(err))
	}
	p.unmark()
	return nil
}

func (p Place) describe() string {
	switch p.Status {
	case Both:
		if p.Problem != "" {
			return p.Problem
		}
		return "directories of both names exist (" + p.Earlier + " and " + p.Current + ")"
	case Blocked:
		return p.Problem
	}
	return string(p.Status)
}

// Describe says what the place holds, for a preview.
func (p Place) Describe() string { return p.describe() }
