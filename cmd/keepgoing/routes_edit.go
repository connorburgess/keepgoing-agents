package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// routeChangeOut is the versioned result of one change to a route list, for
// a page as well as a person. A route is named by its id, which never
// changes once it is added: a move changes the order, never an id. A page
// that reviewed a change by id applies it with the expect value from the
// preview; if the list, the request, or a following loop's configuration
// or current route changed since, the answer is review_again and nothing is
// written, so a reviewed id can never act on another route.
type routeChangeOut struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"` // route_change
	// Outcome is preview, changed, unchanged, refused or review_again.
	Outcome string `json:"outcome"`
	Op      string `json:"op"`   // add, move, remove, off or on
	List    string `json:"list"` // standard or own
	Loop    string `json:"loop,omitempty"`
	Route   string `json:"route"`
	From    int    `json:"from,omitempty"` // 1-based place before the change
	To      int    `json:"to,omitempty"`   // 1-based place after it
	// Expect binds an apply to what this preview showed.
	Expect string `json:"expect,omitempty"`
	// Before and After are the list's ids in order, switched-off ones included.
	Before []string           `json:"before"`
	After  []string           `json:"after"`
	Routes []standardRouteRow `json:"routes"` // the list after the change
	// Loops are the registered loops the change applies to: those that
	// follow the standard list, or the one loop whose own list it is.
	Loops    []routeChangeLoop `json:"loops"`
	Warnings []string          `json:"warnings"`
	Reason   string            `json:"reason,omitempty"` // why nothing changed
	Refusal  *routeRefusal     `json:"refusal,omitempty"`
}

type routeChangeLoop struct {
	Loop    string `json:"loop"`
	Problem string `json:"problem,omitempty"` // configuration_unavailable
	Current string `json:"current"`
	// Before and After are the routes the loop can hand off to, in order.
	Before []string `json:"before"`
	After  []string `json:"after"`
	// NewlySkipped are routes in the list after the change that do not apply
	// to this loop and did apply, or were not in the list, before it.
	NewlySkipped []skippedRoute `json:"newlySkipped"`
	// CurrentLeavesList: the loop is on a route the change takes out of its
	// routes. It stays there until the ordinary rules move it.
	CurrentLeavesList bool `json:"currentLeavesList"`
	// NoRouteApplies: after the change the loop has no route to hand off to.
	NoRouteApplies bool `json:"noRouteApplies"`
	// BelowMinHealthy: after the change, by the recorded check results, the
	// loop has fewer healthy routes than redundancy.min_healthy asks for.
	BelowMinHealthy bool `json:"belowMinHealthy"`
}

// routeRefusal says why nothing was written, by a fixed code.
type routeRefusal struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

func refuseRoute(code, format string, a ...any) *routeRefusal {
	return &routeRefusal{Code: code, Text: fmt.Sprintf(format, a...)}
}

// routeChangeRequest is one change, as a person or a page asks for it.
type routeChangeRequest struct {
	op       string
	id       string
	to       int          // move: the new 1-based place
	at       int          // add: the 1-based place, 0 for last
	route    config.Route // add
	like     string       // add: the LOOP:ID the route was copied from
	loop     string       // a registered loop's own list; "" for the standard list
	expect   string
	caller   string // BB_THREAD_ID; "" for a person
	source   string // cli
	registry string
	now      time.Time
}

const routesEditUsage = `Show, change or check route lists without editing YAML.

Usage:
  keepgoing routes list [--json] [--loop NAME | -c looptender.yaml]
  keepgoing routes add --id ID --provider P --model M --group G --enter E [options] [--at N]
  keepgoing routes add --like LOOP:ID [--id ID] [options] [--at N]
  keepgoing routes move ID --to N [--loop NAME]
  keepgoing routes remove ID [--loop NAME]
  keepgoing routes off ID [--loop NAME]
  keepgoing routes on ID [--loop NAME]
  keepgoing routes check [--loop NAME [ID] [fallbacks check flags]]

Without --loop a change is to the standard route list, which only a person
can make (not a BB thread). With --loop it is to that loop's own list, which
its owner or a person can make. Each change previews its effect on every
loop it applies to and writes only after you answer yes, or with --yes.
--dry-run previews only. --expect binds --yes to a preview's expect value.
These commands run nothing (no agent, model or check), except routes check
--loop, which probes that loop's routes as keepgoing fallbacks check does.

Flags:
`

func routesEditCommand(e env, op string, args []string) int {
	return routesEdit(e, op, args, os.Stdin)
}

