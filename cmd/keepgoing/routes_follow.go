package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Switching a loop between the standard list and a list of its own.
//
// follow-standard replaces loop.fallbacks with loop.routes: standard (or adds
// it to a loop with no other routes). The loop's fallbacks file is kept and
// no longer read. Check results and sessions recorded under the ids the
// standard list gives the loop are cleared first, so nothing recorded for a
// route of the loop's own can count for a standard route that shares its id.
//
// own copies the routes the standard list resolves to for this loop into a
// new fallbacks file beside the configuration file, exactly as resolved, and points
// loop.fallbacks at it. The loop's recorded check results stay: they are for
// the same routes.
//
// Either way only the configuration file's routes lines change, and the loop must be
// on its first route, or for own on a route the list still holds.

const routesFollowUsage = `Switch a loop between the standard route list and a list of its own.

Usage:
  keepgoing routes follow-standard (--loop NAME | -c looptender.yaml)
  keepgoing routes own (--loop NAME | -c looptender.yaml) [--file fallbacks.yaml]

Each command previews the change and writes only after you answer yes, or
with --yes. --dry-run previews only; --expect binds --yes to a preview.

Flags:
`

func routesFollowCommand(e env, op string, args []string) int {
	return routesFollow(e, op, args, os.Stdin)
}

func routesFollow(e env, op string, args []string, input io.Reader) int {
	fs := flag.NewFlagSet("routes "+op, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		fmt.Fprint(e.stderr, routesFollowUsage)
		fs.PrintDefaults()
	}
	cfgPath := fs.String("c", "", "the loop's config file")
	loopName := fs.String("loop", "", "a registered loop, by name")
	directory := fs.String("directory", "", "private registry directory (default: user config/keepgoing/registry)")
	yes := fs.Bool("yes", false, "write without asking")
	dryRun := fs.Bool("dry-run", false, "preview only; write nothing")
	asJSON := fs.Bool("json", false, "print the versioned route_change object; requires --yes or --dry-run")
	expect := fs.String("expect", "", "with --yes: write only if nothing changed since the preview that gave this value")
	file := fs.String("file", "fallbacks.yaml", "own: the new fallbacks file, beside the configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case fs.NArg() != 0 || (*cfgPath == "") == (*loopName == ""):
		e.errf("usage: keepgoing routes %s (--loop NAME | -c looptender.yaml)", op)
		return exitUsage
	case op != "own" && set["file"]:
		e.errf("routes %s: --file applies only to routes own", op)
		return exitUsage
	case *asJSON && !*yes && !*dryRun:
		e.errf("routes %s: --json requires --yes or --dry-run", op)
		return exitUsage
	case *expect != "" && !*yes:
		e.errf("routes %s: --expect applies only with --yes", op)
		return exitUsage
	}
	req := routeChangeRequest{op: strings.ReplaceAll(op, "-", "_"), loop: *loopName, expect: *expect, caller: os.Getenv("BB_THREAD_ID"), source: "cli", registry: *directory, now: time.Now().UTC()}
	path := *cfgPath
	if path != "" {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			e.errf("routes %s: refused, nothing written.\n  The config path is a symbolic link. Pass the real configuration file instead.", op)
			return exitUsage
		}
	}
	out := func(o routeChangeOut, code int) int {
		if *asJSON {
			b, _ := json.MarshalIndent(o, "", "  ")
			fmt.Fprintln(e.stdout, string(b))
			return code
		}
		printRouteSource(e, o)
		return code
	}
	// A reviewed apply goes straight to the lock, where the plan is made
	// again and compared: a no-op or a refusal there is review_again too.
	if req.expect == "" {
		preview, code := routeSourceChange(req, path, *file, false)
		if preview.Outcome != "preview" || *dryRun {
			if *dryRun && preview.Outcome == "preview" && !*asJSON {
				printRouteSource(e, preview)
				fmt.Fprintln(e.stdout, "\nDry run: nothing written.")
				return exitOK
			}
			return out(preview, code)
		}
		if !*yes {
			printRouteSource(e, preview)
			fmt.Fprintln(e.stdout)
			w := &fallbackAddWizard{e: e, r: bufio.NewReader(input)}
			ok, err := w.askYesNo("Write this change? (yes/no)", "no")
			if err != nil {
				e.errf("routes %s: %v; nothing written", op, err)
				return exitUsage
			}
			if !ok {
				fmt.Fprintln(e.stdout, "\nCancelled: nothing written.")
				return exitOK
			}
			req.expect = preview.Expect
		}
	}
	if err := e.ctx.Err(); err != nil {
		e.errf("routes %s: %v", op, err)
		return exitFailed
	}
	done, code := routeSourceChange(req, path, *file, true)
	if !*asJSON && done.Outcome == "changed" {
		fmt.Fprintf(e.stdout, "\n%s\nA running daemon uses the change after its next configuration reload.\n", routeSourceDone(done))
		return code
	}
	return out(done, code)
}

