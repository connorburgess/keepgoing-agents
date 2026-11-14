// Package session keeps two loops from acting on one agent session at the
// same moment, and lets a loop find the other loops that supervise its
// session.
//
// A session is one conversation: a BB thread, or the working directory of a
// headless agent, or the resume command of a generic process. Its key is a
// hash of the adapter kind and that identity, so no thread ID or path is ever
// in a file name.
//
// Two things live in one private directory under the user's state
// directory, shared by every loop the user runs, registered or not:
//
//   - <key>.lock, the claim: an operating-system lock a check holds from
//     before it first observes the session until it has finished acting. A
//     second loop meeting it sends nothing. The system releases it when its
//     holder exits, however it exits, so it cannot be left behind.
//   - <key>.json, the supervisors record: the loops that enabled or checked
//     this session, so `enable` can name one it would be joining.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// DirEnv names a directory to use instead of the default. Loops given
// different directories do not see each other's claims. The variable's
// earlier name, KEEPGOING_SESSION_DIR, is still read; see envvar.
const DirEnv = envvar.Prefix + string(envvar.SessionDir)

// StateHome is the user's state directory, where keepgoing keeps runtime
// files that belong to no single loop; see userdir.StateHome.
func StateHome() (string, error) { return userdir.StateHome() }

// UndeterminedError: there is no user state directory to keep claims in
// (no HOME and no XDG_STATE_HOME). Checks go ahead unprotected.
type UndeterminedError struct{ Reason string }

func (e *UndeterminedError) Error() string { return e.Reason }

// Fix says what a person can do.
func (e *UndeterminedError) Fix() string {
	return "set HOME, or " + DirEnv + " to a private directory"
}

// UnsafeError: the claim directory, or a file in it, is in a state that
// cannot be repaired safely: owned by another user, not a directory, a
// symbolic link, or a directory planted where a claim file belongs. A check
// acts on nothing until it is fixed; a requested stop still goes ahead.
type UnsafeError struct {
	Path    string
	Problem string
	fix     string // what to do, when removing Path is not it
}

func (e *UnsafeError) Error() string { return e.Path + " " + e.Problem }

// Fix says what a person can do.
func (e *UnsafeError) Fix() string {
	if e.fix != "" {
		return e.fix
	}
	return "remove " + e.Path + " if it is not keepgoing's, or set " + DirEnv + " to a private directory of your own"
}

// Dir returns the private directory for claims and records, creating it.
// A directory of this user's that is merely too open, or not writable, is
// made 0700 when repair is set, and tightened is then true; status and doctor
// look without repairing. Anything that cannot be repaired safely is an
// *UnsafeError, and no user state directory at all an *UndeterminedError.
//
// The variable's two names set to different directories are an *UnsafeError
// too: honouring either would hide the claims kept in the other.
func Dir(repair ...bool) (dir string, tightened bool, err error) {
	dir, _, err = envvar.SessionDir.Path()
	if err != nil {
		v := envvar.SessionDir
		return "", false, &UnsafeError{Path: "the session claim directory", Problem: "is named twice: " + v.Name() + " and " + v.Legacy() + " are set to different directories",
			fix: "unset one of them, or set both to the same directory"}
	}
	if dir == "" {
		// Under the state directory, in looptender/, or in keepgoing/ on a
		// computer that already has one: see userdir.
		dir, err = userdir.State("sessions")
		if err != nil {
			return "", false, &UndeterminedError{Reason: "no user state directory: HOME and XDG_STATE_HOME are not set"}
		}
	}
	if !filepath.IsAbs(dir) {
		return "", false, &UnsafeError{Path: dir, Problem: "is not an absolute path"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if info, lerr := os.Lstat(dir); lerr == nil && !info.IsDir() {
			return "", false, &UnsafeError{Path: dir, Problem: "is not a directory"}
		}
		return "", false, &UnsafeError{Path: dir, Problem: "cannot be created"}
	}
	info, err := os.Lstat(dir)
	switch {
	case err != nil:
		return "", false, &UnsafeError{Path: dir, Problem: "cannot be read"}
	case info.Mode()&os.ModeSymlink != 0:
		return "", false, &UnsafeError{Path: dir, Problem: "is a symbolic link"}
	case !info.IsDir():
		return "", false, &UnsafeError{Path: dir, Problem: "is not a directory"}
	case !ownedByMe(info):
		return "", false, &UnsafeError{Path: dir, Problem: "belongs to another user"}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		if len(repair) == 0 || !repair[0] {
			return dir, false, nil // status and doctor say so; the next check repairs it
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", false, &UnsafeError{Path: dir, Problem: "is not private and cannot be made private"}
		}
		return dir, true, nil
	}
	return dir, false, nil
}

// Private reports whether dir is exactly 0700, so status can say a check
// will tighten it.
func Private(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && (runtime.GOOS == "windows" || info.Mode().Perm() == 0o700)
}

// Identity is one session. ID is the raw identity: never written to a file
// name, and only to files in the private directory.
type Identity struct {
	Kind string // bb, workdir or process
	ID   string
}

// Of returns the session an agent configuration acts on. It reports false
// for an agent with no fixed session, such as a fallback that will create a
// new thread and has not yet.
func Of(a config.Agent) (Identity, bool) {
	switch a.Adapter {
	case "bb":
		if a.BB == nil || a.BB.Thread == "" {
			return Identity{}, false
		}
		return Identity{Kind: "bb", ID: a.BB.Thread}, true
	case "process":
		if a.Process == nil || len(a.Process.ResumeCommand) == 0 {
			return Identity{}, false
		}
		b, _ := json.Marshal(a.Process.ResumeCommand)
		return Identity{Kind: "process", ID: string(b)}, true
	}
	if dir := Workdir(a); dir != "" {
		return Identity{Kind: a.Adapter, ID: dir}, true
	}
	return Identity{}, false
}

// Workdir returns the working directory of a headless agent, absolute and
// with symbolic links resolved, or "" for an agent that has none.
func Workdir(a config.Agent) string {
	var dir string
	switch {
	case a.Claude != nil:
		dir = a.Claude.Workdir
	case a.Codex != nil:
		dir = a.Codex.Workdir
	case a.Cursor != nil:
		dir = a.Cursor.Workdir
	case a.Custom != nil:
		dir = a.Custom.Workdir
	}
	return CleanPath(dir)
}

// CleanPath makes a path absolute and resolves symbolic links where it can,
// so two spellings of one directory compare equal.
func CleanPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		// On a case-insensitive file system two spellings of one directory
		// resolve to the same files: use the file system's own name for it.
		return canonical(real)
	}
	return filepath.Clean(abs)
}