func routesEdit(e env, op string, args []string, input io.Reader) int {
	positional, rest := splitPositional(args)
	fs := flag.NewFlagSet("routes "+op, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, routesEditUsage)
		fs.PrintDefaults()
	}
	yes := fs.Bool("yes", false, "write without asking")
	dryRun := fs.Bool("dry-run", false, "preview only; write nothing")
	asJSON := fs.Bool("json", false, "print the versioned route_change object; requires --yes or --dry-run")
	expect := fs.String("expect", "", "with --yes: write only if nothing changed since the preview that gave this value")
	loopName := fs.String("loop", "", "change this registered loop's own list instead of the standard list")
	directory := fs.String("directory", "", "private registry directory (default: user config/keepgoing/registry)")
	to := fs.Int("to", 0, "move: the new 1-based place")
	at := fs.Int("at", 0, "add: the 1-based place (default: last)")
	like := fs.String("like", "", "add: start from route ID of registered loop LOOP, as LOOP:ID")
	var rt config.Route
	fs.StringVar(&rt.ID, "id", "", "add: the new route's id (lowercase letters, digits and hyphens; never reused)")
	fs.StringVar(&rt.Provider, "provider", "", "add: claude-code or codex")
	fs.StringVar(&rt.Model, "model", "", "add: the model")
	fs.StringVar(&rt.Effort, "effort", "", "add: the effort, if any")
	fs.StringVar(&rt.Group, "group", "", "add: the usage limit the route draws on")
	fs.StringVar(&rt.Enter, "enter", "", "add: same_conversation, new_conversation or headless")
	fs.StringVar(&rt.PermissionMode, "permission-mode", "", "add: accept-edits, auto or full, for a new_conversation route")
	fs.StringVar(&rt.AccountDir, "account-dir", "", "add: an existing login directory, for a headless route")
	fs.StringVar(&rt.Probe.Model, "probe-model", "", "add: a cheaper model for scheduled checks")
	fs.StringVar(&rt.Probe.AccountDir, "probe-account-dir", "", "add: an existing login for scheduled checks")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	positional = append(positional, fs.Args()...)
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	usage := map[string]string{
		"add":    "usage: keepgoing routes add --id ID --provider P --model M --group G --enter E [--at N], or --like LOOP:ID",
		"move":   "usage: keepgoing routes move ID --to N [--loop NAME]",
		"remove": "usage: keepgoing routes remove ID [--loop NAME]",
		"off":    "usage: keepgoing routes off ID [--loop NAME]",
		"on":     "usage: keepgoing routes on ID [--loop NAME]",
	}[op]
	addFlags := []string{"at", "like", "id", "provider", "model", "effort", "group", "enter", "permission-mode", "account-dir", "probe-model", "probe-account-dir"}
	switch {
	case op == "add" && len(positional) != 0, op != "add" && len(positional) != 1:
		e.errf("%s", usage)
		return exitUsage
	case op == "add" && *loopName != "":
		e.errf("routes add --loop: add to a loop's own list with keepgoing fallbacks add -c looptender.yaml")
		return exitUsage
	case op != "move" && set["to"]:
		e.errf("routes %s: --to applies only to routes move", op)
		return exitUsage
	case op == "move" && !set["to"]:
		e.errf("%s", usage)
		return exitUsage
	case *asJSON && !*yes && !*dryRun:
		e.errf("routes %s: --json requires --yes or --dry-run", op)
		return exitUsage
	case *expect != "" && !*yes:
		e.errf("routes %s: --expect applies only with --yes", op)
		return exitUsage
	}
	if op != "add" {
		for _, name := range addFlags {
			if set[name] {
				e.errf("routes %s: --%s applies only to routes add", op, name)
				return exitUsage
			}
		}
	}
	req := routeChangeRequest{op: op, to: *to, at: *at, loop: *loopName, expect: *expect, caller: os.Getenv("BB_THREAD_ID"), source: "cli", registry: *directory, now: time.Now().UTC()}
	if op != "add" {
		req.id = positional[0]
	}
	out := func(o routeChangeOut, code int) int {
		if *asJSON {
			b, _ := json.MarshalIndent(o, "", "  ")
			fmt.Fprintln(e.stdout, string(b))
			return code
		}
		printRouteChange(e, o)
		return code
	}
	// Who may make the change is settled before anything is looked up.
	if req.loop == "" && req.caller != "" {
		return out(routeChangeRefused(req, agentIdentityRefusal()), exitUsage)
	}
	if op == "add" {
		if *like != "" {
			req.like = *like
			base, ref := routeLike(*like, req.registry)
			if ref != nil {
				return out(routeChangeRefused(req, ref), exitUsage)
			}
			overrideRoute(&base, rt, set)
			rt = base
		}
		req.route, req.id = rt, rt.ID
		if req.id == "" {
			e.errf("routes add: --id is required (or --like LOOP:ID)")
			return exitUsage
		}
	}
	// A reviewed apply goes straight to the lock, where the plan is made
	// again and compared: a no-op or a refusal there is review_again too.
	if req.expect == "" {
		preview, code := routeChange(req, false)
		if preview.Outcome != "preview" || *dryRun {
			if *dryRun && preview.Outcome == "preview" && !*asJSON {
				printRouteChange(e, preview)
				fmt.Fprintln(e.stdout, "\nDry run: nothing written.")
				return exitOK
			}
			return out(preview, code)
		}
		if !*yes {
			if code, ok := confirmRouteChange(e, preview, input, &req); !ok {
				return code
			}
		}
	}
	if err := e.ctx.Err(); err != nil {
		e.errf("routes %s: %v", op, err)
		return exitFailed
	}
	done, code := routeChange(req, true)
	if !*asJSON && done.Outcome == "changed" {
		fmt.Fprintf(e.stdout, "\n%s\n", routeChangeDone(done))
		if done.List == config.RoutesStandard {
			fmt.Fprintln(e.stdout, "Loops that follow the list use it from their next check.")
		} else {
			fmt.Fprintln(e.stdout, "A running daemon uses the change after its next configuration reload.")
		}
		return code
	}
	return out(done, code)
}

// confirmRouteChange shows the preview and asks; a yes binds the apply to it.
func confirmRouteChange(e env, preview routeChangeOut, input io.Reader, req *routeChangeRequest) (int, bool) {
	printRouteChange(e, preview)
	fmt.Fprintln(e.stdout)
	w := &fallbackAddWizard{e: e, r: bufio.NewReader(input)}
	ok, err := w.askYesNo("Write this change? (yes/no)", "no")
	if err != nil {
		e.errf("routes %s: %v; nothing written", req.op, err)
		return exitUsage, false
	}
	if !ok {
		fmt.Fprintln(e.stdout, "\nCancelled: nothing written.")
		return exitOK, false
	}
	req.expect = preview.Expect
	return exitOK, true
}

// allowedRefusal words a refusal from the ownership and source checks with
// fixed text: never the owner's thread ID or a raw error.
func allowedRefusal(err error) *routeRefusal {
	if strings.HasPrefix(err.Error(), "Ownership:") {
		return refuseRoute("not_owner", "This loop is owned by another BB thread, or its ownership cannot be confirmed; only the owning thread or a person can change its list.")
	}
	return refuseRoute("sources_changed", "The loop's configuration changed since it was loaded; run the command again.")
}

func agentIdentityRefusal() *routeRefusal {
	return refuseRoute("agent_identity", "Only a person can change the standard route list: every loop that follows it uses the change. Run this outside a BB thread.")
}