// sourcePlan is a planned switch. Nothing in it has been written.
type sourcePlan struct {
	cfg           *config.Config
	configYAML    []byte
	fallbacksPath string // own: the new file
	fallbacksYAML []byte
	clear         []string // follow-standard: ids whose records are cleared
	out           routeChangeOut
}

func routeSourceChange(req routeChangeRequest, path, file string, apply bool) (routeChangeOut, int) {
	var cfg *config.Config
	if path == "" {
		var ref *routeRefusal
		if cfg, ref = registeredLoop(req.loop, req.registry); ref != nil {
			return routeChangeRefused(req, ref), exitUsage
		}
	} else {
		var err error
		if cfg, err = config.Load(path); err != nil {
			return routeChangeRefused(req, refuseRoute("configuration_unavailable", "The configuration cannot be loaded; inspect it with keepgoing validate.")), exitUsage
		}
		req.loop = cfg.Loop.Name
	}
	if info, err := os.Lstat(cfg.Path()); err != nil || !info.Mode().IsRegular() {
		return routeChangeRefused(req, refuseRoute("edit_by_hand", "The configuration file is a symbolic link or not a regular file.")), exitUsage
	}
	if ref := ownListAuthority(cfg.Loop.StateDir, req.caller); ref != nil {
		return routeChangeRefused(req, ref), exitUsage
	}
	if code, err := fallbackAddAllowed(cfg); err != nil {
		return routeChangeRefused(req, allowedRefusal(err)), code
	}
	p, ref := planRouteSource(req, cfg, file)
	if ref != nil {
		if apply && req.expect != "" {
			return routeReviewAgain(newRouteChangeOut(req)), exitSkipped
		}
		return routeChangeRefused(req, ref), exitUsage
	}
	if apply && req.expect != "" && req.expect != p.out.Expect {
		return routeReviewAgain(p.out), exitSkipped
	}
	if !apply || p.out.Outcome == "unchanged" {
		return p.out, exitOK
	}
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		return routeChangeRefused(req, refuseRoute("busy", "A check is running; nothing written. Try again in a moment.")), exitSkipped
	}
	if err != nil {
		return routeChangeRefused(req, refuseRoute("busy", "The loop's lock could not be taken; nothing written.")), exitFailed
	}
	defer l.Release()
	if ref := ownListAuthority(cfg.Loop.StateDir, req.caller); ref != nil {
		return routeChangeRefused(req, ref), exitUsage
	}
	if code, err := fallbackAddAllowed(cfg); err != nil {
		if ref := allowedRefusal(err); ref.Code == "not_owner" {
			return routeChangeRefused(req, ref), code
		}
		return routeReviewAgain(p.out), exitSkipped
	}
	// Under the lock the plan is made again from what is on disk now, and
	// must be the one reviewed.
	fresh, err := config.Load(cfg.Path())
	if err != nil {
		return routeReviewAgain(p.out), exitSkipped
	}
	again, ref := planRouteSource(req, fresh, file)
	reviewed := req.expect
	if reviewed == "" {
		reviewed = p.out.Expect
	}
	if ref != nil || again.out.Expect != reviewed || again.out.Outcome != "preview" {
		return routeReviewAgain(p.out), exitSkipped
	}
	if err := writeRouteSource(again); err != nil {
		return routeChangeRefused(req, refuseRoute("write_failed", "%v", err)), exitFailed
	}
	recorded := recordRouteChange(cfg.Loop.StateDir, req.op, "", req.caller) == nil
	if again.out.FailoverOn != nil && recorded {
		recorded = recordRouteChange(cfg.Loop.StateDir, "handoff_on", "", req.caller) == nil
	}
	if !recorded {
		again.out.Warnings = append(again.out.Warnings, "the change was saved, but could not be recorded in the loop's history (keepgoing timeline)")
	}
	again.out.Outcome = "changed"
	return again.out, exitOK
}

