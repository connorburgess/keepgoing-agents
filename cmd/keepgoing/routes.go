package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// routeListsOut is the read side for a page that shows and orders routes. It
// names models, groups and loops. It never carries a conversation ID, an
// account directory, a working directory or a file path.
type routeListsOut struct {
	SchemaVersion int              `json:"schemaVersion"`
	Kind          string           `json:"kind"` // route_lists
	Standard      standardListView `json:"standard"`
	Loops         []loopRoutesView `json:"loops"`
}

type standardListView struct {
	// State is ok, missing (no list has been written) or unreadable.
	State string `json:"state"`
	// Problem is the fixed reason an unreadable list was refused: not_private,
	// not_owned, not_regular, too_large, inside_checkout, named_twice, unreadable or
	// invalid. It names no path.
	Problem    string             `json:"problem,omitempty"`
	Redundancy *redundancyView    `json:"redundancy,omitempty"`
	Routes     []standardRouteRow `json:"routes"`
	// Followers are the loops shown here that follow the standard list.
	Followers []string `json:"followers"`
	// Can lists the changes this caller may make to the list: add, move,
	// remove, off and on. When it is empty, CanReason says why:
	// agent_identity (only a person changes the standard list) or
	// list_unreadable.
	Can       []string `json:"can"`
	CanReason string   `json:"canReason,omitempty"`
}

type redundancyView struct {
	MinHealthy int    `json:"minHealthy"`
	Span       string `json:"span"`
}

type standardRouteRow struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Effort     string `json:"effort,omitempty"`
	Group      string `json:"group"`
	Enter      string `json:"enter"`
	ProbeModel string `json:"probeModel,omitempty"`
	Disabled   bool   `json:"disabled"`
}

type loopRoutesView struct {
	Loop string `json:"loop"`
	// List is standard, own or none.
	List string `json:"list"`
	// Problem is set when nothing more can be said about the loop:
	// configuration_unavailable.
	Problem string `json:"problem,omitempty"`
	// ListUnreadable: the loop follows the standard list, which cannot be
	// read; it has no other routes and will not hand off.
	ListUnreadable        bool           `json:"listUnreadable"`
	Handoff               string         `json:"handoff"` // on or off
	AllowsNewConversation bool           `json:"allowsNewConversation"`
	Current               string         `json:"current"` // id of the route the loop is on
	First                 *loopRouteRow  `json:"first,omitempty"`
	Routes                []loopRouteRow `json:"routes"`
	// Skipped are standard routes that do not apply to this loop, with why.
	Skipped    []skippedRoute  `json:"skipped"`
	Redundancy *health.Summary `json:"redundancy,omitempty"`
	Warnings   []string        `json:"warnings"`
	// Can lists the changes this caller may make to the loop's own list with
	// routes --loop: move, remove, off and on. When it is empty, CanReason
	// says why: follows_standard, no_list, not_owner, ownership_unknown or
	// configuration_unavailable.
	Can       []string `json:"can"`
	CanReason string   `json:"canReason,omitempty"`
}

type loopRouteRow struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Group    string `json:"group"`
	// Enter is first for the loop's first route, otherwise same_conversation,
	// other_conversation, new_conversation, headless or command.
	Enter string `json:"enter"`
	// Source is loop for the first route, otherwise standard or own.
	Source  string `json:"source"`
	Current bool   `json:"current"`
	// NotInList: the loop is on this route, and it is no longer in its list.
	NotInList bool        `json:"notInList"`
	Disabled  bool        `json:"disabled"`
	Healthy   bool        `json:"healthy"`
	LastCheck *routeCheck `json:"lastCheck,omitempty"`
	Usage     *usageView  `json:"usage,omitempty"`
}

