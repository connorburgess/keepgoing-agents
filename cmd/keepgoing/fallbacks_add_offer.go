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
// fallback of its own. It shows the change first, so the answer is the
// confirmation. A yes makes the change through routes follow-standard and
// ends the wizard; a no changes nothing and the wizard goes on. It reports
// whether the wizard is done, and with what exit code.
func (w *fallbackAddWizard) offerStandardList() (bool, int) {
	cfg := w.cfg
	l := cfg.Loop
	if cfg.FallbackSet != nil || l.FollowsStandard() || !initCanFollow(l.Adapter) {
		return false, exitOK
	}
	path, err := config.StandardRoutesPath()
	if err != nil {
		return false, exitOK
	}
	r, err := config.LoadRoutes(path)
	if err != nil || len(r.Routes) == 0 {
		return false, exitOK
	}
	req := routeChangeRequest{op: "follow_standard", handoff: true, caller: os.Getenv("BB_THREAD_ID"), source: "cli", now: time.Now().UTC()}
	preview, _ := routeSourceChange(req, cfg.Path(), "", false)
	if preview.Outcome != "preview" || len(preview.After) == 0 {
		return false, exitOK // the list gives this loop nothing, or it cannot follow now
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
	fmt.Fprintln(w.e.stdout)
	yes, err := w.askYesNo(fmt.Sprintf("Follow your standard route list and %s, instead of adding a fallback? %d of its %d routes %s to this loop. (yes/no)", how, len(preview.After), len(r.Routes), verb), "yes")
	if err != nil {
		w.e.errf("fallbacks add: stopped, nothing written.\n  %v", err)
		return true, exitUsage
	}
	if !yes {
		fmt.Fprintln(w.e.stdout)
		return false, exitOK
	}
	req.expect = preview.Expect
	done, code := routeSourceChange(req, cfg.Path(), "", true)
	if done.Outcome != "changed" {
		printRouteSource(w.e, done)
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