// routeChange previews a change, or with apply makes it. An apply plans the
// change again under the lock and writes only if the plan is the one the
// caller reviewed, when an expect value is given.
func routeChange(req routeChangeRequest, apply bool) (routeChangeOut, int) {
	if req.loop != "" {
		return ownRouteChange(req, apply)
	}
	if req.caller != "" {
		return routeChangeRefused(req, agentIdentityRefusal()), exitUsage
	}
	path, err := config.StandardRoutesPath()
	if err != nil {
		code := config.RoutesProblem(err)
		return routeChangeRefused(req, refuseRoute(code, "The standard route list cannot be used: %s.", routesProblemText(code))), exitUsage
	}
	if !apply {
		p, ref := planStandardChange(req, path)
		if ref != nil {
			return routeChangeRefused(req, ref), exitUsage
		}
		return p.out, exitOK
	}
	// Before the lock, which is a file beside the list: nothing is created
	// where the list may not be written.
	if ref := routesWriteLocation(path); ref != nil {
		return routeChangeRefused(req, ref), exitUsage
	}
	l, err := lock.AcquireNamed(filepath.Dir(path), "routes")
	if errors.Is(err, lock.ErrHeld) {
		return routeChangeRefused(req, refuseRoute("busy", "Another change to the standard route list is being written; try again in a moment.")), exitSkipped
	}
	if err != nil {
		return routeChangeRefused(req, refuseRoute("unreadable", "The standard route list's lock could not be taken.")), exitFailed
	}
	defer l.Release()
	p, ref := planStandardChange(req, path)
	if ref != nil {
		if req.expect != "" {
			return routeReviewAgain(newRouteChangeOut(req)), exitSkipped
		}
		return routeChangeRefused(req, ref), exitUsage
	}
	if req.expect != "" && req.expect != p.out.Expect {
		return routeReviewAgain(p.out), exitSkipped
	}
	if p.out.Outcome == "unchanged" {
		return p.out, exitOK
	}
	// The new list is written and flushed beside the old one first, then the
	// change is recorded, then the new list is put in place. A failure before
	// the record leaves nothing behind; only a failed rename after it leaves
	// a record of a change that was not made, which keeps its id from being
	// used again, the safe direction.
	staged, err := stageRoutesFile(path, p.next)
	if err != nil {
		return routeChangeRefused(req, refuseRoute("write_failed", "The standard route list could not be written; it is unchanged: %v.", err)), exitFailed
	}
	defer staged.discard()
	if err := appendRouteHistory(filepath.Dir(path), routeHistoryRecord{At: req.now, Op: req.op, Route: req.id, To: p.out.To, Before: p.out.Before, After: p.out.After, By: "person", Source: req.source}); err != nil {
		return routeChangeRefused(req, refuseRoute("history_failed", "The change could not be recorded in %s, so nothing was written.", routesHistoryName)), exitFailed
	}
	if err := staged.commit(); err != nil {
		return routeChangeRefused(req, refuseRoute("write_failed", "The standard route list could not be put in place; it is unchanged, and %s records a change that was not made: %v.", routesHistoryName, err)), exitFailed
	}
	p.out.Outcome = "changed"
	return p.out, exitOK
}

func routeChangeRefused(req routeChangeRequest, ref *routeRefusal) routeChangeOut {
	o := newRouteChangeOut(req)
	o.Outcome, o.Refusal = "refused", ref
	return o
}

func routeReviewAgain(o routeChangeOut) routeChangeOut {
	o.Outcome = "review_again"
	o.Refusal = refuseRoute("review_again", "The list, or a loop it applies to, changed since the preview; nothing was written. Review the change again.")
	return o
}

func newRouteChangeOut(req routeChangeRequest) routeChangeOut {
	list := config.RoutesStandard
	if req.loop != "" {
		list = "own"
	}
	return routeChangeOut{SchemaVersion: 1, Kind: "route_change", Op: req.op, List: list, Loop: req.loop, Route: req.id,
		Before: []string{}, After: []string{}, Routes: []standardRouteRow{}, Loops: []routeChangeLoop{}, Warnings: []string{}}
}

// standardPlan is a planned change to the standard list. Nothing in it has
// been written.
type standardPlan struct {
	next []byte
	out  routeChangeOut
}