func planRouteSource(req routeChangeRequest, cfg *config.Config, file string) (*sourcePlan, *routeRefusal) {
	p := &sourcePlan{cfg: cfg, out: newRouteChangeOut(req)}
	o := &p.out
	o.Outcome, o.Loop, o.Route = "preview", cfg.Loop.Name, ""
	l := cfg.Loop
	st, err := state.Load(l.StateDir)
	if err != nil {
		return nil, refuseRoute("not_possible", "The loop's saved state is unreadable, so its current route is unknown.")
	}
	current := st.Target()
	raw, err := os.ReadFile(cfg.Path())
	if err != nil {
		return nil, refuseRoute("configuration_unavailable", "The configuration file cannot be read.")
	}
	o.Before = candidateIDs(cfg.FallbackSet)
	var after *config.Fallbacks
	var skipped []config.RouteSkip
	var routesRaw []byte
	switch req.op {
	case "follow_standard":
		o.List = config.RoutesStandard
		if l.FollowsStandard() {
			o.Outcome, o.Reason = "unchanged", fmt.Sprintf("Loop %s already follows the standard route list.", l.Name)
			o.After = o.Before
			break
		}
		if l.Adapter != "bb" && l.Adapter != "claude" && l.Adapter != "codex" {
			return nil, refuseRoute("not_possible", "The standard list covers bb, claude and codex loops; a %s loop keeps its own list.", l.Adapter)
		}
		if current != state.Primary {
			return nil, refuseRoute("not_on_first_route", "Loop %s is on %q, not its first route. Move it back first with: keepgoing retarget primary", l.Name, current)
		}
		path, err := config.StandardRoutesPath()
		var r *config.Routes
		if err == nil {
			r, err = config.LoadRoutes(path)
		}
		if err != nil {
			code := config.RoutesProblem(err)
			if code == "" {
				return nil, refuseRoute("list_missing", "No standard route list has been written yet. Add a route first with: keepgoing routes add")
			}
			return nil, refuseRoute(code, "The standard route list cannot be used: %s.", routesProblemText(code))
		}
		routesRaw, _ = config.ReadRoutesFile(path)
		loop := l
		loop.Fallbacks, loop.Routes = "", config.RoutesStandard
		after, skipped = r.Resolve(loop)
		// Every id in the list, not only those that apply now: a route
		// switched off, left out for this loop, or not yet allowed can apply
		// later (routes on, routes_allow), and must not inherit what the
		// loop recorded for a route of its own with that id.
		for _, rt := range r.Routes {
			left, err := findFallbackLeftovers(cfg, rt.ID)
			if err != nil {
				return nil, refuseRoute("not_possible", "%v", err)
			}
			if left.any() {
				p.clear = append(p.clear, rt.ID)
			}
		}
		edited, ok := setLoopRoutes(raw, true, "")
		if !ok {
			return nil, refuseRoute("edit_by_hand", "The configuration file's loop section cannot be changed line by line; set loop.routes: standard by hand.")
		}
		// Guided setup also turns handoff on for rate limits, as init does,
		// but only when it is off: a setting of the loop's own is kept.
		handoffOn := req.handoff && len(l.Errors.FailoverOn) == 0
		if handoffOn {
			// Only an inline list is changed, keeping the line; any other
			// form is left to a person rather than re-emitting the file.
			next, ok := setInlineFailoverOn(edited, []string{"rate_limit"})
			if !ok {
				return nil, refuseRoute("edit_by_hand", "The configuration file's errors.failover_on is not written as an inline list, so it cannot be changed line by line; set it to [rate_limit] by hand.")
			}
			edited = next
			o.FailoverOn = &failoverChange{Before: []string{}, After: []string{"rate_limit"}}
		}
		if !sameLoopAfterEdit(raw, edited, func(x *config.Loop) {
			x.Fallbacks, x.Routes = "", config.RoutesStandard
			if handoffOn {
				x.Errors.FailoverOn = []string{"rate_limit"}
			}
		}) {
			return nil, refuseRoute("edit_by_hand", "The configuration file's loop section cannot be changed line by line; set loop.routes: standard by hand.")
		}
		p.configYAML = edited
		if l.Fallbacks != "" {
			o.Warnings = append(o.Warnings, fmt.Sprintf("%s is kept and no longer read; delete it when you no longer need it", filepath.Base(l.Fallbacks)))
		}
		if len(p.clear) > 0 {
			o.Warnings = append(o.Warnings, "check results and sessions recorded under "+strings.Join(p.clear, ", ")+" are cleared first: they may be for another route with the same id")
		}
	case "own":
		o.List = "own"
		if !l.FollowsStandard() {
			if l.Fallbacks != "" {
				o.Outcome, o.Reason = "unchanged", fmt.Sprintf("Loop %s already has its own list.", l.Name)
				o.After = o.Before
				break
			}
			return nil, refuseRoute("not_following", "Loop %s does not follow the standard list; add routes of its own with keepgoing fallbacks add.", l.Name)
		}
		if cfg.RouteList.Unreadable {
			return nil, refuseRoute(cfg.RouteList.Problem, "The standard route list cannot be used (%s), so there is nothing to copy.", cfg.RouteList.Problem)
		}
		if cfg.FallbackSet == nil {
			return nil, refuseRoute("no_route_applies", "No standard route applies to loop %s, so there is nothing to copy; add routes of its own with keepgoing fallbacks add once it has a list.", l.Name)
		}
		if current != state.Primary {
			if c, ok := cfg.FallbackSet.Find(current); !ok || c.Disabled {
				return nil, refuseRoute("current_not_in_list", "Loop %s is on %q, which the standard list no longer gives it. Move it first with: keepgoing retarget primary", l.Name, current)
			}
		}
		if file == "" || file != filepath.Base(file) || strings.HasPrefix(file, ".") {
			return nil, refuseRoute("usage", "--file is a file name beside the configuration file, such as fallbacks.yaml")
		}
		p.fallbacksPath = filepath.Join(filepath.Dir(cfg.Path()), file)
		if _, err := os.Lstat(p.fallbacksPath); !errors.Is(err, os.ErrNotExist) {
			return nil, refuseRoute("file_exists", "%s already exists beside the configuration file; pass --file with a new name.", file)
		}
		after = cfg.FallbackSet
		body, err := yaml.Marshal(config.Fallbacks{Version: 1, Redundancy: after.Redundancy, Probe: after.Probe, Candidates: after.Candidates})
		if err != nil {
			return nil, refuseRoute("not_possible", "The routes cannot be written as a fallbacks file.")
		}
		p.fallbacksYAML = append([]byte("# Copied from the standard route list by keepgoing routes own.\n"), body...)
		parsed, err := config.ParseFallbacks(p.fallbacksYAML)
		if err != nil || !reflect.DeepEqual(parsed.Candidates, after.Candidates) || !reflect.DeepEqual(parsed.Redundancy, after.Redundancy) || !reflect.DeepEqual(parsed.Probe, after.Probe) {
			return nil, refuseRoute("not_possible", "The routes cannot be copied exactly into a fallbacks file.")
		}
		edited, ok := setLoopRoutes(raw, false, file)
		if !ok || !sameLoopAfterEdit(raw, edited, func(x *config.Loop) { x.Fallbacks, x.Routes, x.RoutesAllow = file, "", nil }) {
			return nil, refuseRoute("edit_by_hand", "The configuration file's loop section cannot be changed line by line; set loop.fallbacks by hand.")
		}
		p.configYAML = edited
		// The files as they would be must load as the loop will load them:
		// config.Load also checks what a parse does not, such as a tick
		// timeout too short for the copied probe timeout.
		switch msg, ran := loadsAsWritten(cfg.Path(), edited, file, p.fallbacksYAML); {
		case !ran:
			return nil, refuseRoute("not_possible", "The check that the loop would load with its own copy of the routes could not run, so nothing is written: %s.", msg)
		case msg != "":
			return nil, refuseRoute("not_possible", "The loop would not load with its own copy of the routes, so nothing is written: %s", msg)
		}
		o.Warnings = append(o.Warnings, "changes to the standard list no longer reach this loop; its routes are copied as they resolve now",
			"the copy includes, for each route, what the loop resolves into it from its configuration file: environment values, executable, extra arguments and working directory, and the loop's own conversation for a same-conversation route; later changes to those in the configuration file no longer reach these routes")
	default:
		return nil, refuseRoute("usage", "unknown change %q", req.op)
	}
	if o.Outcome == "preview" {
		o.After = candidateIDs(after)
		if after != nil {
			for _, c := range after.Candidates {
				o.Routes = append(o.Routes, standardRouteRow{ID: c.ID, Provider: c.Provider, Model: c.Model(), Effort: agentEffort(c.Agent), Group: c.Group, Enter: routeEnter(l, c), ProbeModel: c.Probe.Model, Disabled: c.Disabled})
			}
		}
		v := routeChangeLoop{Loop: l.Name, Current: current, Before: o.Before, After: o.After, NewlySkipped: []skippedRoute{}}
		for _, s := range skipped {
			v.NewlySkipped = append(v.NewlySkipped, skippedRoute{ID: s.ID, Reason: s.Reason})
		}
		v.NoRouteApplies = len(v.After) == 0
		if after != nil {
			// As it will be: after the planned clear.
			v.BelowMinHealthy = belowMinHealthy(cfg, after, after.Redundancy.MinHealthy, current, req.now, p.clear...)
		}
		o.Loops = append(o.Loops, v)
		if len(l.Errors.FailoverOn) == 0 && o.FailoverOn == nil {
			o.Warnings = append(o.Warnings, "handoff is off (errors.failover_on is empty); the loop does not hand off until it is turned on")
		}
	}
	var fallbacksRaw []byte
	if l.Fallbacks != "" {
		fallbacksRaw, _ = os.ReadFile(l.Fallbacks)
	}
	h := sha256.New()
	fmt.Fprintf(h, "route_change/1\x00source\x00%s\x00%s\x00%s\x00%x\x00%x\x00%x\x00%s\x00%q\x00%q\x00%t", req.op, l.Name, file, sha256.Sum256(raw), sha256.Sum256(fallbacksRaw), sha256.Sum256(routesRaw), cfg.SourcesDigest(), current, p.clear, req.handoff)
	o.Expect = fmt.Sprintf("%x", h.Sum(nil))
	return p, nil
}

