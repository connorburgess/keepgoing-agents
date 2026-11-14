package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// offerStandardList is the first question of guided fallbacks add for a
// loop with no other routes, when a standard route list gives it routes:
// follow that list, and hand off to it on a rate limit, instead of adding a
// fallback of its own. It shows the change first. Only a typed yes makes it,
// through routes follow-standard, and ends the wizard; a blank line, the end
// of input or any other answer is no, changes nothing, and the wizard goes
// on. Under --dry-run it says what yes would do and asks nothing. It is not
// offered when the command line already describes a fallback. It reports
// whether the wizard is done, and with what exit code.
func (w *fallbackAddWizard) offerStandardList() (bool, int) {
	cfg := w.cfg
	l := cfg.Loop
	if cfg.FallbackSet != nil || l.FollowsStandard() || !initCanFollow(l.Adapter) || describesFallback(w.o.set) {
		return false, exitOK
	}
	path, err := config.StandardRoutesPath()
	if err != nil {
		return false, exitOK
	}
	r, err := config.LoadRoutes(path)
	if err != nil {
		return false, exitOK
	}
	on := switchedOn(r)
	if on == 0 {
		return false, exitOK
	}
	req := routeChangeRequest{op: "follow_standard", handoff: true, caller: os.Getenv("BB_THREAD_ID"), source: "cli", now: time.Now().UTC()}
	preview, _ := routeSourceChange(req, cfg.Path(), "", false)
	if preview.Outcome != "preview" {
		// Said only when it is the one thing in the way.
		if preview.Refusal != nil && preview.Refusal.Code == "edit_by_hand" && strings.Contains(preview.Refusal.Text, "failover_on") {
			fmt.Fprintln(w.e.stdout, "Your standard route list is not offered here: this loop's errors.failover_on is not written as an inline list ([...]), so it cannot be turned on line by line.")
		}
		return false, exitOK
	}
	if len(preview.After) == 0 {
		return false, exitOK // the list gives this loop nothing
	}
	fmt.Fprintln(w.e.stdout, "This loop has no other routes, and your standard route list has routes for it.")
	printRouteSource(w.e, preview)
	verb := "apply"
	if len(preview.After) == 1 {
		verb = "applies"
	}
	how := "hand off to it on a rate limit"
	if preview.FailoverOn == nil {
		how = "hand off to it on " + strings.Join(l.Errors.FailoverOn, ", ") + ", as the loop already does"
	}
	if w.o.dryRun {
		fmt.Fprintf(w.e.stdout, "\nDry run: nothing written. Without --dry-run you would be asked whether to follow your standard route list and %s; %d of its %d switched-on routes %s to this loop. The fallback preview follows.\n\n", how, len(preview.After), on, verb)
		return false, exitOK
	}
	fmt.Fprintln(w.e.stdout)
	if !w.askYesOnly(fmt.Sprintf("Follow your standard route list and %s, instead of adding a fallback? %d of its %d switched-on routes %s to this loop. Type yes to follow it (yes/no)", how, len(preview.After), on, verb)) {
		fmt.Fprintln(w.e.stdout, "  Not following the standard route list; nothing was written.")
		fmt.Fprintln(w.e.stdout)
		return false, exitOK
	}
	req.expect = preview.Expect
	done, code := routeSourceChange(req, cfg.Path(), "", true)
	if done.Outcome != "changed" {
		text := "the change could not be made"
		if done.Refusal != nil {
			text = done.Refusal.Text
		}
		w.e.errf("fallbacks add: following the standard route list was refused, nothing written.\n  %s", text)
		return true, code
	}
	fmt.Fprintf(w.e.stdout, "\n%s\nA running daemon uses the change after its next configuration reload.\n", routeSourceDone(done))
	for _, warning := range done.Warnings {
		if strings.Contains(warning, "could not be recorded") {
			fmt.Fprintf(w.e.stdout, "Note: %s.\n", warning)
		}
	}
	return true, code
}

// askYesOnly asks once, with no as the default: only a typed yes (or y)
// counts. A blank line, the end of input or anything else is no.
func (w *fallbackAddWizard) askYesOnly(label string) bool {
	if w.e.ctx.Err() != nil {
		return false
	}
	fmt.Fprintf(w.e.stdout, "  %s [no]: ", label)
	s, err := readInitLine(w.e.ctx, w.r)
	if err != nil && s == "" {
		fmt.Fprintln(w.e.stdout)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y":
		return true
	}
	return false
}

// describesFallback reports whether the command line already says what
// fallback to add, so the person has chosen.
func describesFallback(set map[string]bool) bool {
	for name := range set {
		switch name {
		case "c", "dry-run", "show-ids":
		default:
			return true
		}
	}
	return false
}

// switchedOn counts the routes of the list that are switched on.
func switchedOn(r *config.Routes) int {
	n := 0
	for _, rt := range r.Routes {
		if !rt.Disabled {
			n++
		}
	}
	return n
}