func planStandardChange(req routeChangeRequest, path string) (*standardPlan, *routeRefusal) {
	raw, err := config.ReadRoutesFile(path)
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		code := config.RoutesProblem(err)
		return nil, refuseRoute(code, "The standard route list cannot be used: %s.", routesProblemText(code))
	}
	if ref := routesWriteLocation(path); ref != nil {
		return nil, ref
	}
	cur := &config.Routes{Version: 1}
	if !absent {
		if cur, err = config.ParseRoutes(raw); err != nil {
			return nil, refuseRoute(config.RoutesInvalid, "The standard route list is not valid; fix it first (keepgoing routes check shows why).")
		}
	}
	p := &standardPlan{out: newRouteChangeOut(req)}
	o := &p.out
	o.Outcome = "preview"
	for _, rt := range cur.Routes {
		o.Before = append(o.Before, rt.ID)
	}
	at := -1
	for i, rt := range cur.Routes {
		if rt.ID == req.id {
			at = i
		}
	}
	if req.op != "add" && at < 0 {
		known := "The list has no routes."
		if len(o.Before) > 0 {
			known = "The list has: " + strings.Join(o.Before, ", ") + "."
		}
		return nil, refuseRoute("unknown_route", "No standard route has the id %q. %s", bbLabel(req.id, 40), known)
	}
	want := append([]config.Route{}, cur.Routes...)
	byHand := "The standard route list's routes cannot be changed line by line; edit routes.yaml by hand."
	var file *yamlListFile
	if !absent {
		if file, err = listSpans(raw, "routes", byHand, "The standard route list is not valid YAML."); err != nil {
			return nil, refuseRoute("edit_by_hand", "%v", err)
		}
		if len(file.spans) != len(cur.Routes) {
			return nil, refuseRoute("edit_by_hand", "%s", byHand)
		}
	}
	var lines []string
	switch req.op {
	case "add":
		if at >= 0 {
			return nil, refuseRoute("id_taken", "The standard list already has a route with the id %q. Ids are never reused or renamed; pick another.", bbLabel(req.id, 40))
		}
		used, warning, err := routeHistoryIDs(filepath.Dir(path))
		if err != nil {
			return nil, refuseRoute("history_unreadable", "%s cannot be read, so it is unknown whether the id %q was used before: %v.", routesHistoryName, bbLabel(req.id, 40), err)
		}
		if warning != "" {
			o.Warnings = append(o.Warnings, warning)
		}
		if used[req.id] {
			return nil, refuseRoute("id_used_before", "The id %q was used by an earlier route. An id is never reused, so check results kept for the old route cannot count for a new one; pick another id.", bbLabel(req.id, 40))
		}
		if ref := idInLoopRecords(req, o); ref != nil {
			return nil, ref
		}
		if req.like != "" {
			o.Warnings = append(o.Warnings, "--like copied the route's fields only: its environment other than the account variable, its executable and its working directory were not copied; a following loop uses its own")
		}
		if req.at < 0 || req.at > len(cur.Routes)+1 {
			return nil, refuseRoute("bad_position", "--at must be a place between 1 and %d.", len(cur.Routes)+1)
		}
		entry, err := flowRoute(req.route)
		if err != nil {
			return nil, refuseRoute("invalid_route", "%v", err)
		}
		place := len(want)
		if req.at > 0 {
			place = req.at - 1
		}
		want = append(want[:place], append([]config.Route{req.route}, want[place:]...)...)
		o.To = place + 1
		if absent {
			lines = []string{"version: 1\n", "routes:\n", "  - " + entry + "\n"}
		} else {
			lines = file.insertEntry("routes", entry, req.at)
		}
	case "move":
		if req.to < 1 || req.to > len(cur.Routes) {
			return nil, refuseRoute("bad_position", "--to must be a place between 1 and %d.", len(cur.Routes))
		}
		o.From, o.To = at+1, req.to
		if req.to == at+1 {
			o.Outcome, o.Reason = "unchanged", fmt.Sprintf("Route %s is already at place %d.", req.id, req.to)
		} else {
			moved := want[at]
			want = append(want[:at:at], want[at+1:]...)
			want = append(want[:req.to-1], append([]config.Route{moved}, want[req.to-1:]...)...)
			lines, _ = cutPasteEntry(file.lines, file.spans, at, req.to)
		}
	case "remove":
		o.From = at + 1
		want = append(want[:at:at], want[at+1:]...)
		lines, _ = cutPasteEntry(file.lines, file.spans, at, 0)
	case "off", "on":
		o.From = at + 1
		off := req.op == "off"
		if cur.Routes[at].Disabled == off {
			o.Outcome, o.Reason = "unchanged", fmt.Sprintf("Route %s is already %s.", req.id, req.op)
		} else {
			want[at].Disabled = off
			var ok bool
			if lines, ok = file.setDisabled(at, off); !ok {
				return nil, refuseRoute("edit_by_hand", "Route %s cannot be switched %s line by line; edit routes.yaml by hand.", req.id, req.op)
			}
		}
	default:
		return nil, refuseRoute("usage", "unknown change %q", req.op)
	}
	next := raw
	after := cur
	if o.Outcome == "preview" {
		next = []byte(strings.Join(lines, ""))
		after, err = config.ParseRoutes(next)
		if err != nil {
			if req.op == "add" {
				return nil, refuseRoute("invalid_route", "The route is not valid: %s", strings.ReplaceAll(err.Error(), "\n", "; "))
			}
			return nil, refuseRoute("edit_by_hand", "%s", byHand)
		}
		if !sameRoutes(after.Routes, want) || (!absent && (after.Version != cur.Version || !reflect.DeepEqual(after.Redundancy, cur.Redundancy) || !reflect.DeepEqual(after.Probe, cur.Probe))) {
			return nil, refuseRoute("edit_by_hand", "%s", byHand)
		}
		p.next = next
	}
	for _, rt := range after.Routes {
		o.After = append(o.After, rt.ID)
		o.Routes = append(o.Routes, standardRouteRow{ID: rt.ID, Provider: rt.Provider, Model: rt.Model, Effort: rt.Effort, Group: rt.Group, Enter: rt.Enter, ProbeModel: rt.Probe.Model, Disabled: rt.Disabled})
	}
	if o.Outcome == "unchanged" {
		o.Before = append([]string{}, o.After...)
	}
	material := standardLoops(req, cur, after, o)
	h := sha256.New()
	fmt.Fprintf(h, "route_change/1\x00%s\x00%s\x00%d\x00%d\x00", req.op, req.id, req.to, req.at)
	if req.op == "add" {
		b, _ := json.Marshal(req.route)
		h.Write(b)
	}
	if absent {
		fmt.Fprint(h, "\x00absent")
	} else {
		fmt.Fprintf(h, "\x00%x", sha256.Sum256(raw))
	}
	h.Write([]byte(material))
	o.Expect = fmt.Sprintf("%x", h.Sum(nil))
	return p, nil
}

// routesWriteLocation refuses to write the list, its history or its lock
// where they would end up inside a Git checkout, the list naming private
// account directories: through a link at routes.yaml, or a link at any
// directory above it, as GNU Stow makes when it folds a directory. A link
// at routes.yaml must also name a file that exists. Reading the list there
// is still allowed.
func routesWriteLocation(path string) *routeRefusal {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return refuseRoute("dangling_link", "routes.yaml is a symbolic link to a file that does not exist; point it at an existing file or remove the link.")
		}
		if config.InsideCheckout(target) {
			return checkoutRefusal()
		}
	}
	// The directory as it really is: the nearest directory above the list
	// that exists, with every link in it followed.
	dir, rest := filepath.Dir(path), filepath.Base(path)
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			if config.InsideCheckout(filepath.Join(real, rest)) {
				return checkoutRefusal()
			}
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return refuseRoute("unreadable", "The standard route list's directory cannot be resolved.")
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func checkoutRefusal() *routeRefusal {
	return refuseRoute(config.RoutesInsideCheckout, "routes.yaml, or a directory above it, is a symbolic link into a Git checkout. The list is read there, but never written there, because it names private account directories; edit it by hand or move it out of the checkout.")
}

// idInLoopRecords refuses an id that a registered loop following the list
// is on (its saved target, or its record of the route it is on) or still has
// check results, a rejected-handoff count or a session recorded under: a
// route added with that id would take their place. This covers ids removed
// by hand, which the history does not name. A loop with its own list keeps
// records for its own ids; following the list clears them. A loop whose
// configuration cannot be loaded is checked through the state directory
// the registry records for it, and refuses the id if that cannot be read.
func idInLoopRecords(req routeChangeRequest, o *routeChangeOut) *routeRefusal {
	dir, err := registryDirectory(req.registry)
	var reg registry.File
	if err == nil {
		reg, err = registry.Load(dir)
	}
	if err != nil {
		return refuseRoute("registry_unavailable", "The loop registry cannot be read, so it is unknown whether a loop has records under the id %q.", bbLabel(req.id, 40))
	}
	for _, entry := range reg.Entries {
		stateDir, name := entry.StateDir, entry.Loop
		if cfg, err := config.Load(entry.ConfigPath); err == nil {
			if !cfg.Loop.FollowsStandard() {
				continue
			}
			stateDir, name = cfg.Loop.StateDir, cfg.Loop.Name
		}
		found, err := loopRecordsUnder(stateDir, req.id)
		if err != nil {
			return refuseRoute("loop_records_unreadable", "Loop %s's records cannot be read, so it is unknown whether it has any under the id %q.", name, bbLabel(req.id, 40))
		}
		if len(found) > 0 {
			return refuseRoute("id_in_loop_records", "Loop %s still has %s under the id %q, which a new route with that id would take the place of; pick another id.", name, strings.Join(found, ", "), bbLabel(req.id, 40))
		}
	}
	return nil
}