// sameLoopAfterEdit reports whether edited parses to the configuration raw
// parses to, changed only as change says.
func sameLoopAfterEdit(raw, edited []byte, change func(*config.Loop)) bool {
	before, err := config.Parse(raw)
	if err != nil {
		return false
	}
	after, err := config.Parse(edited)
	if err != nil {
		return false
	}
	change(&before.Loop)
	return reflect.DeepEqual(before, after)
}

// setLoopRoutes changes only the lines of keepgoing.yaml that say where the
// loop's other routes come from. toStandard replaces loop.fallbacks with
// loop.routes: standard, or adds that line below loop.name; otherwise
// loop.routes becomes loop.fallbacks: file and loop.routes_allow is removed.
func setLoopRoutes(raw []byte, toStandard bool, file string) ([]byte, bool) {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil, false
	}
	root := yamlRoot(&doc)
	loop := yamlMapValue(root, "loop")
	if loop == nil || loop.Kind != yaml.MappingNode || loop.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	eol := "\n"
	if len(lines) > 0 && strings.HasSuffix(lines[0], "\r\n") {
		eol = "\r\n"
	}
	key := func(name string) (int, *yaml.Node, *yaml.Node) {
		for i := 0; i+1 < len(loop.Content); i += 2 {
			if loop.Content[i].Value == name {
				return i, loop.Content[i], loop.Content[i+1]
			}
		}
		return -1, nil, nil
	}
	// replace puts text in place of a key whose value is one scalar on its
	// line, keeping the indentation and a comment after it.
	replace := func(k, v *yaml.Node, text string) bool {
		l := k.Line - 1
		if v.Kind != yaml.ScalarNode || v.Line != k.Line || l < 0 || l >= len(lines) {
			return false
		}
		line := lines[l]
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		comment := ""
		if v.LineComment != "" {
			comment = " " + v.LineComment
		}
		lines[l] = indent + text + comment + eol
		return true
	}
	// end is the line after a loop key's value: the next key of the loop, or
	// of the file, less blank and comment lines.
	end := func(at int, k *yaml.Node) int {
		limit := len(lines)
		if at+2 < len(loop.Content) {
			limit = loop.Content[at+2].Line - 1
		} else {
			for i := 0; i+1 < len(root.Content); i += 2 {
				if n := root.Content[i].Line - 1; n > k.Line-1 && n < limit {
					limit = n
				}
			}
		}
		for limit > k.Line && (yamlBlank(lines[limit-1]) || yamlComment(lines[limit-1])) {
			limit--
		}
		return limit
	}
	if toStandard {
		if _, k, v := key("fallbacks"); k != nil {
			if !replace(k, v, "routes: standard") {
				return nil, false
			}
			return []byte(strings.Join(lines, "")), true
		}
		_, k, v := key("name")
		if k == nil || v.Kind != yaml.ScalarNode || v.Line != k.Line || k.Column < 1 {
			return nil, false
		}
		at := k.Line
		head := append([]string{}, lines[:at]...)
		terminateLines(head, eol)
		out := append(append(head, strings.Repeat(" ", k.Column-1)+"routes: standard"+eol), lines[at:]...)
		return []byte(strings.Join(out, "")), true
	}
	_, k, v := key("routes")
	if k == nil || !replace(k, v, "fallbacks: "+file) {
		return nil, false
	}
	if at, k, _ := key("routes_allow"); k != nil {
		lines = append(lines[:k.Line-1], lines[end(at, k):]...)
	}
	return []byte(strings.Join(lines, "")), true
}