// Key is the file name stem for a session: a hash, never the identity.
func (i Identity) Key() string {
	sum := sha256.Sum256([]byte("keepgoing-session\x00" + i.Kind + "\x00" + i.ID))
	return hex.EncodeToString(sum[:16])
}

// Describe names the session for a person.
func (i Identity) Describe() string {
	switch i.Kind {
	case "bb":
		return "BB thread " + i.ID
	case "process":
		return "the process session this loop's resume command continues"
	}
	return i.Kind + " session in " + i.ID
}

// Holder is who holds a claim, written beside it for the message a second
// loop prints. It holds no secret.
type Holder struct {
	Loop  string    `json:"loop"`
	PID   int       `json:"pid"`
	Since time.Time `json:"since"`
	// Until is the end of the holder's own hard time limit. A claim held
	// past it belongs to a stuck process.
	Until time.Time `json:"until"`
}

// ClaimedError says another loop holds the session's claim. It matches
// lock.ErrHeld, so callers that skip a busy check skip this too.
type ClaimedError struct {
	Session Identity
	Holder  *Holder // nil when the holder had not written its details yet
	Now     time.Time
}

func (e *ClaimedError) Is(target error) bool { return target == lock.ErrHeld }

// Stuck reports whether the holder is past its own time limit.
func (e *ClaimedError) Stuck() bool {
	return e.Holder != nil && !e.Holder.Until.IsZero() && e.Now.After(e.Holder.Until)
}

func (e *ClaimedError) Error() string {
	what := e.Session.Describe()
	if e.Holder == nil {
		return what + " is being checked by another loop; skipping, nothing sent"
	}
	who := fmt.Sprintf("loop %s (pid %d)", e.Holder.Loop, e.Holder.PID)
	if e.Stuck() {
		return fmt.Sprintf("%s is still claimed by %s, which passed its own time limit at %s and is stuck; skipping, nothing sent. Find process %d and stop it; keepgoing does not break the claim", what, who, e.Holder.Until.Local().Format("15:04"), e.Holder.PID)
	}
	return fmt.Sprintf("%s is being checked by %s since %s; skipping, nothing sent", what, who, e.Holder.Since.Local().Format("15:04:05"))
}

// Claim is a held session claim.
type Claim struct {
	lk     *lock.Lock
	holder string
}