// loopRecordsUnder lists what a loop's state directory holds under id: the
// target it is on, the record of the route it is on, check results, a
// rejected-handoff count and a session.
func loopRecordsUnder(stateDir, id string) ([]string, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, errors.New("no state directory")
	}
	var found []string
	st, err := state.Load(stateDir)
	if err != nil {
		return nil, err
	}
	if st.Target() == id {
		found = append(found, "its current route")
	}
	if rt, ok := supervise.LoadCurrentRoute(stateDir); ok && rt.ID == id {
		found = append(found, "the record of the route it is on")
	}
	reg, err := health.Load(stateDir)
	if err != nil {
		return nil, err
	}
	if _, ok := reg.Candidates[id]; ok {
		found = append(found, "check results")
	}
	if _, ok := st.HandoffBriefs[id]; ok {
		found = append(found, "a rejected-handoff count")
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "targets", id)); err == nil {
		found = append(found, "a session")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return found, nil
}

func sameRoutes(got, want []config.Route) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			return false
		}
	}
	return true
}

// flowRoute is a route as one line of YAML: a flow mapping.
func flowRoute(rt config.Route) (string, error) {
	var n yaml.Node
	if err := n.Encode(rt); err != nil {
		return "", errors.New("the route cannot be written as YAML")
	}
	var flow func(*yaml.Node)
	flow = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
			n.Style = yaml.FlowStyle
		}
		for _, c := range n.Content {
			flow(c)
		}
	}
	flow(&n)
	b, err := yaml.Marshal(&n)
	line := strings.TrimSuffix(string(b), "\n")
	if err != nil || strings.ContainsAny(line, "\n\r") {
		return "", errors.New("the route cannot be written on one line; add it to routes.yaml by hand")
	}
	return line, nil
}

// standardLoops fills in the effect on each registered loop that follows
// the standard list, and returns what of them an apply must find unchanged.
func standardLoops(req routeChangeRequest, before, after *config.Routes, o *routeChangeOut) string {
	var material strings.Builder
	dir, err := registryDirectory(req.registry)
	var reg registry.File
	if err == nil {
		reg, err = registry.Load(dir)
	}
	if err != nil {
		o.Warnings = append(o.Warnings, "the loop registry could not be read, so the effect on loops is not shown")
		material.WriteString("\x00registry unavailable")
		return material.String()
	}
	for _, entry := range reg.Entries {
		cfg, err := config.Load(entry.ConfigPath)
		if err != nil {
			o.Loops = append(o.Loops, routeChangeLoop{Loop: entry.Loop, Problem: "configuration_unavailable", Before: []string{}, After: []string{}, NewlySkipped: []skippedRoute{}})
			fmt.Fprintf(&material, "\x00%q unavailable", entry.Loop)
			continue
		}
		if !cfg.Loop.FollowsStandard() {
			continue
		}
		st, _ := state.Load(cfg.Loop.StateDir)
		v := routeChangeLoop{Loop: cfg.Loop.Name, Current: st.Target(), NewlySkipped: []skippedRoute{}}
		fbBefore, skippedBefore := before.Resolve(cfg.Loop)
		fbAfter, skippedAfter := after.Resolve(cfg.Loop)
		v.Before, v.After = candidateIDs(fbBefore), candidateIDs(fbAfter)
		wasSkipped := map[string]bool{}
		for _, s := range skippedBefore {
			wasSkipped[s.ID] = true
		}
		for _, s := range skippedAfter {
			if !wasSkipped[s.ID] {
				v.NewlySkipped = append(v.NewlySkipped, skippedRoute{ID: s.ID, Reason: s.Reason})
			}
		}
		v.CurrentLeavesList = v.Current != state.Primary && slices.Contains(v.Before, v.Current) && !slices.Contains(v.After, v.Current)
		v.NoRouteApplies = len(v.After) == 0
		v.BelowMinHealthy = belowMinHealthy(cfg, fbAfter, after.Redundancy.MinHealthy, v.Current, req.now)
		o.Loops = append(o.Loops, v)
		fmt.Fprintf(&material, "\x00%q %s %q", cfg.Loop.Name, cfg.SourcesDigest(), v.Current)
	}
	return material.String()
}

func candidateIDs(fb *config.Fallbacks) []string {
	ids := []string{}
	if fb != nil {
		for _, c := range fb.Candidates {
			if !c.Disabled {
				ids = append(ids, c.ID)
			}
		}
	}
	return ids
}

// belowMinHealthy says whether, by its recorded check results, the loop would
// have fewer distinct healthy routes than min asks for. Without results it
// says nothing.
func belowMinHealthy(cfg *config.Config, fb *config.Fallbacks, min int, current string, now time.Time) bool {
	if min <= 0 {
		return false
	}
	if fb == nil {
		return true
	}
	reg, err := health.Load(cfg.Loop.StateDir)
	if err != nil {
		return false
	}
	group := cfg.Loop.Group
	if c, ok := fb.Find(current); ok {
		group = c.Group
	}
	s := health.Evaluate(fb, reg, now, current, group)
	return s.Distinct < s.Min
}

// routeLike reads route ID of a registered loop as a standard route. A route
// that enters another conversation than the loop's own has no standard form.
func routeLike(like, dir string) (config.Route, *routeRefusal) {
	name, id, ok := strings.Cut(like, ":")
	if !ok || name == "" || id == "" {
		return config.Route{}, refuseRoute("usage", "--like takes LOOP:ID, a registered loop and one of its routes")
	}
	cfg, ref := registeredLoop(name, dir)
	if ref != nil {
		return config.Route{}, ref
	}
	fb := currentRoutes(cfg)
	c, found := fb.Find(id)
	if !found {
		return config.Route{}, refuseRoute("unknown_route", "Loop %s has no route with the id %q.", name, bbLabel(id, 40))
	}
	if c.BB != nil && !c.BB.NewThread && (cfg.Loop.BB == nil || c.BB.Thread != cfg.Loop.BB.Thread) {
		return config.Route{}, refuseRoute("like_unavailable", "Route %s of loop %s names another conversation, which a standard route cannot; describe the route with flags instead.", id, name)
	}
	rt, ok := config.RouteOf(cfg.Loop, c)
	if !ok || (rt.Provider != "claude-code" && rt.Provider != "codex") {
		return config.Route{}, refuseRoute("like_unavailable", "Route %s of loop %s cannot be described as a standard route; describe it with flags instead.", id, name)
	}
	rt.Disabled = false
	return rt, nil
}