// writeRouteSource runs under the tick lock. For follow-standard the
// records under the standard ids are cleared before keepgoing.yaml changes,
// so a failure leaves the loop as it was, only with fewer recorded results.
// For own the new fallbacks file is written first and removed again if
// keepgoing.yaml cannot be changed.
func writeRouteSource(p *sourcePlan) error {
	for _, id := range p.clear {
		if err := clearFallbackLeftovers(p.cfg, id); err != nil {
			return fmt.Errorf("the records of %s could not be cleared, so nothing else was changed: %v", id, err)
		}
	}
	if p.fallbacksYAML != nil {
		f, err := os.OpenFile(p.fallbacksPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errors.New("the new fallbacks file could not be created; nothing written")
		}
		_, err = f.Write(p.fallbacksYAML)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p.fallbacksPath)
			return errors.New("the new fallbacks file could not be written; nothing written")
		}
	}
	if err := fallbackAddReplace(p.cfg.Path(), p.configYAML); err != nil {
		if p.fallbacksYAML != nil {
			os.Remove(p.fallbacksPath)
		}
		return errors.New("the configuration file could not be updated; nothing written")
	}
	return nil
}

func routeSourceDone(o routeChangeOut) string {
	if o.Op == "own" {
		return fmt.Sprintf("Loop %s now has its own list: %s.", o.Loop, idList(o.After))
	}
	return fmt.Sprintf("Loop %s now follows the standard route list: %s.", o.Loop, idList(o.After))
}