type routeCheck struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"errorClass,omitempty"`
}

type skippedRoute struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func routesCommand(e env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "add", "move", "remove", "off", "on":
			return routesEditCommand(e, args[0], args[1:])
		case "check":
			return routesCheckCommand(e, args[1:])
		}
	}
	if len(args) == 0 || args[0] != "list" {
		e.errf("routes needs a subcommand: list, add, move, remove, off, on or check")
		return exitUsage
	}
	fs := flag.NewFlagSet("routes list", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	asJSON := fs.Bool("json", false, "print the versioned route lists object")
	cfgPath := fs.String("c", "", "show one loop by its config file instead of every registered loop")
	loopName := fs.String("loop", "", "show one registered loop by name")
	directory := fs.String("directory", "", "private registry directory (default: the registry in the user configuration directory)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || (*cfgPath != "" && *loopName != "") {
		if err == nil {
			e.errf("routes list takes --json and one of -c or --loop")
		}
		return exitUsage
	}
	var paths []routeLoopRef
	if *cfgPath != "" {
		abs, err := filepath.Abs(*cfgPath)
		if err != nil {
			e.errf("config unavailable")
			return exitFailed
		}
		paths = []routeLoopRef{{path: abs}}
	} else {
		dir, err := registryDirectory(*directory)
		if err != nil {
			e.errf("%v", err)
			return exitFailed
		}
		f, err := registry.Load(dir)
		if err != nil {
			e.errf("%v", err)
			return exitFailed
		}
		found := false
		for _, entry := range f.Entries {
			if *loopName == "" || entry.Loop == *loopName {
				paths = append(paths, routeLoopRef{path: entry.ConfigPath, name: entry.Loop})
				found = true
			}
		}
		if *loopName != "" && !found {
			e.errf("no registered loop named %q", *loopName)
			return exitUsage
		}
	}
	out := routeLists(paths, time.Now().UTC())
	if *asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return exitOK
	}
	printRouteLists(e, out)
	return exitOK
}

// routeLists reads the standard list and each loop's effective routes. It
// only reads: no lock is taken, nothing is probed and nothing is written.
// routeLoopRef is a loop to show: its config file and, when it came from the
// registry, its registered name, used if the config cannot be loaded.
type routeLoopRef struct{ path, name string }

func routeLists(configPaths []routeLoopRef, now time.Time) routeListsOut {
	out := routeListsOut{SchemaVersion: 1, Kind: "route_lists", Loops: []loopRoutesView{}}
	out.Standard = standardList()
	for _, ref := range configPaths {
		view := loopRoutes(ref, now)
		if view.List == config.RoutesStandard {
			out.Standard.Followers = append(out.Standard.Followers, view.Loop)
		}
		out.Loops = append(out.Loops, view)
	}
	return out
}

func standardList() (view standardListView) {
	view = standardListView{State: "unreadable", Routes: []standardRouteRow{}, Followers: []string{}, Can: []string{}}
	var path string
	// Can offers only what would work now: nothing for an agent or an
	// unreadable list, only add for an empty or missing list, and no add
	// while the history cannot be read.
	defer func() {
		switch {
		case os.Getenv("BB_THREAD_ID") != "":
			view.CanReason = "agent_identity"
			return
		case view.State == "ok" && len(view.Routes) > 0:
			view.Can = []string{"add", "move", "remove", "off", "on"}
		case view.State == "ok", view.State == "missing":
			view.Can = []string{"add"}
		default:
			view.CanReason = "list_unreadable"
			return
		}
		if _, _, err := routeHistoryIDs(filepath.Dir(path)); err != nil {
			view.Can = view.Can[1:]
			if len(view.Can) == 0 {
				view.CanReason = "history_unreadable"
			}
		}
	}()
	path, err := config.StandardRoutesPath()
	if err != nil {
		view.Problem = config.RoutesProblem(err)
		return view
	}
	r, err := config.LoadRoutes(path)
	if errors.Is(err, os.ErrNotExist) {
		view.State = "missing"
		return view
	}
	if err != nil {
		view.Problem = config.RoutesProblem(err)
		return view
	}
	view.State = "ok"
	view.Redundancy = &redundancyView{MinHealthy: r.Redundancy.MinHealthy, Span: r.Redundancy.Span}
	for _, rt := range r.Routes {
		view.Routes = append(view.Routes, standardRouteRow{ID: rt.ID, Provider: rt.Provider, Model: rt.Model, Effort: rt.Effort, Group: rt.Group, Enter: rt.Enter, ProbeModel: rt.Probe.Model, Disabled: rt.Disabled})
	}
	return view
}

func agentEffort(a config.Agent) string {
	switch {
	case a.BB != nil:
		return a.BB.Reasoning
	case a.Claude != nil:
		return a.Claude.Effort
	}
	return ""
}

// routeEnter names how a loop enters one of its routes, from the resolved
// candidate, so a list of the loop's own reads the same way as the standard.
func routeEnter(l config.Loop, c config.Candidate) string {
	switch {
	case c.BB != nil && c.BB.NewThread:
		return config.EnterNewConversation
	case c.BB != nil && l.BB != nil && c.BB.Thread == l.BB.Thread:
		return config.EnterSameConversation
	case c.BB != nil:
		return "other_conversation"
	case c.Claude != nil || c.Codex != nil:
		return config.EnterHeadless
	}
	return "command"
}

func loopRoutes(ref routeLoopRef, now time.Time) loopRoutesView {
	name := ref.name
	if name == "" {
		name = filepath.Base(filepath.Dir(ref.path))
	}
	view := loopRoutesView{Loop: name, List: "none", Handoff: "off", Routes: []loopRouteRow{}, Skipped: []skippedRoute{}, Warnings: []string{}, Can: []string{}}
	cfg, err := config.Load(ref.path)
	if err != nil {
		view.Problem, view.CanReason = "configuration_unavailable", "configuration_unavailable"
		return view
	}
	l := cfg.Loop
	switch {
	case l.FollowsStandard():
		view.CanReason = "follows_standard"
	case cfg.FallbackSet == nil:
		view.CanReason = "no_list"
	default:
		view.Can, view.CanReason = ownListCan(l.StateDir)
	}
	view.Loop = l.Name
	if cfg.RouteList.Source != "" {
		view.List = cfg.RouteList.Source
	}
	view.ListUnreadable = cfg.RouteList.Unreadable
	view.AllowsNewConversation = l.AllowsNewConversation()
	if len(l.Errors.FailoverOn) > 0 {
		view.Handoff = "on"
	}
	for _, s := range cfg.RouteList.Skipped {
		view.Skipped = append(view.Skipped, skippedRoute{ID: s.ID, Reason: s.Reason})
	}
	view.Warnings = append(view.Warnings, cfg.RouteList.Warnings...)
	if view.ListUnreadable {
		view.Warnings = append(view.Warnings, config.StandardRoutesUnreadable)
	}
	st, _ := state.Load(l.StateDir)
	view.Current = st.Target()
	view.First = &loopRouteRow{ID: state.Primary, Provider: l.Provider, Model: l.Agent.Model(), Effort: agentEffort(l.Agent), Group: l.Group, Enter: "first", Source: "loop", Current: view.Current == state.Primary, Healthy: true}
	fb, outside := supervise.EffectiveRoutes(cfg, st)
	if fb == nil {
		return view
	}
	reg, err := health.Load(l.StateDir)
	if err != nil {
		reg = nil
		view.Warnings = append(view.Warnings, "recorded check results are unavailable")
	}
	group := l.Group
	if c, ok := fb.Find(view.Current); ok {
		group = c.Group
	}
	if reg != nil {
		summary := health.Evaluate(fb, reg, now, view.Current, group)
		view.Redundancy = &summary
	}
	for _, c := range fb.Candidates {
		row := loopRouteRow{ID: c.ID, Provider: c.Provider, Model: c.Model(), Effort: agentEffort(c.Agent), Group: c.Group, Enter: routeEnter(l, c), Source: view.List, Current: c.ID == view.Current, Disabled: c.Disabled}
		row.NotInList = outside && row.Current
		if row.NotInList {
			view.Warnings = append(view.Warnings, fmt.Sprintf("the loop is on route %q, which is no longer in its list; it stays there until the ordinary rules move it", c.ID))
		}
		if reg != nil {
			row.Healthy = reg.Healthy(c.ID, now, fb.Probe.TTL.Duration)
			if en, ok := reg.Candidates[c.ID]; ok && en.Last != nil {
				row.LastCheck = &routeCheck{At: en.Last.At, OK: en.Last.OK, ErrorClass: en.Last.ErrorClass}
			}
			u := usageOf(reg, c, now, projectionRules(fb))
			u.ID = ""
			row.Usage = &u
		}
		view.Routes = append(view.Routes, row)
	}
	return view
}

func printRouteLists(e env, out routeListsOut) {
	w := e.stdout
	switch out.Standard.State {
	case "missing":
		fmt.Fprintln(w, "Standard route list: none written yet.")
	case "unreadable":
		fmt.Fprintf(w, "Standard route list: unreadable (%s): %s. Loops that follow it have no other routes until it is repaired.\n", out.Standard.Problem, routesProblemText(out.Standard.Problem))
	default:
		fmt.Fprintf(w, "Standard route list: %d routes; followed by %d of the loops shown.\n", len(out.Standard.Routes), len(out.Standard.Followers))
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  #\tID\tPROVIDER\tMODEL\tEFFORT\tGROUP\tENTERED AS\tSTATE")
		for i, r := range out.Standard.Routes {
			stateText := "on"
			if r.Disabled {
				stateText = "off"
			}
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, r.ID, r.Provider, r.Model, dash(r.Effort), r.Group, strings.ReplaceAll(r.Enter, "_", " "), stateText)
		}
		tw.Flush()
	}
	for _, l := range out.Loops {
		fmt.Fprintln(w)
		if l.Problem != "" {
			fmt.Fprintf(w, "%s: configuration unavailable; inspect it with keepgoing validate.\n", l.Loop)
			continue
		}
		list := map[string]string{"standard": "follows the standard list", "own": "has its own list", "none": "has no other routes"}[l.List]
		fmt.Fprintf(w, "%s: %s; handoff %s; now on %s.\n", l.Loop, list, l.Handoff, l.Current)
		tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  #\tID\tPROVIDER\tMODEL\tEFFORT\tGROUP\tENTERED AS\tFROM\tHEALTHY")
		rows := l.Routes
		if l.First != nil {
			rows = append([]loopRouteRow{*l.First}, rows...)
		}
		for i, r := range rows {
			healthy := "yes"
			switch {
			case r.Enter == "first":
				healthy = "-"
			case r.Disabled:
				healthy = "off"
			case !r.Healthy:
				healthy = "no"
			}
			mark := ""
			if r.Current {
				mark = " (current)"
			}
			if r.NotInList {
				mark = " (current; no longer in the list)"
			}
			fmt.Fprintf(tw, "  %d\t%s%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, r.ID, mark, r.Provider, dash(r.Model), dash(r.Effort), r.Group, strings.ReplaceAll(r.Enter, "_", " "), r.Source, healthy)
		}
		tw.Flush()
		for _, s := range l.Skipped {
			fmt.Fprintf(w, "  not used here: %s (%s)\n", s.ID, s.Reason)
		}
		for _, warning := range l.Warnings {
			fmt.Fprintf(w, "  note: %s\n", warning)
		}
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// effectiveRoutes is the list a loop's current target is looked up in: for a
// loop that follows the standard list and is on a route no longer in it, that
// route from its saved record as well.
func effectiveRoutes(cfg *config.Config, st state.File) *config.Fallbacks {
	fb, _ := supervise.EffectiveRoutes(cfg, st)
	return fb
}

// currentRoutes is effectiveRoutes with the loop's state read here. When the
// state cannot be read, the configured list is used.
func currentRoutes(cfg *config.Config) *config.Fallbacks {
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return cfg.FallbackSet
	}
	return effectiveRoutes(cfg, st)
}

// followsStandardRefusal is what a fallbacks command says for a loop that
// follows the standard route list, which has no fallbacks file to edit.
func followsStandardRefusal(sub string) string {
	if sub == "handoff" {
		return "This loop follows the standard route list. To turn handoff on or off, edit errors.failover_on in keepgoing.yaml."
	}
	return "This loop follows the standard route list. Change that list instead with keepgoing routes add, move, remove, off or on."
}

// ownListCan is what this caller may change in a loop's own list: all of it
// for the loop's owner, or a person outside BB, and nothing for another
// thread, or for any thread when the loop has no recorded owner. It only
// reads; the write checks again under the lock.
func ownListCan(stateDir string) ([]string, string) {
	f, err := control.Load(stateDir)
	if err != nil {
		return []string{}, "ownership_unknown"
	}
	caller := os.Getenv("BB_THREAD_ID")
	if caller != "" && f.Ownership == nil {
		return []string{}, "agent_identity"
	}
	if control.CheckOwner(f, caller, false) != nil {
		return []string{}, "not_owner"
	}
	return []string{"move", "remove", "off", "on"}, ""
}

// routesProblemText says what to do about each refusal reason.
func routesProblemText(code string) string {
	switch code {
	case config.RoutesNotPrivate:
		return "it is readable or writable by others; run chmod 600 on it"
	case config.RoutesNotOwned:
		return "it, or the symbolic link to it, is owned by another user"
	case config.RoutesNotRegular:
		return "it is not a regular file"
	case config.RoutesTooLarge:
		return "it is larger than 1 MiB"
	case config.RoutesInsideCheckout:
		return config.RoutesFileEnv + " points inside a Git checkout"
	case config.RoutesNamedTwice:
		return envvar.RoutesFile.Name() + " and " + envvar.RoutesFile.Legacy() + " name different files; unset one of them"
	case "missing":
		return "no list has been written"
	case config.RoutesInvalid:
		return "its contents are not a valid route list; keepgoing validate shows why"
	}
	return "it could not be read"
}