// overrideRoute applies the flags given on the command line over a route
// read with --like.
func overrideRoute(base *config.Route, flags config.Route, set map[string]bool) {
	for name, apply := range map[string]func(){
		"id":                func() { base.ID = flags.ID },
		"provider":          func() { base.Provider = flags.Provider },
		"model":             func() { base.Model = flags.Model },
		"effort":            func() { base.Effort = flags.Effort },
		"group":             func() { base.Group = flags.Group },
		"enter":             func() { base.Enter = flags.Enter },
		"permission-mode":   func() { base.PermissionMode = flags.PermissionMode },
		"account-dir":       func() { base.AccountDir = flags.AccountDir },
		"probe-model":       func() { base.Probe.Model = flags.Probe.Model },
		"probe-account-dir": func() { base.Probe.AccountDir = flags.Probe.AccountDir },
	} {
		if set[name] {
			apply()
		}
	}
}

func registeredLoop(name, dir string) (*config.Config, *routeRefusal) {
	d, err := registryDirectory(dir)
	if err == nil {
		var f registry.File
		if f, err = registry.Load(d); err == nil {
			return findRegisteredLoop(f, name)
		}
	}
	return nil, refuseRoute("registry_unavailable", "The loop registry cannot be read.")
}

func findRegisteredLoop(f registry.File, name string) (*config.Config, *routeRefusal) {
	for _, entry := range f.Entries {
		if entry.Loop == name {
			cfg, err := config.Load(entry.ConfigPath)
			if err != nil {
				return nil, refuseRoute("configuration_unavailable", "Loop %s's configuration cannot be loaded; inspect it with keepgoing validate.", name)
			}
			return cfg, nil
		}
	}
	return nil, refuseRoute("unknown_loop", "No registered loop is named %q.", bbLabel(name, 40))
}

// stagedRoutes is a new list written and flushed beside the file it will
// replace, not yet in place.
type stagedRoutes struct{ tmp, target string }

// stageRoutesFile writes raw, readable only by this user, beside the list it
// will replace. When the path is a symbolic link, that is the file it names,
// so the link is kept, as a dotfile manager expects. The plan has already
// refused a dangling link and one into a Git checkout.
func stageRoutesFile(path string, raw []byte) (*stagedRoutes, error) {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if target, err = filepath.EvalSymlinks(path); err != nil {
			return nil, errors.New("the symbolic link to it cannot be followed")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, errors.New("its directory cannot be created")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".routes-*")
	if err != nil {
		return nil, errors.New("a temporary file cannot be created beside it")
	}
	name := tmp.Name()
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(raw); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(name)
		return nil, errors.New("the new contents cannot be written")
	}
	return &stagedRoutes{tmp: name, target: target}, nil
}

// commit puts the staged list in place.
func (s *stagedRoutes) commit() error {
	if err := snapshot.Replace(s.tmp, s.target); err != nil {
		return errors.New("the new contents cannot be put in place")
	}
	s.tmp = ""
	if d, err := os.Open(filepath.Dir(s.target)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// discard removes a staged list that was not put in place.
func (s *stagedRoutes) discard() {
	if s.tmp != "" {
		os.Remove(s.tmp)
	}
}

// routesHistoryName is the record of every change made with these commands,
// kept beside routes.yaml. It is also what keeps an id from being reused.
const routesHistoryName = "routes-history.jsonl"

const routesHistoryLimit = 16 << 20

type routeHistoryRecord struct {
	At     time.Time `json:"at"`
	Op     string    `json:"op"`
	Route  string    `json:"route"`
	To     int       `json:"to,omitempty"`
	Before []string  `json:"before"`
	After  []string  `json:"after"`
	By     string    `json:"by"`     // person
	Source string    `json:"source"` // cli
}

func openRouteHistory(dir string) (*os.File, error) {
	path := filepath.Join(dir, routesHistoryName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, errors.New("it cannot be inspected")
	}
	if !config.PrivateFile(info) {
		return nil, errors.New("it must be a regular file owned by this user and readable by no one else (chmod 600)")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, errors.New("it cannot be opened")
	}
	return f, nil
}

// The ids a record cut short still names: its route, and its before and
// after lists as far as they got.
var (
	historyRoutePattern = regexp.MustCompile(`"route"\s*:\s*"([a-z0-9][a-z0-9-]*)"`)
	historyListPattern  = regexp.MustCompile(`"(?:before|after)"\s*:\s*\[([^\]]*)`)
	historyQuoted       = regexp.MustCompile(`"([a-z0-9][a-z0-9-]*)"`)
)

// historyRecovery is what to do about a history that cannot be used.
const historyRecovery = "move routes-history.jsonl aside; add then refuses only ids still in the list or in a loop's records"

// routeHistoryIDs is every id the history names, and a warning when a
// record in it was cut short (a write that did not finish), which is skipped.
// Any other history that cannot be read fails the check: an id is never
// assumed to be new.
func routeHistoryIDs(dir string) (map[string]bool, string, error) {
	ids := map[string]bool{}
	f, err := openRouteHistory(dir)
	if errors.Is(err, os.ErrNotExist) {
		return ids, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, routesHistoryLimit+1))
	if err != nil {
		return nil, "", errors.New("it could not be read")
	}
	if len(raw) > routesHistoryLimit {
		return nil, "", errors.New("it is larger than 16 MiB; " + historyRecovery)
	}
	warning := ""
	lines := bytes.Split(raw, []byte("\n"))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec routeHistoryRecord
		dec := json.NewDecoder(bytes.NewReader(line))
		err := dec.Decode(&rec)
		if err == nil && len(bytes.TrimSpace(line[dec.InputOffset():])) != 0 {
			// A record followed by anything else on its line is damaged.
			return nil, "", fmt.Errorf("line %d is not a history record; %s", i+1, historyRecovery)
		}
		if err != nil {
			// A record cut short by a write that did not finish (the next
			// append starts a new line after it) is skipped; any id it got
			// as far as naming still counts. Anything else is refused.
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) && errors.Is(err, io.ErrUnexpectedEOF) {
				for _, m := range historyRoutePattern.FindAllSubmatch(line, -1) {
					ids[string(m[1])] = true
				}
				for _, list := range historyListPattern.FindAllSubmatch(line, -1) {
					for _, m := range historyQuoted.FindAllSubmatch(list[1], -1) {
						ids[string(m[1])] = true
					}
				}
				warning = routesHistoryName + " holds a record cut short by a write that did not finish; it was skipped"
				continue
			}
			return nil, "", fmt.Errorf("line %d is not a history record; %s", i+1, historyRecovery)
		}
		ids[rec.Route] = true
		for _, id := range append(rec.Before, rec.After...) {
			ids[id] = true
		}
	}
	return ids, warning, nil
}