func printRouteSource(e env, o routeChangeOut) {
	w := e.stdout
	switch o.Outcome {
	case "refused", "review_again":
		e.errf("routes %s: refused, nothing written.\n  %s", strings.ReplaceAll(o.Op, "_", "-"), o.Refusal.Text)
		return
	case "unchanged":
		fmt.Fprintf(w, "%s Nothing was changed.\n", o.Reason)
		return
	case "changed":
		fmt.Fprintln(w, routeSourceDone(o))
		return
	}
	if o.Op == "own" {
		fmt.Fprintf(w, "Loop %s: stop following the standard route list and keep a copy of its routes as the loop's own.\n", o.Loop)
	} else {
		fmt.Fprintf(w, "Loop %s: follow the standard route list.\n", o.Loop)
	}
	if f := o.FailoverOn; f != nil {
		fmt.Fprintf(w, "  errors.failover_on: [%s] -> [%s]: the loop hands off to these routes on a rate limit\n", strings.Join(f.Before, ", "), strings.Join(f.After, ", "))
	}
	for _, l := range o.Loops {
		fmt.Fprintf(w, "  on %s; hands off to %s -> %s\n", l.Current, idList(l.Before), idList(l.After))
		for _, s := range l.NewlySkipped {
			fmt.Fprintf(w, "    not used here: %s (%s)\n", s.ID, s.Reason)
		}
		if l.NoRouteApplies {
			fmt.Fprintln(w, "    note: no route is left to hand off to")
		} else if l.BelowMinHealthy {
			fmt.Fprintln(w, "    note: fewer healthy routes than redundancy.min_healthy by the last checks")
		}
	}
	for _, warning := range o.Warnings {
		fmt.Fprintf(w, "note: %s\n", warning)
	}
}