// Take claims a session without waiting. It returns a *ClaimedError when
// another process holds it.
func Take(dir string, id Identity, me Holder) (*Claim, error) {
	key := id.Key()
	// The claim file must be a regular file of this user's: anything else
	// at that name was put there by someone else.
	lockPath := filepath.Join(dir, key+".lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || !ownedByMe(info)) {
		return nil, &UnsafeError{Path: lockPath, Problem: "is not a claim file of this user's"}
	}
	claimed := func(in string) *ClaimedError {
		e := &ClaimedError{Session: id, Now: time.Now()}
		var h Holder
		if readJSON(filepath.Join(in, key+".holder"), 4096, &h) == nil && h.PID > 0 {
			e.Holder = &h
		}
		return e
	}
	lk, err := lock.AcquireNamed(dir, key)
	if errors.Is(err, lock.ErrHeld) {
		return nil, claimed(dir)
	}
	if err != nil {
		return nil, &UnsafeError{Path: lockPath, Problem: "cannot be opened as a claim file"}
	}
	// The default claim directory has two possible names (see userdir), and
	// two checks can for a moment disagree on which is in use. So a claim is
	// good only if the same session is not claimed under the other name
	// either. Each check takes its own claim first and looks at the other
	// directory second, so of two that overlap at least one sees the other
	// and gives way. A directory that cannot be looked at is not assumed
	// free.
	if other := Sibling(dir); other != "" {
		held, err := lock.Held(other, key)
		if held || err != nil {
			_ = lk.Release()
			if held {
				return nil, claimed(other)
			}
			return nil, &UnsafeError{Path: filepath.Join(other, key+".lock"), Problem: "cannot be checked for a claim"}
		}
	}
	c := &Claim{lk: lk, holder: filepath.Join(dir, key+".holder")}
	_ = writeJSON(dir, c.holder, me)
	return c, nil
}

// Release gives the claim up. The holder details go first, so a later
// holder's details are never removed.
func (c *Claim) Release() {
	if c == nil || c.lk == nil {
		return
	}
	_ = os.Remove(c.holder)
	_ = c.lk.Release()
	c.lk = nil
}

// Supervisor is one loop in a session's record.
type Supervisor struct {
	Loop     string    `json:"loop"`
	Config   string    `json:"config"`
	StateDir string    `json:"stateDir"`
	SeenAt   time.Time `json:"seenAt"`
}

const (
	maxSupervisors = 16
	maxRecordBytes = 64 << 10
)

// Note adds or refreshes a loop in the session's record. It is best effort:
// the claim, not the record, is what prevents a double send. It waits at
// most wait for another writer of the same record.
func Note(dir string, id Identity, s Supervisor, wait time.Duration) error {
	s.Config, s.StateDir = CleanPath(s.Config), CleanPath(s.StateDir)
	if s.StateDir == "" || s.Config == "" {
		return errors.New("a supervisor needs its configuration and state paths")
	}
	key := id.Key()
	var lk *lock.Lock
	for deadline := time.Now().Add(wait); ; time.Sleep(20 * time.Millisecond) {
		var err error
		if lk, err = lock.AcquireNamed(dir, key+"-record"); err == nil {
			break
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) {
			return err
		}
	}
	defer lk.Release()
	list := known(dir, id)
	out := []Supervisor{s}
	for _, e := range list {
		if e.StateDir != s.StateDir {
			out = append(out, e)
		}
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return out[1+i].SeenAt.After(out[1+j].SeenAt) })
	if len(out) > maxSupervisors {
		out = out[:maxSupervisors]
	}
	return writeJSON(dir, filepath.Join(dir, key+".json"), record{SchemaVersion: 1, Kind: id.Kind, ID: id.ID, Supervisors: out})
}

