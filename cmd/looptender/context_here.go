package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/loopcontext"
	"github.com/looptender/looptender/internal/session"
)

// context --here answers "which loop supervises this session" for the
// session running the command. It identifies the caller from BB_THREAD_ID,
// or from the working directory when that is not set, finds the loops in
// that session's supervisors records (see internal/session), and keeps only
// those whose active target, by their current configuration and state, is
// that session. Like context, it writes nothing, takes no lock, and queries
// neither a provider nor the daemon; unlike the claim directory's other
// users it does not create or repair that directory.

// Outcomes of context --here.
const (
	hereNone    = "none"
	hereOne     = "one"
	hereSeveral = "several"
	// hereUnknown: the records could not be read, or a recorded loop could
	// not be checked, so the answer may be incomplete.
	hereUnknown = "unknown"
)

// Fixed sentences of context --here. Each must hold in every configuration.
const (
	hereNoLoop       = "no loop targets this session"
	hereBBCaller     = "taken from BB_THREAD_ID, which is checked only for its form and not with BB; the working directory is not used."
	hereDirCaller    = "BB_THREAD_ID is not set, so the caller is identified only by its working directory: a loop whose active target is a headless agent working in this directory is found, whichever conversation in that directory is asking."
	hereProcessLimit = "A process loop's session is its resume command, which a working directory cannot match, so a process loop is not found this way."
	hereRecordsBasis = "Loops are found only in the session records that enable and each check write, then checked against each loop's current configuration and state; a loop that has not recorded this session is not found."
	hereNoParent     = "Parent threads are not looked up, so a lane is not found through the loop that supervises its parent."
)

// hereView is context --here's answer. Context is S1's context for each loop
// found.
type hereView struct {
	SchemaVersion int        `json:"schemaVersion"`
	Caller        hereCaller `json:"caller"`
	Outcome       string     `json:"outcome"`
	Text          string     `json:"text"`
	Loops         []hereLoop `json:"loops"`
	NotCounted    []hereSkip `json:"notCounted"`
	Basis         []string   `json:"basis"`
	Unavailable   string     `json:"unavailable,omitempty"`
	ids           []session.Identity
}

// hereCaller is how the calling session was identified.
type hereCaller struct {
	// Source is bb_thread_id or working_directory.
	Source   string `json:"source"`
	BBThread string `json:"bbThread,omitempty"`
	Workdir  string `json:"workdir,omitempty"`
	Text     string `json:"text"`
}

type hereLoop struct {
	Loop    string           `json:"loop"`
	Config  string           `json:"config"`
	Context loopcontext.View `json:"context"`
}

// hereSkip is a recorded loop that is not counted. Verified is false when
// whether it targets this session could not be established.
type hereSkip struct {
	Loop     string `json:"loop"`
	Config   string `json:"config"`
	Reason   string `json:"reason"`
	Verified bool   `json:"verified"`
}

// contextHereCmd prints the supervision context of the loop that targets the
// calling session. It exits 1 when the answer may be incomplete or a found
// loop's records could not all be read, and 2 when BB_THREAD_ID is set but
// is not a BB thread ID.
func contextHereCmd(e env, now time.Time, asJSON bool) int {
	thread := os.Getenv("BB_THREAD_ID")
	if thread != "" && control.CheckOwner(control.File{}, thread, false) != nil {
		e.errf("context --here: BB_THREAD_ID is set but is not a BB thread ID")
		return exitUsage
	}
	cwd := ""
	if thread == "" {
		wd, err := os.Getwd()
		if err != nil {
			e.errf("context --here: the working directory cannot be read")
			return exitFailed
		}
		cwd = session.CleanPath(wd)
	}
	v := hereFind(thread, cwd, now)
	if asJSON {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
	} else {
		fmt.Fprint(e.stdout, v.text())
	}
	if v.Outcome == hereUnknown {
		return exitFailed
	}
	for _, l := range v.Loops {
		if len(l.Context.Unavailable) > 0 {
			return exitFailed
		}
	}
	return exitOK
}

// hereCallerOf identifies the caller and the sessions it may be.
func hereCallerOf(thread, cwd string) (hereCaller, []session.Identity) {
	if thread != "" {
		return hereCaller{Source: "bb_thread_id", BBThread: thread, Text: "BB thread " + thread + ", " + hereBBCaller},
			[]session.Identity{{Kind: "bb", ID: thread}}
	}
	ids := make([]session.Identity, 0, len(session.HeadlessKinds))
	for _, k := range session.HeadlessKinds {
		ids = append(ids, session.Identity{Kind: k, ID: cwd})
	}
	return hereCaller{Source: "working_directory", Workdir: cwd, Text: "working directory " + cwd + ". " + hereDirCaller}, ids
}