// ownCheckPrefix names the private directories own checks its copy in.
const ownCheckPrefix = ".routes-own-"

// ownCheckStale is how old a check directory must be to count as left by an
// own that was killed: a check takes well under a second.
const ownCheckStale = 10 * time.Minute

// removeOwnCheckLeftovers removes check directories an earlier own left
// beside keepgoing.yaml when it was killed. They may hold copies of the
// configuration, environment values included. Only directories this user
// owns, last changed more than ownCheckStale ago, are removed, so a check
// another own is running now is left alone; a link or file with the name
// is never touched.
func removeOwnCheckLeftovers(cfgPath string) {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(cfgPath), ownCheckPrefix+"*"))
	for _, m := range matches {
		if info, err := os.Lstat(m); err == nil && info.IsDir() && config.OwnedByUser(info) && time.Since(info.ModTime()) > ownCheckStale {
			_ = os.RemoveAll(m)
		}
	}
}

// loadsAsWritten loads the configuration and its new fallbacks file as they
// would be written, from a private directory beside keepgoing.yaml under
// the same names. It returns why the load fails, naming no path, or, with
// ran false, why the check could not run.
func loadsAsWritten(cfgPath string, configYAML []byte, file string, fallbacksYAML []byte) (string, bool) {
	removeOwnCheckLeftovers(cfgPath)
	dir, err := os.MkdirTemp(filepath.Dir(cfgPath), ownCheckPrefix+"*")
	if err != nil {
		return "a private directory to check it in cannot be created beside the configuration file (is its directory writable?)", false
	}
	defer os.RemoveAll(dir)
	cfgCopy := filepath.Join(dir, filepath.Base(cfgPath))
	if os.WriteFile(cfgCopy, configYAML, 0o600) != nil || os.WriteFile(filepath.Join(dir, file), fallbacksYAML, 0o600) != nil {
		return "the files to check could not be written beside the configuration file", false
	}
	if _, err := config.Load(cfgCopy); err != nil {
		msg := strings.ReplaceAll(err.Error(), dir+string(os.PathSeparator), "")
		return strings.ReplaceAll(msg, dir, ""), true
	}
	return "", true
}
