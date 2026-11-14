package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
// before moving it.
const migrateWalkLimit = 200000

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

	// What each place needs, and what in it must not be in use.
	var todo []userdir.Place
	var walked []string
	for _, p := range places {
		mp := migratePlace{Base: p.Base, Status: string(p.Status), Action: "none"}
		from, to, moves := p.Earlier, p.Current, userdir.Earlier
		if *undo {
			from, to, moves = p.Current, p.Earlier, userdir.Moved
		}
		switch p.Status {
		case moves:
			mp.Action, mp.From, mp.To = "move", from, to
		case userdir.Interrupted:
			// The files are under the current name whichever way it was going.
			mp.Action, mp.From, mp.To = "finish", p.Current, to
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
			out.Refusals = append(out.Refusals, migrateInUse(holds)...)
		}
		out.Places = append(out.Places, mp)
	}
	if len(todo) > 0 {
		out.Refusals = append(out.Refusals, migrateLoopsInUse(places, walked)...)
	}
	switch {
	case len(out.Refusals) > 0:
		out.Outcome = "refused"
		return finish(exitFailed)
	case len(todo) == 0:
		out.Outcome = "nothing_to_do"
		return finish(exitOK)
	case !*apply:
		out.Outcome = "ready"
		return finish(exitOK)
	}

	for _, p := range userdir.InMoveOrder(todo, *undo) {
		move := p.Move
		if *undo {
			move = p.MoveBack
		}
		if err := move(); err != nil {
			out.Outcome, out.Message = "failed", err.Error()
			if errors.Is(err, userdir.ErrNoLink) {
				out.Message = "A link could not be made in " + p.Base + ", so nothing there was moved: without the link, a build from before the rename and paths recorded with the earlier name would not find the files. On Windows, making a link needs Developer Mode or an elevated prompt."
			}
			return finish(exitFailed)
		}
		for i := range out.Places {
			if out.Places[i].Base == p.Base {
				out.Places[i].Done = true
			}
		}
	}
	// The registry records full paths. References to loops kept inside a
	// moved directory are rewritten to its new name, so they still match
	// what a command resolves the loop's files to.
	n, err := migrateRebase(places, *undo)
	out.References = n
	if err != nil {
		out.Outcome, out.Message = "failed", "The directories were moved, but the loop registry could not be updated: "+err.Error()+". Run the same command again."
		return finish(exitFailed)
	}
	out.Outcome = "done"
	return finish(exitOK)
}

// migrateInUse looks through a directory that is about to be renamed for
// anything that says it is in use: a lock some process holds (a check or a
// daemon of a loop kept there, a session claim, the panel, a registry edit)
// and the record of an installed service, which names the loop's files by
// their full path. It reads directory entries and probes lock files; it
// creates and changes nothing.
func migrateInUse(root string) []migrateRefusal {
	var out []migrateRefusal
	held, services, seen := 0, 0, 0
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
		case strings.HasSuffix(name, ".lock"):
			busy, err := lock.HeldFile(path)
			if err != nil {
				return err
			}
			if busy {
				if held++; held <= 5 {
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
	if err != nil {
		out = append(out, migrateRefusal{Code: "not_inspected", Path: root, Message: "it could not be looked through for files in use, so it is not moved"})
	}
	if extra := held + services - len(out); extra > 0 && err == nil {
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
	case strings.HasPrefix(dir, "panel/"):
		return "the panel is running; stop it with keepgoing panel stop"
	case strings.HasPrefix(dir, "registry/"):
		return "the loop registry is being changed"
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
func migrateLoopsInUse(places []userdir.Place, walked []string) []migrateRefusal {
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
	for _, l := range loops {
		for _, probe := range []struct{ name, message string }{
			{lock.Daemon, "its daemon is running; stop it or remove its service first"},
			{lock.Tick, "it is being checked right now; try again in a moment"},
		} {
			busy, err := lock.Held(l.stateDir, probe.name)
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

// migrateRebase rewrites registry references into the moved directories.
// It is run after every move, including one that only finished an
// interrupted move, and rewrites nothing that is already right.
func migrateRebase(places []userdir.Place, undo bool) (int, error) {
	total := 0
	for _, dir := range migrateRegistries(places) {
		for _, p := range places {
			// As the registry resolved them when the references were added.
			base, err := filepath.EvalSymlinks(p.Base)
			if err != nil {
				continue
			}
			from, to := filepath.Join(base, userdir.LegacyName), filepath.Join(base, userdir.Name)
			if undo {
				from, to = to, from
			}
			n, err := registry.Rebase(dir, from, to)
			if err != nil {
				return total, err
			}
			total += n
		}
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
		return "already moved"
	case userdir.Earlier:
		return "under " + userdir.LegacyName + ", and was never moved"
	}
	return status
}