// hereFind builds the answer. It reads the session records and each recorded
// loop's configuration and records; it changes nothing.
func hereFind(thread, cwd string, now time.Time) hereView {
	caller, ids := hereCallerOf(thread, cwd)
	v := hereView{SchemaVersion: 1, Caller: caller, Loops: []hereLoop{}, NotCounted: []hereSkip{}, ids: ids}
	v.Basis = []string{hereRecordsBasis, hereNoParent}
	if thread == "" {
		v.Basis = append(v.Basis, hereProcessLimit)
	}
	dir, _, err := session.Lookup()
	if err != nil {
		v.Unavailable = "the session records cannot be read: " + err.Error()
		v.finish()
		return v
	}
	seen := map[string]bool{}
	var recorded []session.Supervisor
	for _, id := range ids {
		list, err := session.Recorded(dir, id)
		if err != nil {
			v.Unavailable = err.Error()
			v.finish()
			return v
		}
		for _, s := range list {
			key := session.CleanPath(s.StateDir)
			if key == "" {
				key = s.StateDir
			}
			if !seen[key] {
				seen[key] = true
				recorded = append(recorded, s)
			}
		}
	}
	for _, s := range recorded {
		v.check(s, now)
	}
	v.finish()
	return v
}

// check revalidates one recorded loop: its configuration still loads, still
// names the state directory it was recorded with, and its active target is
// the caller's session.
func (v *hereView) check(s session.Supervisor, now time.Time) {
	skip := func(verified bool, reason string) {
		v.NotCounted = append(v.NotCounted, hereSkip{Loop: s.Loop, Config: s.Config, Reason: reason, Verified: verified})
	}
	if _, err := os.Stat(s.Config); errors.Is(err, os.ErrNotExist) {
		skip(true, "its configuration file no longer exists")
		return
	}
	cfg, err := config.Load(s.Config)
	if err != nil {
		skip(false, "its configuration does not load, so its target is unknown")
		return
	}
	if mine, theirs := session.CleanPath(cfg.Loop.StateDir), session.CleanPath(s.StateDir); mine != theirs {
		skip(true, "its configuration now names another state directory")
		return
	}
	in := readContextInput(cfg, s.Config, now)
	switch {
	case in.State == nil:
		skip(false, "its state.json cannot be read, so its active route is unknown")
		return
	case in.TargetPending:
		skip(false, "its active route starts a new BB thread that has not been recorded yet, so its target is unknown")
		return
	case in.TargetAgent == nil:
		skip(true, "its active route is missing or switched off in its configuration")
		return
	}
	id, ok := session.Of(*in.TargetAgent)
	if !ok || !v.matches(id) {
		skip(true, fmt.Sprintf("its active route %q targets another session", in.State.Target()))
		return
	}
	v.Loops = append(v.Loops, hereLoop{Loop: cfg.Loop.Name, Config: cfg.Path(), Context: loopcontext.Build(in)})
}

func (v *hereView) matches(id session.Identity) bool {
	for _, c := range v.ids {
		if c == id {
			return true
		}
	}
	return false
}

// finish sets the outcome and its sentence.
func (v *hereView) finish() {
	unverified := 0
	for _, s := range v.NotCounted {
		if !s.Verified {
			unverified++
		}
	}
	names := make([]string, len(v.Loops))
	for i, l := range v.Loops {
		names[i] = l.Loop
	}
	switch {
	case v.Unavailable != "":
		v.Outcome, v.Text = hereUnknown, "whether a loop targets this session is unknown: "+v.Unavailable
		return
	case len(v.Loops) == 0:
		v.Outcome, v.Text = hereNone, hereNoLoop
	case len(v.Loops) == 1:
		v.Outcome, v.Text = hereOne, "loop "+names[0]+" targets this session"
	default:
		v.Outcome, v.Text = hereSeveral, fmt.Sprintf("%d loops target this session: %s. Two loops on one session can both prompt it, though never at the same moment", len(v.Loops), strings.Join(names, ", "))
	}
	if unverified > 0 {
		if len(v.Loops) == 0 {
			v.Text = "no loop is known to target this session"
		}
		v.Outcome = hereUnknown
		v.Text += fmt.Sprintf("; whether %d other recorded loop(s) do could not be checked", unverified)
	}
}

// text is the answer as plain lines: the caller and outcome, then each
// loop's context.
func (v hereView) text() string {
	var b strings.Builder
	line := func(label, s string) { fmt.Fprintf(&b, "%-10s %s\n", label+":", s) }
	line("caller", v.Caller.Text)
	line("found", v.Text)
	for _, s := range v.NotCounted {
		line("not counted", fmt.Sprintf("loop %s (%s): %s", s.Loop, s.Config, s.Reason))
	}
	for _, s := range v.Basis {
		line("basis", s)
	}
	for _, l := range v.Loops {
		fmt.Fprintf(&b, "\n%s (%s)\n", l.Loop, l.Config)
		b.WriteString(l.Context.Text())
	}
	return b.String()
}