// appendRouteHistory adds one record on a line of its own, starting a new
// line first if the last write did not finish one.
func appendRouteHistory(dir string, rec routeHistoryRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := openRouteHistory(dir)
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.OpenFile(filepath.Join(dir, routesHistoryName), os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return errors.New("it cannot be opened")
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err != nil {
			return errors.New("it could not be read")
		}
		if last[0] != '\n' {
			line = append([]byte("\n"), line...)
		}
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return errors.New("it could not be written")
	}
	if err := f.Sync(); err != nil {
		return errors.New("it could not be flushed")
	}
	return nil
}

// ownRouteChange changes a registered loop's own list through the same
// plans as keepgoing fallbacks move and remove, with off and on beside them.
func ownRouteChange(req routeChangeRequest, apply bool) (routeChangeOut, int) {
	cfg, ref := registeredLoop(req.loop, req.registry)
	if ref != nil {
		return routeChangeRefused(req, ref), exitUsage
	}
	switch {
	case cfg.Loop.FollowsStandard():
		return routeChangeRefused(req, refuseRoute("follows_standard", "Loop %s follows the standard route list; change that list (leave out --loop) or give the loop its own list.", req.loop)), exitUsage
	case cfg.FallbackSet == nil:
		return routeChangeRefused(req, refuseRoute("no_list", "Loop %s has no other routes. Add one with: keepgoing fallbacks add", req.loop)), exitUsage
	}
	if info, err := os.Lstat(cfg.Loop.Fallbacks); err != nil || !info.Mode().IsRegular() {
		return routeChangeRefused(req, refuseRoute("edit_by_hand", "Loop %s's fallbacks file is a symbolic link or not a regular file.", req.loop)), exitUsage
	}
	if ref := ownListAuthority(cfg.Loop.StateDir, req.caller); ref != nil {
		return routeChangeRefused(req, ref), exitUsage
	}
	if code, err := fallbackAddAllowed(cfg); err != nil {
		return routeChangeRefused(req, allowedRefusal(err)), code
	}
	p, out, ref := planOwnChange(req, cfg)
	if ref != nil {
		if apply && req.expect != "" {
			return routeReviewAgain(newRouteChangeOut(req)), exitSkipped
		}
		return routeChangeRefused(req, ref), exitUsage
	}
	if apply && req.expect != "" && req.expect != out.Expect {
		return routeReviewAgain(out), exitSkipped
	}
	if !apply || out.Outcome == "unchanged" {
		return out, exitOK
	}
	reviewed := req.expect
	if reviewed == "" {
		reviewed = out.Expect
	}
	errReview := errors.New("review again")
	inner := p.recheck
	p.recheck = func() error {
		if inner != nil {
			if err := inner(); err != nil {
				return err
			}
		}
		fresh, err := config.Load(cfg.Path())
		if err != nil {
			return errReview
		}
		if _, now, ref := planOwnChange(req, fresh); ref != nil || now.Expect != reviewed {
			return errReview
		}
		return nil
	}
	if code, err := writeFallbackEdit(p); err != nil {
		if errors.Is(err, errReview) {
			return routeReviewAgain(out), exitSkipped
		}
		return routeChangeRefused(req, refuseRoute("write_failed", "%v", err)), code
	}
	if p.historyFailed {
		out.Warnings = append(out.Warnings, "the change was saved, but could not be recorded in the loop's history (keepgoing timeline)")
	}
	out.Outcome = "changed"
	return out, exitOK
}

// ownListAuthority is who may change a loop's own list: a person, or the BB
// thread that owns the loop. An agent may not change the list of a loop
// with no recorded owner, as it may not change the standard list.
func ownListAuthority(stateDir, caller string) *routeRefusal {
	if caller == "" {
		return nil
	}
	// Another thread's loop, and ownership that cannot be read, are refused
	// by the ownership check every change makes (fallbackAddAllowed).
	if f, err := control.Load(stateDir); err == nil && f.Ownership == nil {
		return refuseRoute("agent_identity", "This loop has no recorded owner, so only a person can change its list. Run this outside a BB thread, or from the thread that owns the loop.")
	}
	return nil
}

// ownListCaller is who made a change to a loop's own list, as a role.
func ownListCaller(caller string) string {
	if caller == "" {
		return "terminal"
	}
	return "owner_thread"
}

func planOwnChange(req routeChangeRequest, cfg *config.Config) (*fallbackEdit, routeChangeOut, *routeRefusal) {
	out := newRouteChangeOut(req)
	out.Outcome = "preview"
	var p *fallbackEdit
	var err error
	switch req.op {
	case "move":
		p, err = planFallbackMove(cfg, req.id, req.to)
	case "remove":
		p, err = planFallbackRemove(cfg, req.id)
		if err == nil && p.cleanupOnly {
			err = fmt.Errorf("No fallback has the id %q; to clear what it left behind, use keepgoing fallbacks remove", bbLabel(req.id, 40))
		}
	case "off", "on":
		p, err = planFallbackToggle(cfg, req.id, req.op == "off")
	default:
		err = fmt.Errorf("unknown change %q", req.op)
	}
	if err != nil {
		return nil, out, refuseRoute("not_possible", "%v", err)
	}
	if p.unchanged != "" {
		out.Outcome, out.Reason = "unchanged", p.unchanged
	}
	out.From, out.To = p.from, p.to
	for _, c := range cfg.FallbackSet.Candidates {
		out.Before = append(out.Before, c.ID)
	}
	l := cfg.Loop
	for _, c := range p.order {
		out.After = append(out.After, c.ID)
		out.Routes = append(out.Routes, standardRouteRow{ID: c.ID, Provider: c.Provider, Model: c.Model(), Effort: agentEffort(c.Agent), Group: c.Group, Enter: routeEnter(l, c), ProbeModel: c.Probe.Model, Disabled: c.Disabled})
	}
	if out.Outcome == "unchanged" {
		out.Before = append([]string{}, out.After...)
	}
	after := &config.Fallbacks{Version: 1, Redundancy: cfg.FallbackSet.Redundancy, Probe: cfg.FallbackSet.Probe, Candidates: p.order}
	v := routeChangeLoop{Loop: l.Name, Current: p.active, Before: candidateIDs(cfg.FallbackSet), After: candidateIDs(after), NewlySkipped: []skippedRoute{}}
	v.NoRouteApplies = len(v.After) == 0
	v.BelowMinHealthy = belowMinHealthy(cfg, after, after.Redundancy.MinHealthy, v.Current, req.now)
	out.Loops = append(out.Loops, v)
	raw, err := os.ReadFile(l.Fallbacks)
	if err != nil {
		return nil, out, refuseRoute("unreadable", "Loop %s's fallbacks file cannot be read.", req.loop)
	}
	h := sha256.New()
	fmt.Fprintf(h, "route_change/1\x00own\x00%s\x00%s\x00%s\x00%d\x00%x\x00%s\x00%q", req.loop, req.op, req.id, req.to, sha256.Sum256(raw), cfg.SourcesDigest(), p.active)
	out.Expect = fmt.Sprintf("%x", h.Sum(nil))
	return p, out, nil
}

