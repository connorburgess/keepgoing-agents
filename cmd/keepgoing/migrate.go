package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// migrate moves this user's per-user directories from the tool's earlier
// name, keepgoing, to its current one, looptender, and back. Nothing else
// ever moves them: without this command a computer that has keepgoing
// directories keeps using them (see userdir).
//
// It only says what it would do unless --apply is given. It refuses while
// anything is using the directories: a check, a daemon, a panel or a
// registry edit holds a lock, and it looks for every one of them. It leaves
// a link under the earlier name, so a build from before the rename and every
// path recorded with that name still reach the files, and --undo puts the
// directories back.

type migratePlace struct {
	Base   string `json:"base"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Status string `json:"status"`
	Action string `json:"action"` // move, finish, none
	Done   bool   `json:"done,omitempty"`
}

type migrateRefusal struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Loop    string `json:"loop,omitempty"`
	Message string `json:"message"`
}

type migrateOutput struct {
	SchemaVersion int              `json:"schemaVersion"`
	Operation     string           `json:"operation"` // move, undo
	DryRun        bool             `json:"dryRun"`
	Outcome       string           `json:"outcome"` // nothing_to_do, ready, refused, done, failed
	Places        []migratePlace   `json:"places"`
	Refusals      []migrateRefusal `json:"refusals"`
	References    int              `json:"registryReferencesRewritten"`
	Message       string           `json:"message,omitempty"`
}

// migrateWalkLimit bounds how many entries of a directory are looked at
// before moving it, and migrateHoldLimit how many locks are held through it.
const (
	migrateWalkLimit = 200000
	migrateHoldLimit = 4096
)

// migrateLockWait bounds how long one lock file may take to open and be
// tried. On some file systems (FAT, exFAT, some network volumes) opening a
// lock file can wait without end; a lock that cannot be tried in time is
// treated as in use, and named.
var migrateLockWait = 3 * time.Second

// migrateLockNames are the tool's own lock files, by exact name. Session
// claims and their records are named by a hash and matched by pattern.
// Every lock file the tool keeps under the per-user directories or in a
// loop's state directory must be listed here: one that is not is neither
// looked at nor held while those directories move.
var migrateLockNames = func() map[string]bool {
	names := map[string]bool{}
	for _, n := range []string{lock.Tick, lock.Daemon, lock.Control, lock.Service, "turn", panelOpenLock, panelServerLock, "routes", registry.LockName, "incident", "delivery", "graph", clientsLock} {
		names[n+".lock"] = true
	}
	return names
}()

// The lock calls, replaced by tests.
var migrateHoldFile, migrateHeldFile = lock.HoldFile, lock.HeldFile

var migrateClaimLock = regexp.MustCompile(`^[0-9a-f]{32}(-record)?\.lock$`)

// migrateIsLock reports whether a file name is one of the tool's lock
// files. A name beginning "._" is never one: macOS keeps a file's extended
// attributes in such a file on file systems that cannot hold them.
func migrateIsLock(name string) bool {
	if strings.HasPrefix(name, "._") {
		return false
	}
	return migrateLockNames[name] || migrateClaimLock.MatchString(name)
}

// migrateHoldLocks says whether the locks found free are held through the
// move. On Windows an open file keeps its directory from being renamed, so
// they are only looked at there.
var migrateHoldLocks = runtime.GOOS != "windows"

// Moments in a run, for tests: after the directories are looked at and
// before anything moves, and after the moves and before the registry is
// rewritten.
var migrateBeforeMoves, migrateBeforeRewrite func()

func migrateCommand(e env, args []string) int {
	fl := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fl.SetOutput(e.stderr)
	apply := fl.Bool("apply", false, "move the directories; without it, only say what would be done")
	undo := fl.Bool("undo", false, "move the directories back to the earlier name (with --apply)")
	asJSON := fl.Bool("json", false, "print versioned JSON")
	if fl.Parse(args) != nil {
		return exitUsage
	}
	if fl.NArg() != 0 {
		e.errf("usage: keepgoing migrate [--undo] [--apply] [--json]")
		return exitUsage
	}
	out := migrateOutput{SchemaVersion: 1, Operation: "move", DryRun: !*apply, Places: []migratePlace{}, Refusals: []migrateRefusal{}}
	if *undo {
		out.Operation = "undo"
	}
	finish := func(code int) int {
		if *asJSON {
			if json.NewEncoder(e.stdout).Encode(out) != nil {
				return exitFailed
			}
			return code
		}
		migratePrint(e, out)
		return code
	}
	places, err := userdir.Places()
	if err != nil {
		out.Outcome, out.Message = "failed", err.Error()
		return finish(exitFailed)
	}
	held := &migrateHeld{hold: *apply && migrateHoldLocks}
	defer held.release()

	// What each place needs, and what in it must not be in use. A place
	// already in the state this run leaves it in is "done": its registry
	// references are still rewritten, so a run that stopped before the
	// rewrite is finished by running it again.
	var todo, done []userdir.Place
	var walked []string
	for _, p := range places {
		mp := migratePlace{Base: p.Base, Status: string(p.Status), Action: "none"}
		from, to, moves, finished := p.Earlier, p.Current, userdir.Earlier, userdir.Moved
		if *undo {
			from, to, moves, finished = p.Current, p.Earlier, userdir.Moved, userdir.Earlier
		}
		switch p.Status {
		case moves:
			mp.Action, mp.From, mp.To = "move", from, to
		case userdir.Interrupted:
			// The files are under the current name whichever way it was going.
			mp.Action, mp.From, mp.To = "finish", p.Current, to
		case finished:
			done = append(done, p)
		case userdir.Both, userdir.Blocked:
			out.Refusals = append(out.Refusals, migrateRefusal{Code: string(p.Status), Path: p.Base, Message: p.Describe()})
		}
		if mp.Action != "none" {
			todo = append(todo, p)
			holds := p.Earlier
			if p.Status != userdir.Earlier {
				holds = p.Current
			}
			walked = append(walked, holds)
			out.Refusals = append(out.Refusals, held.inUse(holds)...)
			// A link is needed to move forward; whether one can be made is
			// known before anything moves, in a preview too.
			if !*undo && p.Status == userdir.Earlier {
				if err := p.CanLink(); err != nil {
					out.Refusals = append(out.Refusals, migrateRefusal{Code: "no_link", Path: p.Base, Message: "a link cannot be made here, and without one a build from before the rename and paths recorded with the earlier name would not find the files. On Windows, making a link needs Developer Mode or an elevated prompt"})
				}
			}
		}
		out.Places = append(out.Places, mp)
	}
	if len(todo) > 0 {
		out.Refusals = append(out.Refusals, held.loopsInUse(places, walked)...)
		if *undo {
			out.Refusals = append(out.Refusals, migrateNamesCurrent(places, todo)...)
		}
	}
	if len(out.Refusals) > 0 {
		out.Outcome = "refused"
		return finish(exitFailed)
	}
	// References to rewrite in places already done.
	pending, err := migrateRebase(places, done, *undo, true)
	if err != nil {
		out.Outcome, out.Message = "failed", "The loop registry could not be read: "+err.Error()+"."
		return finish(exitFailed)
	}
	switch {
	case len(todo) == 0 && pending == 0:
		if *apply {
			for _, p := range places {
				p.Finish() // a marker or link a stopped run left behind
			}
		}
		out.Outcome = "nothing_to_do"
		return finish(exitOK)
	case !*apply:
		out.Outcome, out.References = "ready", pending
		return finish(exitOK)
	}

	if migrateBeforeMoves != nil {
		migrateBeforeMoves()
	}
	var moved []userdir.Place
	var failed error
	var failedAt userdir.Place
	for _, p := range userdir.InMoveOrder(todo, *undo) {
		move := p.Move
		if *undo {
			move = p.MoveBack
		}
		if err := move(); err != nil {
			failed, failedAt = err, p
			break
		}
		moved = append(moved, p)
		for i := range out.Places {
			if out.Places[i].Base == p.Base {
				out.Places[i].Done = true
			}
		}
	}
	// Checks may run again from here; the registry is rewritten under its
	// own lock.
	held.release()
	if migrateBeforeRewrite != nil {
		migrateBeforeRewrite()
	}
	// The registry records full paths. References to loops kept inside a
	// moved directory are rewritten to its new name, so they still match
	// what a command resolves the loop's files to. Places done before this
	// run are included, so a rewrite that did not happen then happens now.
	n, rerr := migrateRebase(places, append(done, moved...), *undo, false)
	out.References = n
	movedText := migrateMovedText(moved)
	switch {
	case failed != nil:
		out.Outcome = "failed"
		out.Message = movedText + failedAt.Base + " was not moved: " + failed.Error() + "."
		if errors.Is(failed, userdir.ErrNoLink) {
			out.Message = movedText + "A link could not be made in " + failedAt.Base + ", so nothing there was moved: without the link, a build from before the rename and paths recorded with the earlier name would not find the files. On Windows, making a link needs Developer Mode or an elevated prompt."
		}
		if rerr != nil {
			out.Message += " The loop registry could not be updated either: " + rerr.Error() + "."
		}
		out.Message += " Run the same command again to finish; what is done is kept."
		return finish(exitFailed)
	case rerr != nil:
		out.Outcome, out.Message = "failed", movedText+"The loop registry could not be updated: "+rerr.Error()+". Run the same command again: it rewrites what is left."
		return finish(exitFailed)
	}
	for _, p := range places {
		userdir.PlaceAt(p.Base).Finish()
	}
	out.Outcome = "done"
	return finish(exitOK)
}

// migrateMovedText says which places were moved, for a run that stopped.
func migrateMovedText(moved []userdir.Place) string {
	if len(moved) == 0 {
		return "Nothing was moved. "
	}
	var bases []string
	for _, p := range moved {
		bases = append(bases, p.Base)
	}
	return "Moved " + strings.Join(bases, " and ") + ". "
}

// migrateHeld collects the locks a run holds through its move, so no check,
// daemon, panel or registry edit can start in a directory while it moves.
// When hold is false locks are only looked at.
type migrateHeld struct {
	hold  bool
	locks []*lock.Lock
	slow  bool // a lock could not be tried in time: look no further
}

func (h *migrateHeld) release() {
	for _, l := range h.locks {
		_ = l.Release()
	}
	h.locks = nil
}

// errLockSlow means a lock file could not be opened and tried in time, and
// errStopWalk ends a walk once the move is refused for it.
var (
	errLockSlow = errors.New("it could not be tried in time")
	errStopWalk = errors.New("stopped")
)

// probe looks at one lock file: busy when another holds it. When holding,
// a free lock is taken and kept. Nothing here can wait longer than
// migrateLockWait: a lock that cannot be tried in that time is in use as
// far as the move is concerned. (Its attempt is left to finish or not;
// whatever it takes is let go when the command exits.)
func (h *migrateHeld) probe(path string) (busy bool, err error) {
	if !h.hold {
		type result struct {
			busy bool
			err  error
		}
		done := make(chan result, 1)
		held := migrateHeldFile // read here: the attempt may outlive this call
		go func() {
			b, err := held(path)
			done <- result{b, err}
		}()
		select {
		case r := <-done:
			return r.busy, r.err
		case <-time.After(migrateLockWait):
			return true, errLockSlow
		}
	}
	if len(h.locks) >= migrateHoldLimit {
		return false, errors.New("too many locks to hold")
	}
	type result struct {
		l   *lock.Lock
		err error
	}
	done := make(chan result, 1)
	hold := migrateHoldFile // read here: the attempt may outlive this call
	go func() {
		l, err := hold(path)
		done <- result{l, err}
	}()
	var l *lock.Lock
	select {
	case r := <-done:
		l, err = r.l, r.err
	case <-time.After(migrateLockWait):
		// An attempt that succeeds after all lets its lock go at once. It
		// touches nothing but its own result.
		go func() {
			if r := <-done; r.l != nil {
				_ = r.l.Release()
			}
		}()
		return true, errLockSlow
	}
	switch {
	case errors.Is(err, lock.ErrHeld):
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	h.locks = append(h.locks, l)
	return false, nil
}

// inUse looks through a directory that is about to be renamed for anything
// that says it is in use: a lock some process holds (a check or a daemon of
// a loop kept there, a session claim, the panel, a registry or route list
// edit) and the record of an installed service, which names the loop's
// files by their full path. It reads directory entries and probes lock
// files, holding the free ones when h holds; it creates nothing.
func (h *migrateHeld) inUse(root string) []migrateRefusal {
	if h.slow {
		return nil
	}
	var out []migrateRefusal
	busyCount, services, seen := 0, 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if seen++; seen > migrateWalkLimit {
			return errors.New("too many entries")
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		switch {
		case migrateIsLock(name):
			busy, err := h.probe(path)
			if errors.Is(err, errLockSlow) {
				// The move is refused now whatever else is found; on a
				// volume where every open stalls, trying the rest would only
				// cost the bound again for each.
				out = append(out, migrateRefusal{Code: "lock_not_tried", Path: path, Message: "this lock could not be tried in " + migrateLockWait.String() + " (the file system may not support locks well), so it is taken to be in use"})
				h.slow = true
				return errStopWalk
			}
			if err != nil {
				return err
			}
			if busy {
				if busyCount++; busyCount <= 5 {
					out = append(out, migrateRefusal{Code: "in_use", Path: path, Message: migrateLockMeaning(root, path)})
				}
			}
		case strings.HasPrefix(name, "service-") && strings.HasSuffix(name, ".json"):
			if services++; services <= 5 {
				out = append(out, migrateRefusal{Code: "service_installed", Path: filepath.Dir(path), Message: "a loop kept here has an installed service, which names its files by their full path. Remove the service (keepgoing service remove), move, then install it again"})
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		out = append(out, migrateRefusal{Code: "not_inspected", Path: root, Message: "it could not be looked through for files in use, so it is not moved"})
	}
	if extra := busyCount + services - len(out); extra > 0 && err == nil {
		out = append(out, migrateRefusal{Code: "in_use", Path: root, Message: fmt.Sprintf("and %d more in use", extra)})
	}
	return out
}

// migrateLockMeaning says what a held lock means, from where it is.
func migrateLockMeaning(root, path string) string {
	rel, _ := filepath.Rel(root, path)
	dir, name := filepath.Split(filepath.ToSlash(rel))
	switch {
	case strings.HasPrefix(dir, "sessions/"):
		return "a check is running: it holds a session claim"
	case strings.HasPrefix(dir, "panel/") && name == clientsLock+".lock":
		return "the BB plugin or the macOS menu is recording which version it is; try again in a moment"
	case strings.HasPrefix(dir, "panel/"):
		return "the panel is running; stop it with keepgoing panel stop"
	case strings.HasPrefix(dir, "registry/"):
		return "the loop registry is being changed"
	case dir == "" && name == "routes.lock":
		return "the standard route list is being changed"
	case name == lock.Daemon+".lock":
		return "a daemon is running for a loop kept here; stop it or remove its service first"
	case name == lock.Tick+".lock":
		return "a check is running for a loop kept here"
	}
	return "a process holds this lock"
}

// migrateLoopsInUse looks at every loop this user's records name, wherever
// its files are: a loop whose check or daemon is running is using the
// session claims that are about to move. The loops are those in the
// registry and those that have ever claimed a session. A loop kept inside a
// directory that was just looked through (walked) has been covered already.
func (h *migrateHeld) loopsInUse(places []userdir.Place, walked []string) []migrateRefusal {
	type loop struct{ name, stateDir string }
	seen := map[string]bool{}
	var loops []loop
	add := func(name, stateDir string) {
		key := session.CleanPath(stateDir)
		if key == "" || seen[key] || len(loops) >= 4*maxOtherLoops {
			return
		}
		for _, root := range walked {
			if pathWithin(session.CleanPath(root), key) {
				return
			}
		}
		seen[key] = true
		loops = append(loops, loop{name, key})
	}
	for _, dir := range migrateRegistries(places) {
		if f, err := registry.Load(dir); err == nil {
			for _, entry := range f.Entries {
				add(entry.Loop, entry.StateDir)
			}
		}
	}
	for _, p := range places {
		for _, name := range []string{userdir.LegacyName, userdir.Name} {
			for _, s := range session.All(filepath.Join(p.Base, name, "sessions"), maxOtherLoops) {
				add(s.Loop, s.StateDir)
			}
		}
	}
	sort.Slice(loops, func(i, j int) bool { return loops[i].name < loops[j].name })
	var out []migrateRefusal
	if h.slow {
		return out
	}
	for _, l := range loops {
		for _, probe := range []struct{ name, message string }{
			{lock.Daemon, "its daemon is running; stop it or remove its service first"},
			{lock.Tick, "it is being checked right now; try again in a moment"},
		} {
			busy, err := h.probe(filepath.Join(l.stateDir, probe.name+".lock"))
			if errors.Is(err, errLockSlow) {
				out = append(out, migrateRefusal{Code: "lock_not_tried", Loop: l.name, Message: "its " + probe.name + " lock could not be tried in " + migrateLockWait.String() + ", so it is taken to be running"})
				h.slow = true
				return out
			}
			if err != nil {
				out = append(out, migrateRefusal{Code: "not_inspected", Loop: l.name, Message: "whether it is running could not be told"})
				break
			}
			if busy {
				out = append(out, migrateRefusal{Code: "loop_running", Loop: l.name, Message: probe.message})
			}
		}
	}
	return out
}

// migrateRegistries lists the registries this user's commands use: the one
// in the per-user directory, under either name, and the one the variable
// names, each once.
func migrateRegistries(places []userdir.Place) []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if key := session.CleanPath(dir); key != "" && !seen[key] {
			if _, err := os.Lstat(key); err == nil {
				seen[key] = true
				dirs = append(dirs, key)
			}
		}
	}
	if len(places) > 0 { // the configuration directory comes first
		add(filepath.Join(places[0].Base, userdir.LegacyName, "registry"))
		add(filepath.Join(places[0].Base, userdir.Name, "registry"))
	}
	if dir, _, err := envvar.RegistryDir.Path(); err == nil && dir != "" {
		add(dir)
	}
	return dirs
}

// migrateRebase rewrites registry references to loops kept inside the given
// places, from one name to the other, and returns how many it rewrote (or,
// with dryRun, would rewrite). It is safe to repeat.
func migrateRebase(places, in []userdir.Place, undo, dryRun bool) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	rewrite := func(path string) string {
		for _, p := range in {
			if moved := p.Rewrite(path, undo); moved != path {
				return moved
			}
		}
		return path
	}
	total := 0
	for _, dir := range migrateRegistries(places) {
		n, err := registry.Rebase(dir, rewrite, dryRun)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func migratePrint(e env, out migrateOutput) {
	w := e.stdout
	moving := "to " + userdir.Name
	if out.Operation == "undo" {
		moving = "back to " + userdir.LegacyName
	}
	for _, p := range out.Places {
		switch {
		case p.Action == "none":
			fmt.Fprintf(w, "%s: nothing to move (%s)\n", p.Base, migrateStatusText(p.Status, out.Operation))
		case p.Done && p.Action == "finish":
			fmt.Fprintf(w, "Finished the move in %s: the files are in %s\n", p.Base, p.To)
		case p.Done:
			fmt.Fprintf(w, "Moved %s to %s\n", p.From, p.To)
		case p.Action == "finish":
			fmt.Fprintf(w, "Would finish an interrupted move in %s: the files are in %s\n", p.Base, p.From)
		default:
			fmt.Fprintf(w, "Would move %s to %s\n", p.From, p.To)
		}
	}
	for _, r := range out.Refusals {
		where := r.Path
		if r.Loop != "" {
			where = "loop " + r.Loop
		}
		fmt.Fprintf(w, "Not moved: %s: %s.\n", where, r.Message)
	}
	switch out.Outcome {
	case "refused":
		fmt.Fprintln(w, "Nothing was changed.")
	case "nothing_to_do":
		fmt.Fprintln(w, "Nothing to do.")
	case "ready":
		moves := false
		for _, p := range out.Places {
			moves = moves || p.Action != "none"
		}
		if out.References > 0 {
			fmt.Fprintf(w, "Would rewrite %d loop registry reference(s) left from an earlier run.\n", out.References)
		}
		if !moves {
			fmt.Fprintln(w, "Nothing was changed. Run the same command with --apply to finish.")
			break
		}
		if out.Operation == "undo" {
			fmt.Fprintf(w, "The link under the earlier name would be removed and the directory renamed back. Nothing was changed. Run keepgoing migrate --undo --apply to move %s.\n", moving)
		} else {
			fmt.Fprintf(w, "A link named %s would be left in place of each directory, so builds from before the rename and paths recorded with that name keep working. Nothing was changed. Run keepgoing migrate --apply to move %s.\n", userdir.LegacyName, moving)
		}
	case "done":
		if out.References > 0 {
			fmt.Fprintf(w, "Rewrote %d loop registry reference(s) to the new place.\n", out.References)
		}
		if out.Operation == "undo" {
			fmt.Fprintln(w, "Done. The directories are under the earlier name again.")
		} else {
			fmt.Fprintf(w, "Done. Each %s is now a link to %s beside it; keep the link while any configuration, script or service names the earlier path. keepgoing migrate --undo --apply moves everything back.\n", userdir.LegacyName, userdir.Name)
		}
	case "failed":
		fmt.Fprintln(w, out.Message)
	}
}

func migrateStatusText(status, operation string) string {
	switch userdir.Status(status) {
	case userdir.Fresh:
		return "no directory of either name"
	case userdir.Current:
		return "already under " + userdir.Name + ", and was never moved"
	case userdir.Moved:
		if operation == "undo" {
			return "moved, with " + userdir.LegacyName + " a link to " + userdir.Name
		}
		return "already moved"
	case userdir.Earlier:
		if operation == "undo" {
			return "already under " + userdir.LegacyName
		}
		return "under " + userdir.LegacyName + ", and was never moved"
	}
	return status
}

// migrateNamesCurrent is what stops a move back: a loop configuration that
// names, by its full path, something under a looptender directory about to
// be renamed back. That path would not exist afterwards, and the loop's state
// would be left behind. Configurations are looked for inside those
// directories, and wherever the registry and the session records say a loop
// is configured. A relative path moves with its configuration and is fine.
func migrateNamesCurrent(places, todo []userdir.Place) []migrateRefusal {
	var configs []string
	seen := map[string]bool{}
	add := func(path string) {
		if key := session.CleanPath(path); key != "" && !seen[key] {
			seen[key] = true
			configs = append(configs, path)
		}
	}
	for _, p := range todo {
		for _, pattern := range []string{"*.yaml", filepath.Join("loops", "*", "*.yaml")} {
			found, _ := filepath.Glob(filepath.Join(p.Current, pattern))
			for _, f := range found {
				add(f)
			}
		}
	}
	for _, dir := range migrateRegistries(places) {
		if f, err := registry.Load(dir); err == nil {
			for _, entry := range f.Entries {
				add(entry.ConfigPath)
			}
		}
	}
	for _, p := range places {
		for _, name := range []string{userdir.LegacyName, userdir.Name} {
			for _, s := range session.All(filepath.Join(p.Base, name, "sessions"), 4*maxOtherLoops) {
				add(s.Config)
			}
		}
	}
	home, _ := os.UserHomeDir()
	var out []migrateRefusal
	for _, path := range configs {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc any
		if yaml.Unmarshal(raw, &doc) != nil {
			continue
		}
		var named []string
		migrateStrings(doc, func(s string) {
			if strings.HasPrefix(s, "~/") && home != "" {
				s = filepath.Join(home, s[2:])
			}
			if !filepath.IsAbs(s) {
				return
			}
			for _, p := range todo {
				if p.Under(s, userdir.Name) {
					named = append(named, s)
					return
				}
			}
		})
		if len(named) > 0 {
			sort.Strings(named)
			if len(named) > 3 {
				named = append(named[:3], fmt.Sprintf("and %d more", len(named)-3))
			}
			out = append(out, migrateRefusal{Code: "names_current_path", Path: path, Message: "this loop configuration names " + strings.Join(named, ", ") + ", under " + userdir.Name + "; after moving back that path would not exist and the loop's files would be left behind. Change it to a path under " + userdir.LegacyName + " or to a relative one first, or keep the directories as they are"})
		}
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// migrateStrings calls f with every string value in a decoded YAML document.
func migrateStrings(v any, f func(string)) {
	switch t := v.(type) {
	case string:
		f(t)
	case []any:
		for _, x := range t {
			migrateStrings(x, f)
		}
	case map[string]any:
		for _, x := range t {
			migrateStrings(x, f)
		}
	case map[any]any:
		for _, x := range t {
			migrateStrings(x, f)
		}
	}
}