type record struct {
	SchemaVersion int          `json:"schemaVersion"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	Supervisors   []Supervisor `json:"supervisors"`
}

// Sibling returns the claim directory under the tool's other name, when dir
// is the default claim directory under one of its two names and the other
// exists as a different directory. It is "" for a directory a person chose
// (see DirEnv), which has no other name, and "" when the two names are one
// directory, as when one name is a link to the other: there is then one set
// of claims, and looking for a claim "under the other name" would find this
// check's own. Claims and records are honoured in both.
func Sibling(dir string) string {
	base, err := userdir.StateHome()
	if err != nil || base == "" || dir == "" {
		return ""
	}
	for _, names := range [][2]string{{userdir.Name, userdir.LegacyName}, {userdir.LegacyName, userdir.Name}} {
		mine := filepath.Join(base, names[0], "sessions")
		if filepath.Clean(dir) != mine && CleanPath(dir) != CleanPath(mine) {
			continue
		}
		other := filepath.Join(base, names[1], "sessions")
		theirs, err := os.Stat(other) // through a link: what the name leads to
		if err != nil || !theirs.IsDir() {
			return ""
		}
		if ours, err := os.Stat(mine); err == nil && os.SameFile(ours, theirs) {
			return ""
		}
		return other
	}
	return ""
}

// both is dir and, after it, its Sibling when there is one.
func both(dir string) []string {
	if other := Sibling(dir); other != "" {
		return []string{dir, other}
	}
	return []string{dir}
}

// Known returns the loops in a session's record, and in its record under
// the tool's other directory name when there is one (see Sibling). A record
// that cannot be read, or that names another session, gives none.
func Known(dir string, id Identity) []Supervisor {
	var out []Supervisor
	seen := map[string]bool{}
	for _, d := range both(dir) {
		for _, s := range known(d, id) {
			if !seen[s.StateDir] && len(out) < maxSupervisors {
				seen[s.StateDir] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// known is Known for one directory.
func known(dir string, id Identity) []Supervisor {
	var r record
	if readJSON(filepath.Join(dir, id.Key()+".json"), maxRecordBytes, &r) != nil || r.SchemaVersion != 1 || r.Kind != id.Kind || r.ID != id.ID {
		return nil
	}
	if len(r.Supervisors) > maxSupervisors {
		r.Supervisors = r.Supervisors[:maxSupervisors]
	}
	return r.Supervisors
}

// Recorded is Known for a caller that must not take an unreadable record
// for an empty one: it returns an error when a record exists but cannot be
// read, is too large, has another schema or names another session. No
// record at all is not an error: no loop has recorded the session.
func Recorded(dir string, id Identity) ([]Supervisor, error) {
	var out []Supervisor
	seen := map[string]bool{}
	for _, d := range both(dir) {
		list, err := recorded(d, id)
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			if !seen[s.StateDir] {
				seen[s.StateDir] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// recorded is Recorded for one directory.
func recorded(dir string, id Identity) ([]Supervisor, error) {
	path := filepath.Join(dir, id.Key()+".json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var r record
	if err := readJSON(path, maxRecordBytes, &r); err != nil {
		return nil, fmt.Errorf("the session record of %s cannot be read", id.Describe())
	}
	if r.SchemaVersion != 1 || r.Kind != id.Kind || r.ID != id.ID || len(r.Supervisors) > maxSupervisors {
		return nil, fmt.Errorf("the session record of %s is not one this build reads", id.Describe())
	}
	return r.Supervisors, nil
}

// All returns every loop in every record in the directory, each once, at
// most limit of them. It is how a loop finds others that share a working
// directory or a repository rather than a session.
func All(dir string, limit int) []Supervisor {
	seen := map[string]bool{}
	var out []Supervisor
	for _, d := range both(dir) {
		out = all(d, limit, seen, out)
	}
	return out
}

// all is All for one directory, adding to out the loops not yet seen.
func all(dir string, limit int, seen map[string]bool, out []Supervisor) []Supervisor {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || len(name) != 32+len(".json") {
			continue
		}
		var r record
		if readJSON(filepath.Join(dir, name), maxRecordBytes, &r) != nil || r.SchemaVersion != 1 || (Identity{Kind: r.Kind, ID: r.ID}).Key()+".json" != name {
			continue
		}
		for _, s := range r.Supervisors {
			if !seen[s.StateDir] && len(out) < limit {
				seen[s.StateDir] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func readJSON(path string, limit int64, v any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("not a readable record")
	}
	f, err := snapshot.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return errors.New("not a readable record")
	}
	return json.Unmarshal(b, v)
}

func writeJSON(dir, path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Chmod(0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return snapshot.Replace(f.Name(), path)
}

// Peek returns the details of whoever holds a session's claim now, without
// taking it, or nil. Taking it to find out could make a check that starts at
// that instant skip, so status never does.
func Peek(dir string, id Identity) *Holder {
	for _, d := range both(dir) {
		if h := peek(d, id); h != nil {
			return h
		}
	}
	return nil
}

// peek is Peek for one directory.
func peek(dir string, id Identity) *Holder {
	var h Holder
	if readJSON(filepath.Join(dir, id.Key()+".holder"), 4096, &h) != nil || h.PID <= 0 || !alive(h.PID) {
		return nil
	}
	// A holder file left by a process that died, whose pid has since been
	// reused, is not believed: the claim itself must be held. A shared,
	// non-blocking probe answers that without holding anything.
	if !heldByOther(filepath.Join(dir, id.Key()+".lock")) {
		return nil
	}
	return &h
}