// planFallbackToggle switches one fallback off or on by changing only its
// disabled setting. The fallback the loop is on cannot be switched off.
func planFallbackToggle(cfg *config.Config, id string, off bool) (*fallbackEdit, error) {
	p, err := newFallbackEdit("fallback_toggle", cfg)
	if err != nil {
		return nil, err
	}
	at, err := fallbackEditFind(cfg, id)
	if err != nil {
		return nil, err
	}
	word := map[bool]string{true: "off", false: "on"}[off]
	p.subject, p.from = id, at+1
	if p.order[at].Disabled == off {
		p.unchanged = fmt.Sprintf("Fallback %s is already %s.", id, word)
		return p, nil
	}
	if off && p.active == id {
		return nil, fmt.Errorf("The loop is running on %q right now. Move it first with: keepgoing retarget primary", id)
	}
	raw, err := os.ReadFile(cfg.Loop.Fallbacks)
	if err != nil {
		return nil, errors.New("The fallbacks file is unreadable.")
	}
	byHand := "The fallbacks file's candidates list cannot be edited line by line;\n  edit it by hand."
	f, err := listSpans(raw, "candidates", byHand, "The fallbacks file is not valid YAML.")
	if err != nil {
		return nil, err
	}
	if len(f.spans) != len(p.order) {
		return nil, errors.New("The fallbacks file changed while it was being read; run the command again.")
	}
	lines, ok := f.setDisabled(at, off)
	if !ok {
		return nil, errors.New(byHand)
	}
	p.fallbacksYAML = []byte(strings.Join(lines, ""))
	p.order = append([]config.Candidate{}, p.order...)
	p.order[at].Disabled = off
	parsed, err := config.ParseFallbacks(p.fallbacksYAML)
	before, _ := config.ParseFallbacks(raw)
	if err != nil || before == nil || len(parsed.Candidates) != len(before.Candidates) ||
		!reflect.DeepEqual(parsed.Redundancy, before.Redundancy) || !reflect.DeepEqual(parsed.Probe, before.Probe) {
		return nil, errors.New(byHand)
	}
	for i, c := range parsed.Candidates {
		want := before.Candidates[i]
		if i == at {
			want.Disabled = off
		}
		if !reflect.DeepEqual(c, want) {
			return nil, errors.New(byHand)
		}
	}
	if off {
		p.recheck = func() error {
			st, err := state.Load(cfg.Loop.StateDir)
			if err != nil {
				return errors.New("the loop's saved state is unreadable, so its current target is unknown; nothing written")
			}
			if st.Target() == id {
				return errors.New("the loop moved to this fallback since the preview; nothing written")
			}
			return nil
		}
	}
	p.confirm, p.done = fmt.Sprintf("Switch this fallback %s?", word), fmt.Sprintf("Switched fallback %s %s.", id, word)
	return p, nil
}

func routeChangeDone(o routeChangeOut) string {
	list := "the standard route list"
	if o.List == "own" {
		list = "loop " + o.Loop + "'s own list"
	}
	switch o.Op {
	case "add":
		return fmt.Sprintf("Added route %s to %s at place %d.", o.Route, list, o.To)
	case "move":
		return fmt.Sprintf("Moved route %s to place %d of %d in %s.", o.Route, o.To, len(o.After), list)
	case "remove":
		return fmt.Sprintf("Removed route %s from %s. Its id is not used again.", o.Route, list)
	}
	return fmt.Sprintf("Switched route %s %s in %s.", o.Route, o.Op, list)
}

func printRouteChange(e env, o routeChangeOut) {
	w := e.stdout
	switch o.Outcome {
	case "refused", "review_again":
		e.errf("routes %s: refused, nothing written.\n  %s", o.Op, o.Refusal.Text)
		return
	case "unchanged":
		fmt.Fprintf(w, "%s Nothing was changed.\n", o.Reason)
		return
	case "changed":
		fmt.Fprintln(w, routeChangeDone(o))
		return
	}
	list := "Standard route list"
	if o.List == "own" {
		list = "Loop " + o.Loop + "'s own list"
	}
	switch o.Op {
	case "add":
		fmt.Fprintf(w, "%s: add route %s at place %d.\n", list, o.Route, o.To)
	case "move":
		fmt.Fprintf(w, "%s: move route %s from place %d to %d.\n", list, o.Route, o.From, o.To)
	case "remove":
		fmt.Fprintf(w, "%s: remove route %s (place %d). Its id will not be used again.\n", list, o.Route, o.From)
	default:
		fmt.Fprintf(w, "%s: switch route %s %s.\n", list, o.Route, o.Op)
	}
	fmt.Fprintf(w, "  before: %s\n  after:  %s\n", idList(o.Before), idList(o.After))
	if o.List == config.RoutesStandard {
		if len(o.Loops) == 0 {
			fmt.Fprintln(w, "No registered loop follows the standard list.")
		} else {
			fmt.Fprintln(w, "Loops that follow it:")
		}
	}
	for _, l := range o.Loops {
		if l.Problem != "" {
			fmt.Fprintf(w, "  %s: configuration unavailable; the effect is not shown.\n", l.Loop)
			continue
		}
		fmt.Fprintf(w, "  %s (on %s): hands off to %s -> %s\n", l.Loop, l.Current, idList(l.Before), idList(l.After))
		for _, s := range l.NewlySkipped {
			fmt.Fprintf(w, "    not used here: %s (%s)\n", s.ID, s.Reason)
		}
		if l.CurrentLeavesList {
			fmt.Fprintf(w, "    note: the loop is on %s, which leaves its routes; it stays there until the ordinary rules move it\n", l.Current)
		}
		if l.NoRouteApplies {
			fmt.Fprintln(w, "    note: no route is left to hand off to")
		} else if l.BelowMinHealthy {
			fmt.Fprintln(w, "    note: fewer healthy routes than redundancy.min_healthy by the last checks; handoffs would be refused until more are healthy")
		}
	}
	for _, warning := range o.Warnings {
		fmt.Fprintf(w, "note: %s\n", warning)
	}
}

func idList(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}
